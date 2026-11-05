// Package stream names positions in this server's event stream.
//
// Every position is a row of `events.stream_ordering`: one counter across all rooms,
// assigned when an event is persisted. A token is that number in a form a client can
// carry around and cannot meaningfully invent.
//
// Both `/sync` and `/messages` speak these tokens, and they must mean the same thing in
// each: a `prev_batch` from a sync timeline is handed to `/messages` as `from`, and a
// pagination token is a starting point for a later sync. Keeping the spelling in one
// place is what makes those swaps work.
package stream

import (
	"strconv"
	"strings"

	"github.com/enimatek/lattice/internal/matrixerr"
)

// Position is a point in the event stream. The zero value is "before anything was
// persisted", which is a real position and not an error.
type Position int64

// String renders the token a client sees. The `s` prefix marks it as ours, so a bare
// number typed into a request is not mistaken for a valid position.
func (p Position) String() string { return "s" + strconv.FormatInt(int64(p), 10) }

// Parse reads a token issued by this server.
//
// A token this server did not issue is refused rather than treated as the beginning of
// the stream: a wrong-but-accepted token would hand back a history the client believes
// is complete.
func Parse(raw string) (Position, error) {
	if !strings.HasPrefix(raw, "s") {
		return 0, matrixerr.New(matrixerr.CodeInvalidToken,
			"This server does not recognise that pagination token.")
	}
	value, err := strconv.ParseInt(raw[1:], 10, 64)
	if err != nil || value < 0 {
		return 0, matrixerr.New(matrixerr.CodeInvalidToken,
			"This server does not recognise that pagination token.")
	}
	return Position(value), nil
}
