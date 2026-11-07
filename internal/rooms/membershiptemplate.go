package rooms

import (
	"context"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/roomversion"
)

// MembershipTemplate is the unsigned `m.room.member` event a resident server hands to the
// server that speaks for a user who wants to join, leave or knock on a room this server holds.
// It carries what that server cannot invent from its own copy of the room: where this room's
// history currently ends, which state authorises the change here, and how deep the event sits.
//
// The three handshakes are the same shape, and the differences are the room's business rather
// than the endpoint's: a join in a restricted room needs a resident voice named in the event
// before the event is named (so the authoriser is chosen here), and which of the room's rules
// apply is decided by the authorisation check this function runs before handing anything over.
//
// Leaving is worth one sentence of its own: a member of a room that lives elsewhere cannot
// write their own departure into that room's history by themselves. They ask a resident server
// for a template so the event's `prev_events` and `auth_events` are drawn from that server's
// copy of the history, which is the only copy the room will accept as the event's place in it.
//
// The template comes back unsigned on purpose. An event ID and hashes from the resident server
// would be a verdict on an event the asking server has not agreed to yet — a membership change
// is signed by the server that speaks for the user it is about.
func (s *Service) MembershipTemplate(ctx context.Context, roomID, userID, membership string) (map[string]any, string, error) {
	switch membership {
	case eventauth.MembershipJoin, eventauth.MembershipLeave, eventauth.MembershipKnock:
	default:
		// A membership this handshake does not speak is a programming error at the call
		// site, not something a peer can ask for: invites, bans and kicks travel over
		// `/send` and `/invite`, not a template.
		return nil, "", fmt.Errorf("rooms: no template is written for membership %q", membership)
	}
	if err := matrixid.UserValid(userID); err != nil {
		return nil, "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", userID)
	}
	if err := matrixid.RoomIDValid(roomID); err != nil {
		return nil, "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", roomID)
	}
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return nil, "", matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
		}
		return nil, "", fmt.Errorf("rooms: cannot load the room for a membership template: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return nil, "", matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot authorise a %s in.",
			roomID, room.RoomVersion, membership).WithExtra(map[string]any{"room_version": room.RoomVersion})
	}

	snapshot, err := s.loadState(ctx, roomID)
	if err != nil {
		return nil, "", err
	}
	prev, err := s.prevEvents(ctx, roomID)
	if err != nil {
		return nil, "", err
	}
	depth, err := s.depth(ctx, prev)
	if err != nil {
		return nil, "", err
	}

	event := map[string]any{
		"type":             eventauth.TypeMember,
		"room_id":          roomID,
		"sender":           userID,
		"state_key":        userID,
		"content":          map[string]any{"membership": membership},
		"prev_events":      toAnySlice(prev),
		"auth_events":      []any{},
		"depth":            depth,
		"origin":           s.name,
		"origin_server_ts": s.now().UnixMilli(),
	}
	// A restricted room needs a resident voice on the join event before the event is named and
	// signed: rule 4.2 of the room version requires the event to be signed by the homeserver of
	// the user the join names as its authoriser, and the authoriser has to be in the event's
	// auth chain for a receiving server to be able to check it without replaying the room.
	// Deciding this after the event ID is computed would rename the event in front of everyone.
	authoriser := ""
	if membership == eventauth.MembershipJoin {
		var err error
		authoriser, err = s.RestrictedJoin(ctx, roomID, userID)
		if err != nil {
			return nil, "", err
		}
	}
	if authoriser != "" {
		content, _ := event["content"].(map[string]any)
		content[eventauth.JACSKey] = authoriser
	}
	event["auth_events"] = toAnySlice(eventauth.SelectEventIDs(rules, event, snapshot))
	if err := s.nameAndReference(event, ctx, rules, nil); err != nil {
		return nil, "", err
	}

	// The template is not an event yet, so rule 4.2's signature is not something it can carry:
	// the server that speaks for the joining user seals it, and this server adds its own at
	// /send_join, where the signature is checked for real before the join is stored.
	templateOpts := s.authOpts(ctx, rules)
	templateOpts.AllowUnsignedTemplate = true
	if err := eventauth.Check(rules, event, snapshot, templateOpts); err != nil {
		failure, reason := "the room's own rules", ""
		if detail, ok := eventauth.AsFailure(err); ok {
			failure = detail.Rule
		}
		reason = fmt.Sprintf("%s does not let %s %s this room.", failure, userID, verbOf(membership))
		s.log.Debug("rooms: a membership template was refused",
			"room_id", roomID, "user_id", userID, "membership", membership, "rule", failure, "reason", reason)
		return nil, "", matrixerr.New(matrixerr.CodeForbidden, "You are not allowed to "+verbOf(membership)+" this room.").
			WithExtra(map[string]any{"rule": failure, "reason": reason})
	}
	return event, room.RoomVersion, nil
}

// verbOf words a refusal the way a client reads it: the room did not let the user do the thing
// they asked for, and the sentence should name that thing rather than say "membership change".
func verbOf(membership string) string {
	switch membership {
	case eventauth.MembershipJoin:
		return "join"
	case eventauth.MembershipKnock:
		return "knock on"
	default:
		return "leave"
	}
}
