package httpserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Config configures the listeners.
type Config struct {
	// ClientAddr is the plain HTTP address for the Client-Server API.
	ClientAddr string
	// FederationAddr is the HTTPS address for the Server-Server and key APIs.
	FederationAddr string

	CertFile string
	KeyFile  string

	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration

	Logger *slog.Logger
}

// Server serves the client API over HTTP and the federation/key API over HTTPS.
type Server struct {
	cfg      Config
	log      *slog.Logger
	srvs     []*http.Server
	lnts     []net.Listener
	resolved []string
	// mu guards lnts: Serve hands each http.Server its listener from goroutines
	// while shutdown may close the same listeners, so both paths take the lock.
	mu              sync.Mutex
	listenersClosed bool
	tlsCert         string
	tlsKey          string
}

// New creates a Server. Handlers must be supplied with SetHandlers before Run.
func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}
	return &Server{cfg: cfg, log: cfg.Logger}
}

// SetHandlers installs the client API and federation API handlers.
func (s *Server) SetHandlers(client, federation http.Handler) {
	s.srvs = nil
	base := func(addr string, h http.Handler) *http.Server {
		return &http.Server{
			Addr:              addr,
			Handler:           h,
			ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
			WriteTimeout:      s.cfg.WriteTimeout,
			IdleTimeout:       s.cfg.IdleTimeout,
			ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		}
	}
	s.srvs = append(s.srvs, base(s.cfg.ClientAddr, client))
	fed := base(s.cfg.FederationAddr, federation)
	fed.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	s.srvs = append(s.srvs, fed)
	s.tlsCert, s.tlsKey = s.cfg.CertFile, s.cfg.KeyFile
}

// Listen binds the listeners and records their resolved addresses, which lets
// tests bind port 0 and discover the real port.
func (s *Server) Listen() error {
	if len(s.srvs) != 2 {
		return errors.New("httpserver: handlers must be installed before Listen")
	}
	fed := s.srvs[1]
	if s.tlsCert == "" || s.tlsKey == "" {
		return errors.New("httpserver: the federation listener requires a certificate and key")
	}
	cert, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey)
	if err != nil {
		return fmt.Errorf("httpserver: cannot load federation certificate: %w", err)
	}
	fed.TLSConfig.Certificates = []tls.Certificate{cert}
	for i, srv := range s.srvs {
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			s.CloseListeners()
			return fmt.Errorf("httpserver: cannot listen on %s: %w", srv.Addr, err)
		}
		if i == 1 {
			ln = tls.NewListener(ln, srv.TLSConfig)
		}
		s.lnts = append(s.lnts, ln)
		s.resolved = append(s.resolved, ln.Addr().String())
	}
	return nil
}

// Run binds the listeners and serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve(ctx)
}

// Serve serves on the already bound listeners until ctx is cancelled, then shuts
// down gracefully within ShutdownTimeout.
func (s *Server) Serve(ctx context.Context) error {
	g, gctx := errgroup.WithContext(ctx)
	for _, srv := range s.srvs {
		g.Go(func() error {
			s.log.Info("listening", "addr", srv.Addr, "tls", srv.TLSConfig != nil)
			if err := srv.Serve(s.nextListener(srv)); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
	}
	<-gctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	var errs []error
	for _, srv := range s.srvs {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// The graceful window elapsed with connections still active. Close them
			// so the process can still exit, and report that the shutdown was forced.
			s.log.Warn("graceful shutdown timed out, closing connections", "addr", srv.Addr, "error", err)
			_ = srv.Close()
			errs = append(errs, fmt.Errorf("httpserver: forced shutdown of %s: %w", srv.Addr, err))
		}
	}
	s.CloseListeners()
	if err := g.Wait(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ClientAddr returns the resolved client listener address, which differs from the
// configured address when port 0 was requested.
func (s *Server) ClientAddr() string { return s.resolvedAddr(0) }

// FederationAddr returns the resolved federation listener address.
func (s *Server) FederationAddr() string { return s.resolvedAddr(1) }

// CloseListeners closes any bound listeners. It is safe to call more than once and
// safe to call while Serve is running.
func (s *Server) CloseListeners() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listenersClosed {
		return
	}
	s.listenersClosed = true
	for _, ln := range s.lnts {
		_ = ln.Close()
	}
}

// resolvedAddr returns the bound address at the given listener index. Listeners
// are always ordered: client first, federation second.
func (s *Server) resolvedAddr(i int) string {
	if i < len(s.resolved) {
		return s.resolved[i]
	}
	if i < len(s.srvs) {
		return s.srvs[i].Addr
	}
	return ""
}

// nextListener pairs a server with its listener by construction order, which is
// stable because srvs and lnts are built in the same order.
func (s *Server) nextListener(srv *http.Server) net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.srvs {
		if l == srv && i < len(s.lnts) {
			return s.lnts[i]
		}
	}
	return nil
}
