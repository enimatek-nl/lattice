package rooms

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/serveracl"
)

// RoomACL reads a room's exclusion of homeservers from its current state.
//
// The second answer says whether the room has one this server can act on. A room with no
// `m.room.server_acl` event excludes nobody, and neither does a room whose ACL event this server
// could not make sense of — the specification's fields are all optional, so a garbled event is not
// a sentence with a meaning, and treating a mangled `allow` list as an absent one would exclude
// every server in the room on a typo. What is refused is the event that would have been applied,
// which is reported where an operator can read it.
func (s *Service) RoomACL(ctx context.Context, roomID string) (serveracl.ACL, bool, error) {
	entry, err := s.state.Get(ctx, roomID, serveracl.Type, "")
	if err != nil {
		if notFound(err) {
			return serveracl.ACL{}, false, nil
		}
		return serveracl.ACL{}, false, fmt.Errorf("rooms: cannot read a room's server ACL: %w", err)
	}
	if entry == nil || entry.EventID == "" {
		return serveracl.ACL{}, false, nil
	}
	stored, err := s.events.ByID(ctx, entry.EventID)
	if err != nil {
		if notFound(err) {
			// The state names an event this server does not hold. There is nothing to apply,
			// and inventing an exclusion would be acting on bytes this server has never seen.
			return serveracl.ACL{}, false, nil
		}
		return serveracl.ACL{}, false, fmt.Errorf("rooms: cannot read the event that carries a room's server ACL: %w", err)
	}
	body, err := events.Parse(stored.JSON)
	if err != nil {
		s.log.Warn("rooms: a room's server ACL event could not be read, so no exclusion is applied",
			slog.String("room_id", roomID), slog.String("event_id", entry.EventID), slog.Any("error", err))
		return serveracl.ACL{}, false, nil
	}
	content, _ := body["content"].(map[string]any)
	acl, understood := serveracl.Parse(content)
	if !understood {
		s.log.Warn("rooms: a room's server ACL is shaped wrongly, so no exclusion is applied",
			slog.String("room_id", roomID), slog.String("event_id", entry.EventID),
			slog.String("sender", stringOr(body["sender"])))
		return serveracl.ACL{}, false, nil
	}
	return acl, true, nil
}

// DeniesServer says whether a room has excluded a homeserver.
//
// The room's decision is applied before its contents are handed over, not only when they are: a
// room that will not deal with a server has to refuse to tell it the room's shape, accept events
// from it, or pass events to it, or the exclusion is a preference the server can ignore.
func (s *Service) DeniesServer(ctx context.Context, roomID, serverName string) (bool, error) {
	if roomID == "" || serverName == "" {
		return false, nil
	}
	acl, present, err := s.RoomACL(ctx, roomID)
	if err != nil || !present {
		return false, err
	}
	return !acl.Allows(serverName), nil
}

// refusedByACL refuses an event because the room it belongs to has excluded the server that sent
// it. The reason is recorded as a rejection, so that this server can say later that it saw the event
// and would not take it, rather than being asked again as if it had never heard of it.
func (s *Service) refusedByACL(ctx context.Context, event map[string]any, roomID, eventID string) error {
	origin, _ := event["origin"].(string)
	if origin == "" {
		// Nothing to check the exclusion against. This is not forgiven — an event that names no
		// origin server cannot be traced to anybody and does not survive the checks that follow —
		// but it is those checks that say so, in the terms that are true of it, rather than this
		// one inventing an answer about a server name it does not have.
		return nil
	}
	denied, err := s.DeniesServer(ctx, roomID, origin)
	if err != nil {
		return fmt.Errorf("rooms: cannot read whether the room excludes the server that sent an event: %w", err)
	}
	if !denied {
		return nil
	}
	return s.refuse(ctx, roomID, eventID,
		fmt.Sprintf("the room %s excludes the server %s by its server ACL", roomID, origin))
}

// ServersAllowedToHear names the servers a room will accept an event from and send one to, out of
// the servers that have a member in the room. A room that excludes a server is not served by
// relaying to it: the specification is explicit that a server must not send a room's events to a
// server the room has excluded.
func (s *Service) ServersAllowedToHear(ctx context.Context, roomID string, servers []string) ([]string, error) {
	acl, present, err := s.RoomACL(ctx, roomID)
	if err != nil {
		return nil, err
	}
	if !present {
		return servers, nil
	}
	out := make([]string, 0, len(servers))
	for _, server := range servers {
		if acl.Allows(server) {
			out = append(out, server)
		}
	}
	return out, nil
}
