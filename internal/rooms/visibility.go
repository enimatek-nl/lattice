package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

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
func (s *Service) VisibilityToPeer(ctx context.Context, event *persist.Event, serverName string) (bool, error) {
	if event == nil || serverName == "" {
		return false, nil
	}
	visibility, snapshotKnown, err := s.historyVisibilityAt(ctx, event)
	if err != nil {
		return false, err
	}
	switch visibility {
	case "world_readable":
		return true, nil
	case "", "shared":
		// `shared` reads backwards: whoever is a member now may read what came before.
		return s.serverHasJoinedUser(ctx, event.RoomID, serverName)
	case "joined", "invited":
		if !snapshotKnown {
			return false, nil
		}
		joined, invited, err := s.serverMembershipsAt(ctx, event.EventID, serverName)
		if err != nil {
			return false, err
		}
		return joined || (invited && visibility == "invited"), nil
	default:
		// A value this server does not know is answered as the protocol answers an absent
		// entry — `shared` — rather than as whatever the string seems to suggest, and that
		// reading is deliberately the least permissive of the two it could have been for a
		// peer that is not in the room.
		return s.serverHasJoinedUser(ctx, event.RoomID, serverName)
	}
}

// historyVisibilityAt reports the room's history visibility as of the moment the given event was
// sent, and whether this server has a state snapshot for that event at all. The second answer is
// part of the result because the first is meaningless without it: a room whose visibility event is
// absent from the snapshot may genuinely have had no visibility event then, which is `shared`, or
// this server may simply not have recorded what the room looked like.
func (s *Service) historyVisibilityAt(ctx context.Context, event *persist.Event) (string, bool, error) {
	eventID, err := s.groups.StateEntryAt(ctx, event.EventID, "m.room.history_visibility", "")
	if err == nil {
		row, err := s.events.ByID(ctx, eventID)
		if err != nil {
			if notFound(err) {
				// The snapshot names a visibility event this server does not hold. This
				// is not "the room had no visibility setting"; say so by reporting that the
				// moment is unknown rather than by falling back to today's value.
				return "", false, nil
			}
			return "", false, err
		}
		return contentString(row, "history_visibility"), true, nil
	}
	if !errors.Is(err, persist.ErrNotFound) {
		return "", false, err
	}
	if _, err := s.groups.GroupOf(ctx, event.EventID); err != nil {
		// No snapshot for this event: the moment is unknown, whatever the room says today.
		return "", false, nil
	}
	// A snapshot that holds no visibility event: the room had no such setting at that moment,
	// which the protocol reads as `shared`.
	return "shared", true, nil
}

// serverHasJoinedUser reports whether any user on this server's side of the federation is a joined
// member of the room now.
func (s *Service) serverHasJoinedUser(ctx context.Context, roomID, serverName string) (bool, error) {
	entries, err := s.state.ByType(ctx, roomID, "m.room.member")
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if userBelongsTo(entry.StateKey, serverName) {
			ids = append(ids, entry.EventID)
		}
	}
	if len(ids) == 0 {
		return false, nil
	}
	rows, err := s.events.ByIDs(ctx, ids)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if contentString(row, "membership") == "join" {
			return true, nil
		}
	}
	return false, nil
}

// serverMembershipsAt reports whether the peer had a joined or invited user in the room at the
// moment the named event was sent, read out of the state snapshot that event was sent against —
// the same snapshot the room's own authorisation rules judged it by.
func (s *Service) serverMembershipsAt(ctx context.Context, eventID, serverName string) (joined, invited bool, err error) {
	groupID, err := s.groups.GroupOf(ctx, eventID)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			return false, false, nil
		}
		return false, false, err
	}
	entries, err := s.groups.Entries(ctx, groupID)
	if err != nil {
		return false, false, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "m.room.member" || !userBelongsTo(entry.StateKey, serverName) {
			continue
		}
		ids = append(ids, entry.EventID)
	}
	if len(ids) == 0 {
		return false, false, nil
	}
	rows, err := s.events.ByIDs(ctx, ids)
	if err != nil {
		return false, false, err
	}
	for _, row := range rows {
		switch contentString(row, "membership") {
		case "join":
			joined = true
		case "invite":
			invited = true
		}
	}
	return joined, invited, nil
}

// contentString reads one string out of an event's content, from the form of the event this server
// serves. A redaction does not remove the keys that identify what an event is about — the
// membership of a member event, the visibility of a visibility event — so both forms are read the
// same way.
func contentString(row *persist.Event, key string) string {
	raw := row.JSON
	if row.Redacted && len(row.RedactedJSON) > 0 {
		raw = row.RedactedJSON
	}
	var event struct {
		Content map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return ""
	}
	value, ok := event.Content[key]
	if !ok {
		return ""
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		return ""
	}
	return out
}

// userBelongsTo asks whether a Matrix user ID names a user on the given homeserver.
func userBelongsTo(userID, serverName string) bool {
	at := strings.LastIndex(userID, ":")
	return at >= 0 && userID[at+1:] == serverName
}
