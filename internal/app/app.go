// Package app wires the application: configuration, storage, services and the
// HTTP surface. Both cmd/lattice and the integration tests construct an App, so
// tests exercise the same wiring the binary uses.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/api/client"
	"github.com/enimatek/lattice/internal/api/federation"
	"github.com/enimatek/lattice/internal/api/media"
	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/e2ekeys"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/keys"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/presence"
	"github.com/enimatek/lattice/internal/pushrules"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/sync"
	"github.com/enimatek/lattice/internal/tlsgen"
	"github.com/enimatek/lattice/internal/users"
)

// Version is the server version reported by /versions, /version and /healthz.
const Version = "0.1.0-dev"

// App is the assembled homeserver.
type App struct {
	Cfg       *config.Config
	Log       *slog.Logger
	DB        *persist.Database
	Keys      *keys.Service
	Rooms     *rooms.Service
	Tokens    *auth.TokenService
	UIA       *auth.UIAService
	Users     *users.Service
	PushRules *pushrules.Service
	Presence  *presence.Service
	Server    *httpserver.Server

	Started time.Time

	clientRouter     *httpserver.Router
	federationRouter *httpserver.Router
}

// New builds the application without binding any socket.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	if log == nil {
		return nil, fmt.Errorf("app: a logger is required")
	}
	db, err := persist.Open(cfg.DatabasePath(), log)
	if err != nil {
		return nil, err
	}

	signingKeys, err := keys.Load(ctx, cfg.ServerName, db.SigningKeys)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	tokens := auth.NewTokenService(db.Tokens)
	uia := auth.NewUIAService(5*time.Minute, 8192)
	userService := users.New(db, tokens, cfg.Registration, cfg.ServerName, log)
	presenceService := presence.New(db, cfg.ServerName, log)
	pushRules := pushrules.NewService(db, log)
	deviceKeys := e2ekeys.New(db, log)
	roomService, err := rooms.New(db, signingKeys, presenceService, pushRules, cfg.ServerName, log)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	// What a user owns from the moment they exist: their push rules, and a presence
	// entry so that the first question about them is answered with something this server
	// knows rather than with a guess. Accounts that predate this code are covered by the
	// sweeps below.
	userService.AfterRegister = func(ctx context.Context, userID string) error {
		if err := pushRules.EnsureDefaults(ctx, userID); err != nil {
			return err
		}
		return presenceService.Set(ctx, userID, persist.PresenceOnline, "")
	}

	a := &App{
		Cfg:              cfg,
		Log:              log,
		DB:               db,
		Keys:             signingKeys,
		Tokens:           tokens,
		UIA:              uia,
		Users:            userService,
		PushRules:        pushRules,
		Presence:         presenceService,
		Started:          time.Now().UTC(),
		clientRouter:     httpserver.NewRouter(),
		federationRouter: httpserver.NewRouter(),
	}

	if err := pushRules.EnsureAllDefaults(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if created, err := presenceService.EnsureDefaults(ctx); err != nil {
		_ = db.Close()
		return nil, err
	} else if created > 0 {
		log.Info("presence: gave the existing accounts a presence entry", "accounts", created)
	}

	requireAuth := auth.RequireAuth(tokens, userService)
	client.Register(a.clientRouter, &client.Deps{
		Cfg:       cfg,
		Log:       log,
		Users:     userService,
		Tokens:    tokens,
		UIA:       uia,
		Keys:      signingKeys,
		Rooms:     roomService,
		PushRules: pushRules,
		Presence:  presenceService,
		E2EKeys:   deviceKeys,
		Sync:      sync.NewService(db, log),
		DB:        db,
		Version:   Version,
		Started:   a.Started,
	}, requireAuth)
	media.Register(a.clientRouter, &media.Deps{Cfg: cfg}, requireAuth)
	// Method negotiation must be consistent across listeners: a federation path
	// probed on the client port answers 405, not 404.
	defer a.clientRouter.MirrorFrom(a.federationRouter)
	federation.Register(a.federationRouter, &federation.Deps{
		Cfg:     cfg,
		Log:     log,
		Keys:    signingKeys,
		Version: Version,
	})

	a.Server = httpserver.New(httpserver.Config{
		ClientAddr:        cfg.HTTP.ClientAddr,
		FederationAddr:    cfg.HTTP.FederationAddr,
		CertFile:          cfg.TLS.CertFile,
		KeyFile:           cfg.TLS.KeyFile,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout.Get(),
		WriteTimeout:      cfg.HTTP.WriteTimeout.Get(),
		IdleTimeout:       cfg.HTTP.IdleTimeout.Get(),
		ShutdownTimeout:   cfg.HTTP.ShutdownTimeout.Get(),
		Logger:            log,
	})
	a.Server.SetHandlers(a.ClientHandler(), a.FederationHandler())
	return a, nil
}

// ClientHandler returns the Client-Server API handler, wrapped in the middleware
// chain, for use by tests and by the HTTP server.
func (a *App) ClientHandler() http.Handler {
	return httpserver.Chain(a.clientRouter.Handler(),
		httpserver.Recover(a.Log),
		httpserver.RequestID(),
		httpserver.AccessLog(a.Log),
		httpserver.SecurityHeaders(),
		httpserver.MaxBody(a.Cfg.HTTP.MaxBodyBytes),
		httpserver.Timeout(a.Cfg.HTTP.RequestTimeout.Get()),
	)
}

// FederationHandler returns the Server-Server API handler.
func (a *App) FederationHandler() http.Handler {
	return httpserver.Chain(a.federationRouter.Handler(),
		httpserver.Recover(a.Log),
		httpserver.RequestID(),
		httpserver.AccessLog(a.Log),
		httpserver.SecurityHeaders(),
		httpserver.MaxBody(a.Cfg.HTTP.FederationMaxBodyBytes),
		httpserver.Timeout(a.Cfg.HTTP.RequestTimeout.Get()),
	)
}

// PrepareTLS creates the federation certificate when one is needed. A configured
// CA wins over self-generation, because a CA-signed certificate is what other
// homeservers can actually verify.
func (a *App) PrepareTLS(now time.Time) error {
	notBefore, notAfter := tlsgen.Validity(now)
	if a.Cfg.TLS.CACertFile != "" && a.Cfg.TLS.CAKeyFile != "" {
		return tlsgen.EnsureSignedByCA(a.Cfg.TLS.CertFile, a.Cfg.TLS.KeyFile, a.Cfg.ServerName,
			a.Cfg.TLS.CACertFile, a.Cfg.TLS.CAKeyFile, notBefore, notAfter)
	}
	if !a.Cfg.TLS.AutoGenerate {
		return nil
	}
	return tlsgen.Ensure(a.Cfg.TLS.CertFile, a.Cfg.TLS.KeyFile, a.Cfg.ServerName, notBefore, notAfter)
}

// Prepare generates the federation certificate and binds the listeners, so the
// resolved addresses are known before serving starts. Tests use this to learn the
// ephemeral ports.
func (a *App) Prepare() error {
	if err := a.PrepareTLS(time.Now()); err != nil {
		return err
	}
	return a.Server.Listen()
}

// Serve serves on the bound listeners until ctx is cancelled.
func (a *App) Serve(ctx context.Context) error { return a.Server.Serve(ctx) }

// Run prepares the server and serves until ctx is cancelled.
func (a *App) Run(ctx context.Context) error {
	if err := a.Prepare(); err != nil {
		return err
	}
	return a.Serve(ctx)
}

// ClientAddr returns the resolved client API address.
func (a *App) ClientAddr() string { return a.Server.ClientAddr() }

// FederationAddr returns the resolved federation address.
func (a *App) FederationAddr() string { return a.Server.FederationAddr() }

// Close releases resources.
func (a *App) Close() error {
	if a.Server != nil {
		a.Server.CloseListeners()
	}
	if a.DB != nil {
		return a.DB.Close()
	}
	return nil
}
