package rooms

import (
	"context"

	"github.com/enimatek/lattice/internal/persist"
)

// VisibilityToPeer decides whether an event may be handed to another homeserver with its content
// intact, or only in the redacted form.
//
// The distinction the room itself draws is between an event that was destroyed and one that a
// particular reader was never entitled to read. The first is a fact about the room and is stored;
// the second is a fact about the reader and is worked out at the moment of serving, which is what
// this does for a peer. A server that hands over history its own rules say the peer's users could
// not see is not protected by the peer applying the same rules later: the bytes are already out.
//
// The rule is the room's `m.room.history_visibility` read at the moment of the event, because a
// room's visibility is itself state that changes:
//
//   - `world_readable`: anybody may read it, joined or not.
//   - `shared` (and an absent entry, which the protocol reads as `shared`): a member may read the
//     whole of the room's history, including the part written before they arrived. Entitlement is
//     therefore about the peer now, not about the moment the event was written — and it is why a
//     server that joined late is given the older events intact.
//   - `joined`: only what was written while that peer had a joined user here.
//   - `invited`: the same, counting an invitation as enough.
//
// Where the answer depends on the moment and this server has no state snapshot for that event, the
// answer is no: the event is handed over redacted. Not knowing who was here when this was written
// is not a licence to assume the asker was, and the difference between the two is the difference
// between a gap in the record and a leak.
// VisibilityToPeer decides whether an event may be handed to another homeserver with its content
// intact, or only in the redacted form.
//
// The distinction the room draws is between an event that was destroyed and one that this reader
// was never entitled to read. The first is a fact about the room and is stored; the second is a
// fact about the reader, worked out at the moment of serving. A server that hands over history
// its own rules say the peer's users could not see is not protected by the peer applying the same
// rules later: the bytes are already out.
//
// The rule itself lives in the historyvis package, because the same question is asked by a client reading
// `/event` or a sync and by this server's own outbound fan-out. It used to live here, where only
// the room's doors could reach it, and a second door needing it would have had to write its own
// version.
func (s *Service) VisibilityToPeer(ctx context.Context, event *persist.Event, serverName string) (bool, error) {
	return s.history.MayReadAsServer(ctx, event, serverName)
}
