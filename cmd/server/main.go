// Command server runs the Lattice homeserver: a minimal Matrix
// client-server API implementation backed by SQLite.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rs/cors"

	"github.com/enimatek-nl/lattice/internal/api"
	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

func main() {
	var (
		addr              = flag.String("addr", ":8008", "address to listen on")
		serverName        = flag.String("server-name", "localhost", "homeserver name; user and room IDs end with it")
		publicBaseURL     = flag.String("base-url", "", "URL clients reach this server on, published in /.well-known/matrix/client; defaults to https://<server-name>")
		dbPath            = flag.String("db", "lattice.db", "SQLite database file")
		jwtSecret         = flag.String("jwt-secret", "", "secret used to sign access tokens; generated when empty")
		tokenTTL          = flag.Duration("token-ttl", 24*time.Hour, "access token lifetime")
		maxUpload         = flag.Int64("max-upload-size", service.DefaultMaxUploadSize, "maximum media upload size in bytes")
		reservationWindow = flag.Duration("media-reservation-window", service.DefaultMediaReservationWindow,
			"how long a media id created by POST /_matrix/media/v3/create waits for its bytes")
		allowGuests       = flag.Bool("allow-guests", true, "allow guest account creation")
		disableReg        = flag.Bool("disable-registration", false, "disable self-service registration")
		corsOrigin        = flag.String("cors-origin", "*", "allowed CORS origin, or a comma separated list")
		supportEmail      = flag.String("support-email", "", "publish a support email address in the well-known document")
		supportID         = flag.String("support-matrix-id", "", "publish a matrix ID for support in the well-known document")
		mediaEnabled      = flag.Bool("media", true, "enable the media endpoints")
		maintenancePeriod = flag.Duration("maintenance-interval", service.MaintenancePeriod,
			"how often to prune expired to-device messages, transactions and typing rows; 0 disables it")
		registrationSecrets = flag.String("registration-secret", "",
			"comma separated registration tokens; when set, registering requires one and the open m.login.dummy flow disappears")
		rateLimitOff = flag.Bool("no-rate-limit", false,
			"turn off the request limits on the expensive writes, media uploads and login attempts")
		writeBurst = flag.Int("rate-limit-write-burst", api.DefaultRateLimits().WriteBurst,
			"how many room writes one device may send in a burst")
		writeRate = flag.Float64("rate-limit-write-rate", api.DefaultRateLimits().WriteRate,
			"how many room writes per second one device's budget refills to")
		authBurst = flag.Int("rate-limit-auth-burst", api.DefaultRateLimits().AuthBurst,
			"how many login, refresh or registration attempts one address may make in a burst")
		authRate = flag.Float64("rate-limit-auth-rate", api.DefaultRateLimits().AuthRate,
			"how many authentication attempts per second one address's budget refills to")
		uploadBurst = flag.Int("rate-limit-upload-burst", api.DefaultRateLimits().UploadBurst,
			"how many media uploads one device may start in a burst")
		uploadRate = flag.Float64("rate-limit-upload-rate", api.DefaultRateLimits().UploadRate,
			"how many media uploads one device's budget refills to per second")
		federationOn = flag.Bool("federation", false,
			"serve the server-server API: other homeservers can talk to this one")
		federationPeers = flag.String("federation-peer", "",
			"comma separated server=URL pairs that pin where a peer is reached, bypassing well-known delegation; for tunnels and tests")
		federationFlush = flag.Duration("federation-flush", time.Second,
			"how often federation traffic waiting for a peer is retried; 0 delivers only on demand")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("lattice", service.LatticeVersion)
		return
	}

	secret := *jwtSecret
	if secret == "" {
		secret = randomSecret()
		log.Print("no --jwt-secret given; generated an ephemeral one, access tokens stop working on restart")
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("lattice: ")

	db, err := persist.NewDatabase(*dbPath)
	if err != nil {
		log.Fatalf("opening database %s: %v", *dbPath, err)
	}
	tokens := auth.NewTokenService(secret, auth.WithTTL(*tokenTTL))
	svc, err := service.New(db, *serverName, tokens,
		service.WithMaxUploadSize(*maxUpload),
		service.WithMediaReservationWindow(*reservationWindow),
		service.WithRegistrationSecrets(splitList(*registrationSecrets)...),
		service.WithPublicBaseURL(*publicBaseURL),
		service.WithFederation(*federationOn),
	)
	if err != nil {
		log.Fatalf("starting service: %v", err)
	}

	app := api.New(svc, auth.NewStore(tokens, db), api.Config{
		ServerName:           *serverName,
		Version:              service.LatticeVersion,
		RegistrationDisabled: *disableReg,
		AllowGuests:          *allowGuests,
		EnableMedia:          *mediaEnabled,
		MaxUploadSize:        *maxUpload,
		SupportEmail:         *supportEmail,
		AdminMatrixID:        *supportID,
		RateLimits: api.RateLimits{
			Disabled:    *rateLimitOff,
			WriteBurst:  *writeBurst,
			WriteRate:   *writeRate,
			AuthBurst:   *authBurst,
			AuthRate:    *authRate,
			UploadBurst: *uploadBurst,
			UploadRate:  *uploadRate,
		},
		Federation:      *federationOn,
		FederationPeers: peerOverrides(*federationPeers),
	})

	if *federationOn {
		// Saying which key the server signs with turns "my peer refuses me" into a
		// line an operator can compare against what the peer fetched.
		if key, err := svc.SigningKey(context.Background()); err == nil {
			log.Printf("federation on: signing as %s with key %s", *serverName, key.ID)
		}
	}

	handler := applyCORS(app.Handler(), *corsOrigin)
	server := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		// Sync long polling needs a generous write timeout.
		WriteTimeout: 0,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Housekeeping runs until the process is asked to stop; the returned function
	// cancels the loop and waits for it, so no goroutine outlives the database.
	stopMaintenance := svc.StartMaintenance(ctx, *maintenancePeriod)
	// Federation traffic is queued rather than sent inline, so something has to put it on
	// the wire. A peer that is down is retried with backoff from this loop; the interval
	// is how long a message waits before its first attempt leaves.
	stopOutbox := svc.StartFederationOutbox(ctx, *federationFlush)

	errs := make(chan error, 1)
	go func() {
		log.Printf("listening on %s as %s with database %s", *addr, *serverName, *dbPath)
		errs <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Print("shutting down")
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serving: %v", err)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	stopOutbox()
	stopMaintenance()
}

// applyCORS wraps a handler with the configured cross origin policy.
func applyCORS(handler http.Handler, origins string) http.Handler {
	c := cors.New(cors.Options{
		AllowedOrigins:   splitList(origins),
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Requested-With"},
		AllowCredentials: true,
		MaxAge:           86400,
	})
	return c.Handler(handler)
}

// splitList splits a comma separated list into trimmed, non-empty entries.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// randomSecret returns a token used to sign access tokens.
func randomSecret() string {
	token := matrix.NewToken()
	if token == "" {
		log.Fatal("could not generate a signing secret")
	}
	return token
}

// peerOverrides parses the server=URL pairs of --federation-peer. A pin bypasses
// well-known delegation and TLS for that name, which is how two homeservers on one
// machine reach each other, and how a peer behind a fixed tunnel is named.
func peerOverrides(list string) map[string]string {
	if strings.TrimSpace(list) == "" {
		return nil
	}
	overrides := map[string]string{}
	for _, entry := range splitList(list) {
		name, url, found := strings.Cut(entry, "=")
		if !found || name == "" || url == "" {
			log.Fatalf("--federation-peer expects server=URL pairs, got %q", entry)
		}
		overrides[name] = strings.TrimRight(url, "/")
	}
	return overrides
}
