// Command lattice runs the Lattice Matrix homeserver.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/enimatek/lattice/internal/app"
	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/logging"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(runHealthcheck(os.Args[2:]))
		case "generate-config":
			os.Exit(runGenerateConfig(os.Args[2:]))
		case "version", "--version":
			fmt.Printf("lattice %s\n", app.Version)
			return
		}
	}
	os.Exit(run(os.Args[1:]))
}

// run starts the homeserver and blocks until the process is asked to stop.
func run(args []string) int {
	fs := flag.NewFlagSet("lattice", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to a JSON configuration file")
	serverName := fs.String("server-name", "", "override the server name")
	clientAddr := fs.String("client-addr", "", "override the client API listen address")
	federationAddr := fs.String("federation-addr", "", "override the federation API listen address")
	logLevel := fs.String("log-level", "", "debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.LoadForRun(*configPath, *serverName, *clientAddr, *federationAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lattice: configuration error: %v\n", err)
		return 1
	}
	if *logLevel != "" {
		cfg.Log.Level = *logLevel
	}

	log := logging.New(cfg.Log.Level, cfg.Log.Format)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	instance, err := app.New(ctx, cfg, log)
	if err != nil {
		log.Error("startup failed", "error", err)
		return 1
	}
	defer func() {
		if err := instance.Close(); err != nil {
			log.Error("shutdown error", "error", err)
		}
	}()

	log.Info("lattice starting",
		"server_name", cfg.ServerName,
		"version", app.Version,
		"client_addr", cfg.HTTP.ClientAddr,
		"federation_addr", cfg.HTTP.FederationAddr,
		"registration_mode", registrationMode(cfg),
	)
	if err := instance.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("server error", "error", err)
		return 1
	}
	log.Info("lattice stopped")
	return 0
}

// registrationMode describes how accounts may be created, for the startup log line.
// The shared secret itself is never logged; only whether one is configured.
func registrationMode(cfg *config.Config) string {
	switch {
	case cfg.Registration.Open:
		return "open"
	case cfg.Registration.SharedSecret != "":
		return "shared-secret"
	default:
		return "closed"
	}
}

// runHealthcheck answers the container healthcheck: it performs one client API
// request and reports whether the server is serving Matrix traffic.
func runHealthcheck(args []string) int {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	addr := fs.String("addr", "", "address to probe (defaults to LATTICE_CLIENT_ADDR or 127.0.0.1:8008)")
	timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	target := *addr
	if target == "" {
		target = os.Getenv("LATTICE_CLIENT_ADDR")
	}
	if target == "" {
		target = "127.0.0.1:8008"
	}
	url := "http://" + trimScheme(target) + "/_matrix/client/versions"

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: %s returned %s\n", url, res.Status)
		return 1
	}
	var body struct {
		Versions []string `json:"versions"`
	}
	dec := json.NewDecoder(res.Body)
	if err := dec.Decode(&body); err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: malformed /versions response: %v\n", err)
		return 1
	}
	if len(body.Versions) == 0 {
		fmt.Fprintln(os.Stderr, "healthcheck: /versions listed no specification versions")
		return 1
	}
	return 0
}

// runGenerateConfig writes a documented example configuration.
func runGenerateConfig(args []string) int {
	fs := flag.NewFlagSet("generate-config", flag.ContinueOnError)
	out := fs.String("out", "", "file to write (default: standard output)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg := config.Default()
	cfg.ServerName = "example.com"
	cfg.Registration.Open = false
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate-config: %v\n", err)
		return 1
	}
	if *out == "" {
		fmt.Println(string(data))
		return 0
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o640); err != nil {
		fmt.Fprintf(os.Stderr, "generate-config: %v\n", err)
		return 1
	}
	return 0
}

// trimScheme accepts "host:port" or a full URL and returns an address.
func trimScheme(s string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if len(s) > len(prefix) && s[:len(prefix)] == prefix {
			return s[len(prefix):]
		}
	}
	return s
}
