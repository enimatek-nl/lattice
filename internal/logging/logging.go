// Package logging configures structured logging and protects secret values from
// reaching log output (AGENTS.md §9: never log access tokens, passwords or keys).
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// sensitiveKeys lists attribute keys whose values must never be logged verbatim.
var sensitiveKeys = map[string]struct{}{
	"access_token":    {},
	"refresh_token":   {},
	"token":           {},
	"password":        {},
	"mac":             {},
	"nonce":           {},
	"authorization":   {},
	"private_key":     {},
	"signing_key":     {},
	"seed":            {},
	"token_hash":      {},
	"password_hash":   {},
	"shared_secret":   {},
	"client_secret":   {},
	"x_matrix_mac":    {},
	"one_time_key":    {},
	"fingerprint_key": {},
}

// New returns a logger configured by level and format ("json" or "text").
func New(level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: parseLevel(level),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == "msg" {
				return a
			}
			if isSensitive(a.Key) {
				a.Value = slog.StringValue(redactedValue(a.Value))
			}
			return a
		},
	}
	if format == "text" {
		return slog.New(NewRedactingHandler(slog.NewTextHandler(stdout(), opts)))
	}
	return slog.New(NewRedactingHandler(slog.NewJSONHandler(stdout(), opts)))
}

// NewNop returns a logger that discards everything; useful in tests.
func NewNop() *slog.Logger { return slog.New(DiscardHandler{}) }

// NewFor returns a logger writing to the given handler, with redaction applied.
func NewFor(h slog.Handler) *slog.Logger { return slog.New(NewRedactingHandler(h)) }

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// RedactingHandler wraps a slog.Handler and replaces sensitive attribute values.
type RedactingHandler struct {
	inner slog.Handler
}

// NewRedactingHandler wraps inner with secret redaction.
func NewRedactingHandler(inner slog.Handler) slog.Handler {
	return &RedactingHandler{inner: inner}
}

// Enabled implements slog.Handler.
func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler: sensitive attribute values are replaced.
func (h *RedactingHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.NumAttrs() == 0 {
		return h.inner.Handle(ctx, r)
	}
	clone := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if isSensitive(a.Key) {
			a.Value = slog.StringValue(redactedValue(a.Value))
		}
		clone.AddAttrs(a)
		return true
	})
	return h.inner.Handle(ctx, clone)
}

// WithAttrs implements slog.Handler.
func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup implements slog.Handler.
func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}

// DiscardHandler drops all records.
type DiscardHandler struct{}

// Enabled implements slog.Handler.
func (DiscardHandler) Enabled(context.Context, slog.Level) bool { return false }

// Handle implements slog.Handler.
func (DiscardHandler) Handle(context.Context, slog.Record) error { return nil }

// WithAttrs implements slog.Handler.
func (d DiscardHandler) WithAttrs([]slog.Attr) slog.Handler { return d }

// WithGroup implements slog.Handler.
func (d DiscardHandler) WithGroup(string) slog.Handler { return d }

func isSensitive(key string) bool {
	if _, ok := sensitiveKeys[strings.ToLower(key)]; ok {
		return true
	}
	lower := strings.ToLower(key)
	for _, part := range []string{"token", "password", "secret", "private_key"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func redactedValue(v slog.Value) string {
	s := v.String()
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

// stdout returns the writer log records go to. Tests install their own handlers
// instead of swapping this.
func stdout() io.Writer { return os.Stdout }
