package federation

import (
	"context"

	"github.com/enimatek/lattice/internal/federation"
)

// GapFiller catches up on events this server is missing, starting from a named set of them. The
// federation backfiller implements it.
type GapFiller interface {
	FillFrom(ctx context.Context, roomID string, destinations, seeds []string) (federation.FillOutcome, error)
}

// RoomACLs decides whether a room has excluded a homeserver. The room service implements it: a
// room's exclusion of servers is part of the room's state, so the endpoints that answer a room and
// the path that accepts its events have to read the same one entry rather than each keeping a
// notion of who is out.
type RoomACLs interface {
	DeniesServer(ctx context.Context, roomID, serverName string) (bool, error)
}
