// Command server runs the Lattice homeserver: a minimal Matrix
// client-server API implementation backed by SQLite.
package main

import (
	"context"
	"crypto/x509"
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
	"github.com/enimatek-nl/lattice/internal/cli"
	"github.com/enimatek-nl/lattice/internal/config"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/service"
)

func main() {
	// The binary has two halves. With flags in front of it, it is the homeserver - which
	// is how README, the Makefile and the container entrypoint start it, and how it stays
	// started. With a command word in front of them, it is one of the operator utilities
	// in internal/cli: the same settings read the same way, working on the database
	// instead of serving it. That is what lets a server run with -disable-registration
	// and still gain an account.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		// A first word that is not a command is a typo, and it is never the homeserver.
		// Every way of starting the server begins with a flag, while `lattice adduser`
		// misspelled would otherwise open a listening port and write a fresh database
		// into whatever directory the operator happened to be standing in.
		if !cli.IsCommand(os.Args[1]) {
			fmt.Fprintf(os.Stderr, "lattice: %q is not a command\n", os.Args[1])
			cli.Usage(os.Stderr)
			os.Exit(2)
		}
		if err := cli.Run(os.Args[1], os.Args[2:], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "lattice:", err)
			os.Exit(1)
		}
		return
	}

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
		federationAdvertise = flag.String("federation-advertise", "",
			"host:port published under /.well-known/matrix/server as where peers reach this server, including for its signing key; empty means the server name, which means port 8448")
		tlsCert = flag.String("tls-cert", "",
			"serve TLS with this certificate; with -tls-addr it is a second listener for federation traffic only")
		tlsKey  = flag.String("tls-key", "", "the private key of -tls-cert")
		tlsAddr = flag.String("tls-addr", "",
			"additional address to serve over TLS, for deployments where the federation port is served with a certificate and the client port is not")
		caFiles = flag.String("ca-cert", "",
			"comma separated PEM certificate authorities to trust when reaching peers; empty means the system trust store")
		federationFlush = flag.Duration("federation-flush", time.Second,
			"how often federation traffic waiting for a peer is retried; 0 delivers only on demand")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.CommandLine.Usage = func() {
		flag.Usage()
		// The operator commands belong in the same help output as the flags, because
		// everyone who reads one is looking for the other.
		_, _ = fmt.Fprintln(flag.CommandLine.Output())
		cli.Usage(flag.CommandLine.Output())
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("lattice", service.LatticeVersion)
		return
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("lattice: ")

	// Every flag above can also be given as LATTICE_<NAME>: -server-name as
	// LATTICE_SERVER_NAME, -federation-advertise as LATTICE_FEDERATION_ADVERTISE. The
	// container image is configured that way, because a container gets variables
	// rather than a command line, and an operator writing a systemd unit gets the same
	// interface. The command line still wins, so a flag typed by hand overrides what
	// the image or the orchestrator set.
	takenFromEnv, err := config.ApplyEnvDefaults(flag.CommandLine, os.LookupEnv)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	if len(takenFromEnv) > 0 {
		// Worth one line: the difference between "my token stopped working on restart"
		// and an answer is knowing whether LATTICE_JWT_SECRET ever arrived.
		log.Printf("settings taken from the environment: %s", strings.Join(takenFromEnv, " "))
	}

	db, err := persist.NewDatabase(*dbPath)
	if err != nil {
		log.Fatalf("opening database %s: %v", *dbPath, err)
	}

	// The secret that signs access tokens is kept in the database when the operator did not
	// name one. A secret thrown away at shutdown would log every user out at the next start:
	// the sessions it made are rows in that database, and the signatures that let them be read
	// are the only thing tying them together.
	secret, generated, err := auth.TokenSecret(db, *jwtSecret)
	if err != nil {
		log.Fatalf("reading the access token secret: %v", err)
	}
	if generated {
		log.Print("no --jwt-secret given; generated one and stored it in the database, where it stays across restarts")
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

	// Outbound federation is TLS first. A deployment behind a private CA names that
	// CA; nothing here disables verification, because the certificate is what stands
	// between this server and somebody who knows its name.
	var federationCAs *x509.CertPool
	if *caFiles != "" {
		pool, err := federation.LoadCAs(splitList(*caFiles))
		if err != nil {
			log.Fatalf("certificate authorities: %v", err)
		}
		federationCAs = pool
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
		Federation:          *federationOn,
		FederationPeers:     peerOverrides(*federationPeers),
		FederationAdvertise: *federationAdvertise,
		FederationRootCAs:   federationCAs,
	})

	if *federationOn {
		// Saying which key the server signs with turns "my peer refuses me" into a
		// line an operator can compare against what the peer fetched.
		if key, err := svc.SigningKey(context.Background()); err == nil {
			log.Printf("federation on: signing as %s with key %s", *serverName, key.ID)
		}
		// And where a peer will look for that key. Every federation request this server
		// sends is signed, and a peer that cannot fetch the key document refuses the request
		// - which reaches the operator as a peer answering an error, and reaches their client
		// as a room that will not open. The address is not this server's to choose: it comes
		// from the server name, and from what /.well-known/matrix/server at that name says.
		log.Printf("federation: peers verify requests from this server against "+
			"https://%s/_matrix/key/v2/server, or against the host and port that "+
			"/.well-known/matrix/server at %s names - that address has to answer over TLS, and "+
			"serve the key route, or no peer will believe anything sent from here", *serverName, *serverName)
		if *federationAdvertise != "" {
			log.Printf("federation: /.well-known/matrix/server advertises %s", *federationAdvertise)
		} else {
			log.Printf("federation: /.well-known/matrix/server advertises %s, so peers will knock on port 8448; "+
				"a deployment reached through a reverse proxy should pass -federation-advertise host:port", *serverName)
		}
	}

	handler := applyCORS(app.Handler(), *corsOrigin)

	// A server name may carry a port - that is legal, and a name like localhost:8448 is
	// ordinary. But the name is what every account and room ID ends with, so a port added
	// to it makes a new server out of an existing one: accounts written under the name
	// without the port stop being able to use it. The port peers should dial is a separate
	// thing, and belongs in -federation-advertise.
	if host, _, hasPort := federation.ServerNameParts(*serverName); hasPort {
		log.Printf("warning: -server-name %s carries a port, so every account and room here ends with it; "+
			"accounts created under %s cannot use this server. Where peers should reach this server is set "+
			"with -federation-advertise, which does not change any ID", *serverName, host)
	}

	// Accounts are keyed by their full user ID, which carries the server name they were
	// created for. Change -server-name over a database that already has accounts and every
	// one of them keeps authenticating - the sessions are rows in this file - while being
	// unable to join a room, send an event or be invited. That is invisible until someone
	// tries, and the failure reads like a missing account.
	if total, err := db.UserRepo.Count(); err == nil && total > 0 {
		if stray, err := db.UserRepo.CountOutsideDomain(*serverName); err == nil && stray > 0 {
			log.Printf("warning: %d of %d accounts here are not accounts of %s: they can still "+
				"sign in and read this server, but cannot join rooms or send events; -server-name has "+
				"to be the name those accounts were created for", stray, total, *serverName)
		}
	}

	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatalf("-tls-cert and -tls-key are given together")
	}
	if *tlsAddr != "" && *tlsCert == "" {
		log.Fatalf("-tls-addr needs -tls-cert and -tls-key: a second listener that is not TLS is the same listener twice")
	}

	// The API is one handler with possibly two doors. A deployment that terminates TLS
	// in a proxy serves plain HTTP on the client port; the federation port is a
	// different matter, because a peer validates the certificate against the server name
	// that appears in every event id it has ever seen - and because the specification
	// sends federation traffic to https://<server name>.
	type door struct {
		server *http.Server
		// secure says whether this door is served over TLS.
		secure bool
	}
	newServer := func(address string) *http.Server {
		return &http.Server{
			Addr:              address,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			// Sync long polling needs a generous write timeout.
			WriteTimeout: 0,
		}
	}
	// With a certificate and no second address, TLS is how the one address is served.
	// With a second address, the first stays plain HTTP and the second is the TLS door
	// peers reach.
	doors := []door{{server: newServer(*addr), secure: *tlsCert != "" && *tlsAddr == ""}}
	if *tlsCert != "" && *tlsAddr != "" {
		doors = append(doors, door{server: newServer(*tlsAddr), secure: true})
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Housekeeping runs until the process is asked to stop; the returned function
	// cancels the loop and waits for it, so no goroutine outlives the database.
	stopMaintenance := svc.StartMaintenance(ctx, *maintenancePeriod)
	// Federation traffic is queued rather than sent inline, so something has to put it on
	// the wire. A peer that is down is retried with backoff from this loop; the interval
	// is how long a message waits before its first attempt leaves.
	// Worth one line at start-up: a backlog that survives a restart is the queue working,
	// and a backlog that an operator did not know about explains a room that stopped
	// receiving messages hours ago.
	if waiting, err := db.OutboxRepo.Count(); err == nil && waiting > 0 {
		log.Printf("federation: %d queued transaction(s) from an earlier run are waiting for peers", waiting)
	}
	stopOutbox := svc.StartFederationOutbox(ctx, *federationFlush)

	errs := make(chan error, len(doors))
	for _, entry := range doors {
		entry := entry
		go func() {
			if entry.secure {
				log.Printf("listening on %s as %s over TLS with database %s", entry.server.Addr, *serverName, *dbPath)
				errs <- entry.server.ListenAndServeTLS(*tlsCert, *tlsKey)
				return
			}
			log.Printf("listening on %s as %s with database %s", entry.server.Addr, *serverName, *dbPath)
			errs <- entry.server.ListenAndServe()
		}()
	}

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
	for _, entry := range doors {
		if err := entry.server.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
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
