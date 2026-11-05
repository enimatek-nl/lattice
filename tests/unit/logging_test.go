package unit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/enimatek/lattice/internal/logging"
)

// TestLoggingRedactsSecrets proves the logger never writes access tokens,
// passwords or key material verbatim (AGENTS.md §9).
func TestLoggingRedactsSecrets(t *testing.T) {
	t.Parallel()
	buffer := &bytes.Buffer{}
	base := slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug})
	log := logging.NewFor(base)

	log.Info("request finished",
		"access_token", "abcdefABCDEFGH1234567890abcdef",
		"password", "correct horse battery staple",
		"shared_secret", "complement",
		"token_hash", "deadbeefdeadbeef",
		"private_key", "MDEyMzQ1Njc4OWFiY2RlZg",
		"refresh_token", "zzzzzzzzzzzzzzzzzzzzzz",
		"user_id", "@alice:hs1",
	)

	out := buffer.String()
	for _, secret := range []string{
		"abcdefABCDEFGH1234567890abcdef",
		"correct horse battery staple",
		"complement",
		"deadbeefdeadbeef",
		"MDEyMzQ1Njc4OWFiY2RlZg",
		"zzzzzzzzzzzzzzzzzzzzzz",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("log output leaked %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "@alice:hs1") {
		t.Errorf("non-secret attributes should still be logged: %s", out)
	}

	// The redacted value must remain valid JSON.
	var record map[string]any
	if err := json.Unmarshal([]byte(out), &record); err != nil {
		t.Fatalf("log output is not valid JSON: %v (%s)", err, out)
	}
}

func TestLoggerLevelFiltering(t *testing.T) {
	t.Parallel()
	buffer := &bytes.Buffer{}
	base := slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelWarn})
	log := logging.NewFor(base)
	log.Info("should be dropped")
	if buffer.Len() != 0 {
		t.Errorf("info record written at warn level: %s", buffer.String())
	}
	log.Error("kept")
	if !strings.Contains(buffer.String(), "kept") {
		t.Errorf("error record missing: %s", buffer.String())
	}
}

func TestRedactingHandlerImplementsInterface(t *testing.T) {
	t.Parallel()
	buffer := &bytes.Buffer{}
	handler := logging.NewRedactingHandler(slog.NewTextHandler(buffer, nil))
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "message", 0)
	record.AddAttrs(slog.String("password", "secret123"))
	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle failed: %v", err)
	}
	if strings.Contains(buffer.String(), "secret123") {
		t.Errorf("text handler leaked the password: %s", buffer.String())
	}
}
