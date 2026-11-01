package unit

import (
	"math"
	"strconv"
	"testing"

	"github.com/enimatek-nl/lattice/internal/auth"
)

// TestStreamTokens pins the sync and pagination tokens. They are opaque to
// clients but every paginated endpoint parses them, and a token that a client
// invents must be rejected rather than read as position zero: a corrupted token
// read as "the beginning of the stream" replays a whole room.
func TestStreamTokens(t *testing.T) {
	for _, pos := range []uint64{0, 1, 42, math.MaxUint64} {
		token := auth.StreamPos(pos)
		got, err := auth.ParseStreamPos(token)
		if err != nil {
			t.Errorf("ParseStreamPos(%q) failed: %v", token, err)
			continue
		}
		if got != pos {
			t.Errorf("StreamPos(%d) = %q, which reads back as %d", pos, token, got)
		}
	}
	if got := auth.StreamPos(17); got != "s17" {
		t.Errorf("StreamPos(17) = %q, want s17", got)
	}
}

// TestParseStreamTokenShape pins what the parser accepts and refuses.
func TestParseStreamTokenShape(t *testing.T) {
	accepted := []struct {
		token   string
		pos     uint64
		comment string
	}{
		{token: "", pos: 0, comment: "no token means the start of the stream"},
		{token: "s0", pos: 0},
		{token: "t17", pos: 17,
			comment: "presence and typing tokens carry a t prefix and share the numbering"},
		{token: "17", pos: 17, comment: "a bare number is accepted from older clients"},
	}
	for _, tt := range accepted {
		got, err := auth.ParseStreamPos(tt.token)
		if err != nil {
			t.Errorf("ParseStreamPos(%q) failed: %v", tt.token, err)
			continue
		}
		if got != tt.pos {
			t.Errorf("ParseStreamPos(%q) = %d, want %d", tt.token, got, tt.pos)
		}
	}
	refused := []string{"s", "abc", "s12x", "-4", "s-4", "s 4", "s+4", "99999999999999999999999"}
	for _, token := range refused {
		if got, err := auth.ParseStreamPos(token); err == nil {
			t.Errorf("ParseStreamPos(%q) = %d, want an error", token, got)
		}
	}
}

// TestStreamTokenOrdering pins the property every paginated endpoint leans on:
// a token that is larger describes a later position, so comparing tokens is the
// same as comparing the events they point at.
func TestStreamTokenOrdering(t *testing.T) {
	var tokens []string
	for i := uint64(1); i <= 20; i++ {
		tokens = append(tokens, auth.StreamPos(i))
	}
	for i := 1; i < len(tokens); i++ {
		previous, err := auth.ParseStreamPos(tokens[i-1])
		if err != nil {
			t.Fatalf("ParseStreamPos(%q): %v", tokens[i-1], err)
		}
		current, err := auth.ParseStreamPos(tokens[i])
		if err != nil {
			t.Fatalf("ParseStreamPos(%q): %v", tokens[i], err)
		}
		if previous >= current {
			t.Errorf("%s and %s do not move forward", tokens[i-1], tokens[i])
		}
	}
	// The textual order is not the numeric order, which is why the tokens are
	// parsed before being compared: a test that pins this stops anyone from
	// sorting tokens as strings.
	if auth.StreamPos(100) >= auth.StreamPos(99) {
		t.Errorf("s100 no longer sorts before s99 as text: the parser, not the string, must decide order")
	}
	if _, err := auth.ParseStreamPos(strconv.FormatUint(math.MaxUint64, 10)); err != nil {
		t.Errorf("the largest position must round-trip: %v", err)
	}
}
