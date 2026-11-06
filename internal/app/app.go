// Package app wires the application: configuration, storage, services and the
// HTTP surface. Both cmd/lattice and the integration tests construct an App, so
// tests exercise the same wiring the binary uses.
package app

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/api/client"
	federationapi "github.com/enimatek/lattice/internal/api/federation"
	"github.com/enimatek/lattice/internal/api/media"
	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/e2ekeys"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/keys"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/presence"
	"github.com/enimatek/lattice/internal/pushrules"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/roomversion"
	"github.com/enimatek/lattice/internal/sync"
	"github.com/enimatek/lattice/internal/tlsgen"
	"github.com/enimatek/lattice/internal/users"
	"golang.org/x/sync/errgroup"
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
	// Sender is the federation outbound worker: the queue of events still owed to other
	// homeservers. It is nil when federation is disabled.
	Sender *federation.Sender
	// Keyring holds the signing keys of other homeservers.
	Keyring *federation.Keyring
	// Backfill asks other homeservers for the events this server's rooms cite and does not hold.
	// It is nil when federation is disabled.
	Backfill *federation.Backfiller
	// Joiner carries this server's users into rooms it does not hold. It is nil when
	// federation is disabled, and a join of a remote room is then refused plainly rather than
	// attempted against nothing.
	Joiner *federation.Joiner

	Started time.Time

	clientRouter     *httpserver.Router
	federationRouter *httpserver.Router
}

// Options are the parts of assembly a test replaces. They are deliberately narrow: a
// test may supply its own way of reaching another homeserver, but not its own signing,
// key checking or event handling — those are the things under test.
type Options struct {
	// FederationResolver and FederationTransport replace how this server dials other
	// homeservers. An in-process test of two homeservers has no DNS and no TLS
	// listener, but the request it signs, the credential it sends and the credential it
	// checks are exactly what a deployed server would send.
	FederationResolver  federation.Resolver
	FederationTransport http.RoundTripper
	// FederationTargets maps a server name to the address to dial, which is how a test
	// points one homeserver at another without a DNS server.
	FederationTargets map[string]federation.Target
}

// New builds the application without binding any socket.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	return NewWithOptions(ctx, cfg, log, Options{})
}

// NewWithOptions builds the application with the test hooks in opts. opts is zero for a
// normal start.
func NewWithOptions(ctx context.Context, cfg *config.Config, log *slog.Logger, opts Options) (*App, error) {
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
		Rooms:            roomService,
		Tokens:           tokens,
		UIA:              uia,
		Users:            userService,
		PushRules:        pushRules,
		Presence:         presenceService,
		Started:          time.Now().UTC(),
		clientRouter:     httpserver.NewRouter(),
		federationRouter: httpserver.NewRouter(),
	}

	// A restricted room's join rule is only half of it: the room version also demands that the
	// homeserver of the user who authorised the join signed the join event. That is a question
	// about somebody's signing keys, so the room service needs a key ring to answer it — for a
	// neighbour's user, and for this server's own, whose signature it is about to make. This is
	// wired outside the federation block on purpose: a server that does not federate still has
	// restricted rooms, and its own signature is still the one those rooms ask for.
	roomService.SetKeyRing(func(ctx context.Context, rules roomversion.Rules) events.KeyLookup {
		return func(origin, keyID string) (ed25519.PublicKey, error) {
			// This server's own signature is checked against its own keys. The federation key ring
			// holds other homeservers' keys, because that is what federation needs from it, and a
			// server that looked up itself there would find nothing and refuse a join it had made
			// with its own hand.
			if key, err := signingKeys.VerifyLocal(origin, keyID); err == nil {
				return key, nil
			}
			if a.Keyring == nil {
				return nil, fmt.Errorf("app: no key ring to check %q's signature with", origin)
			}
			return a.Keyring.LookupForRoom(ctx, rules)(origin, keyID)
		}
	})

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
		Cfg:         cfg,
		Log:         log,
		Users:       userService,
		AccountData: users.NewAccountDataService(db.AccountData),
		Tokens:      tokens,
		UIA:         uia,
		Keys:        signingKeys,
		Rooms:       roomService,
		PushRules:   pushRules,
		Presence:    presenceService,
		E2EKeys:     deviceKeys,
		Sync:        sync.NewService(db, log),
		DB:          db,
		Version:     Version,
		Started:     a.Started,
	}, requireAuth)
	media.Register(a.clientRouter, &media.Deps{Cfg: cfg}, requireAuth)
	// Method negotiation must be consistent across listeners: a federation path
	// probed on the client port answers 405, not 404.
	defer a.clientRouter.MirrorFrom(a.federationRouter)
	federationDeps := &federationapi.Deps{
		Cfg:         cfg,
		Log:         log,
		Keys:        signingKeys,
		Version:     Version,
		E2EKeys:     deviceKeys,
		DeviceLists: deviceKeys,
		Invites:     roomService,
		Gaps:        roomService,
	}
	if cfg.Federation.Enabled {
		policy, err := federation.NewIPpolicy(cfg.Federation.IPAllow, cfg.Federation.IPDeny)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		if len(cfg.Federation.IPDeny) == 0 && len(cfg.Federation.IPAllow) == 0 {
			policy, err = federation.DefaultIPpolicy()
			if err != nil {
				_ = db.Close()
				return nil, err
			}
		}
		timeout := cfg.Federation.Timeout.Get()
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		// One client for the whole federation stack: discovery, key fetching and
		// transactions all go through the same dialer, so the timeouts and connection
		// reuse are the same for all of them.
		dialer := &http.Client{Timeout: timeout, Transport: opts.FederationTransport}
		var resolver federation.Resolver = federation.NewDiscovery(dialer, policy)
		if opts.FederationResolver != nil {
			resolver = opts.FederationResolver
		} else if len(opts.FederationTargets) > 0 {
			resolver = &federation.StaticResolver{Targets: opts.FederationTargets, Fallback: resolver}
		}
		a.Keyring = federation.NewKeyring(db.Federation, resolver, dialer, cfg.ServerName,
			cfg.Federation.NotaryServers, log)
		client := federation.NewClient(resolver, dialer, cfg.ServerName, signingKeys, log)
		// A key query for a user who lives elsewhere is answered by asking that user's
		// homeserver, because it holds the documents their devices uploaded. Without this the
		// query for a remote user is simply empty, which a client reads as "no keys".
		deviceKeys.ServeRemotes(cfg.ServerName, client)
		a.Sender = federation.NewSender(db.Federation, db.Events, db.State, client, cfg.ServerName, log)
		// And the other direction: this server's own device list changes go out as ephemeral
		// units through the same queue as its events, so that a client on another server is
		// prompted to ask at all.
		deviceKeys.ServeDeviceEDUs(a.Sender)
		// A room's exclusion of servers is read from room state, so both the endpoints that answer
		// a server and the relay that decides which servers to tell read it from the room service.
		a.Sender.ACLs = roomService
		federationDeps.ACLs = roomService
		if maxAge := cfg.Federation.QueueMaxAge.Get(); maxAge > 0 {
			a.Sender.MaxAge = maxAge
		}
		if attempts := cfg.Federation.QueueMaxAttempts; attempts > 0 {
			a.Sender.MaxAttempts = attempts
		}
		// Every event this server accepts is offered to the other servers in the room.
		// Queuing is not delivery: an unreachable remote cannot make a local send fail.
		roomService.SetRelayer(a.Sender)
		// A user joining from here is a new device list to the rest of the room, and only this
		// server can say so.
		roomService.SetDeviceListAnnouncer(deviceKeys)
		// An invite for a user who lives elsewhere has to be countersigned by that user's own
		// server before the room's other servers will believe it. This server holds the room,
		// so it can build the event; the signature it cannot make for itself.
		roomService.SetInviter(&federation.Inviter{Client: client, Name: cfg.ServerName, Log: log})
		// A local user joining a room held elsewhere is carried by this handshake: ask a
		// server that holds the room for the shape of the join, sign it here, hand it back.
		// The room service decides when it is needed; this is the part that reaches out.
		// A room joined from elsewhere is held only partly: the join cites extremities the
		// handover does not include. This is what asks for them.
		backfiller := &federation.Backfiller{
			Client:    client,
			Keyring:   a.Keyring,
			LocalKeys: signingKeys,
			Name:      cfg.ServerName,
			Store:     roomService,
			Log:       log,
		}
		a.Backfill = backfiller
		federationDeps.Backfill = backfiller
		joiner := &federation.Joiner{
			Client:    client,
			Keyring:   a.Keyring,
			Keys:      signingKeys,
			LocalKeys: signingKeys,
			Name:      cfg.ServerName,
			Ingest:    roomService,
			Announcer: roomService,
			Log:       log,
		}
		joiner.Backfill = backfiller
		a.Joiner = joiner
		roomService.SetRemoteJoin(joiner)
		federationDeps.Keyring = a.Keyring
		federationDeps.Federation = db.Federation
		federationDeps.Rooms = roomService
		federationDeps.Joins = roomService
		federationDeps.DB = db
	}
	federationapi.Register(a.federationRouter, federationDeps)

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

// Serve serves on the bound listeners until ctx is cancelled, and works the federation
// queue in the meantime.
//
// The worker shares the context so that shutting down stops retrying: a goroutine that
// outlives the listeners keeps opening connections to servers that will never answer,
// and a process that has decided to stop does not get to keep talking.
func (a *App) Serve(ctx context.Context) error {
	if a.Sender == nil {
		return a.Server.Serve(ctx)
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return a.Server.Serve(groupCtx) })
	group.Go(func() error { return a.Sender.Run(groupCtx) })
	return group.Wait()
}

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
