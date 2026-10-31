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
		service.WithRegistrationSecrets(splitList(*registrationSecrets)...),
		service.WithPublicBaseURL(*publicBaseURL),
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
	})

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
