package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
)

// RemoteInviter carries an invite to the homeserver of the user being invited, so that the
// event can be signed there as well as here. The federation inviter implements it.
//
// The countersignature is the point of the round trip. An invite is a claim about a user who
// lives elsewhere, and this server cannot make that claim on its own: the specification has the
// invited server sign the event precisely so that the room's other servers see the user's own
// server agreeing that this user was invited.
type RemoteInviter interface {
	SendInvite(ctx context.Context, destination, roomVersion string, event map[string]any) (map[string]any, error)
}

// SetInviter connects the transport for invites to users on other homeservers. Without it a
// remote invite is refused rather than half-done.
func (s *Service) SetInviter(inviter RemoteInviter) { s.inviter = inviter }

// inviteRoomStateTypes names the state that travels with an invite so that the invited user can
// tell which room they have been asked into before they are in it. The create event is required
// by the specification; the rest is what a client shows in a room list.
// m.room.power_levels and the member events are in the list because the invited server has to be
// able to *authorise* the invite, not merely describe the room: its own checks read the power
// levels and the inviter's membership out of the events the invite cites.
var inviteRoomStateTypes = []string{
	eventauth.TypeCreate, "m.room.join_rules", "m.room.name", "m.room.avatar",
	"m.room.canonical_alias", "m.room.encryption", "m.room.power_levels",
}

// inviteRoomStateKeys are member events by state key: the inviter's own membership (which is what
// says they may invite) and the invited user's previous membership (which is what says whether this
// invite lifts a ban).
const inviteMemberLimit = 64

// inviteRemote carries an invite for a user on another homeserver.
//
// The room is held here, so this server is the one that can build the event: it knows where the
// history ends, which state authorises an invite, and how deep the event sits. The invited
// server adds its signature to that event and hands it back; only then is it stored, because
// what gets stored is the event both servers signed, and every other server in the room will be
// given that version.
func (s *Service) inviteRemote(ctx context.Context, change MembershipChange) (string, error) {
	if s.inviter == nil {
		// The same answer this server gave before invites over federation existed at all: a
		// membership change for a user who lives elsewhere cannot be carried out here. The
		// status matters — a permission refusal would send an administrator looking at the
		// room's power levels when what is missing is a transport.
		return "", ErrRemoteTarget
	}
	event, room, err := s.buildInvite(ctx, change)
	if err != nil {
		return "", err
	}
	destination, err := matrixid.UserDomain(change.Target)
	if err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", change.Target)
	}
	countersigned, err := s.inviter.SendInvite(ctx, destination, room.RoomVersion, event)
	if err != nil {
		// Nothing is stored. An invite only this server signed is an invite the rest of the
		// room has no reason to believe, and storing it would tell the inviter's client that
		// the user was invited when their own homeserver has not agreed to it.
		//
		// The reason goes to the log; the client is told that the other server would not have
		// it, with a gateway status, because that is what this is: this server did its part and
		// somebody else's answer was the problem.
		s.log.Warn("rooms: the invited user's server would not countersign the invite",
			"room_id", change.RoomID, "target", change.Target, "destination", destination, "error", err)
		return "", matrixerr.Newf(matrixerr.CodeUnknown,
			"%s, the homeserver of the user being invited, did not accept the invite.", destination).
			WithStatus(http.StatusBadGateway)
	}
	// The invited server is entitled to change nothing but the signatures. An event that
	// comes back with a different shape is not the invite this server asked to be countersigned.
	if problem := inviteShapeProblem(countersigned, change); problem != "" {
		return "", matrixerr.Newf(matrixerr.CodeBadRequest,
			"%s returned an invite that is not the one it was given to sign: %s", destination, problem)
	}
	eventID, err := s.storeInvite(ctx, countersigned, room.RoomVersion)
	if err != nil {
		return "", err
	}
	s.applySideEffects(ctx, eventID)
	return eventID, nil
}

// buildInvite makes the invite event, signs it here, and returns it with the room it belongs to.
// Nothing is stored: the event has to travel to the invited user's server first.
func (s *Service) buildInvite(ctx context.Context, change MembershipChange) (map[string]any, *persist.Room, error) {
	room, err := s.rooms.ByRoomID(ctx, change.RoomID)
	if err != nil {
		if notFound(err) {
			return nil, nil, matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", change.RoomID)
		}
		return nil, nil, fmt.Errorf("rooms: cannot load the room for an invite: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return nil, nil, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot authorise an invite in.",
			change.RoomID, room.RoomVersion).WithExtra(map[string]any{"room_version": room.RoomVersion})
	}
	snapshot, err := s.loadState(ctx, change.RoomID)
	if err != nil {
		return nil, nil, err
	}
	prev, err := s.prevEvents(ctx, change.RoomID)
	if err != nil {
		return nil, nil, err
	}
	depth, err := s.depth(ctx, prev)
	if err != nil {
		return nil, nil, err
	}
	content := map[string]any{"membership": eventauth.MembershipInvite}
	if change.IsDirect {
		// The flag travels with the invitation because the countersigning server has no other
		// way to learn it, and the invited user's client reads it from the invitation.
		content["is_direct"] = true
	}
	if change.Reason != "" {
		content["reason"] = change.Reason
	}
	event := map[string]any{
		"type":             eventauth.TypeMember,
		"room_id":          change.RoomID,
		"sender":           change.Sender,
		"state_key":        change.Target,
		"content":          content,
		"prev_events":      toAnySlice(prev),
		"auth_events":      []any{},
		"depth":            depth,
		"origin":           s.name,
		"origin_server_ts": s.now().UnixMilli(),
	}
	event["auth_events"] = toAnySlice(eventauth.SelectEventIDs(rules, event, snapshot))
	if err := s.nameAndReference(event, ctx, rules, nil); err != nil {
		return nil, nil, err
	}
	// The room's own rules decide whether this invite may be written at all, here as
	// everywhere else: an invite into a room whose power levels say the sender cannot invite
	// is refused before a signature is asked for.
	if err := eventauth.Check(rules, event, snapshot, s.authOpts(ctx, rules)); err != nil {
		return nil, nil, s.refused(err, SendRequest{RoomID: change.RoomID, Sender: change.Sender})
	}
	state, err := s.inviteRoomState(ctx, change.RoomID)
	if err != nil {
		return nil, nil, err
	}
	if len(state) > 0 {
		// `unsigned` is not covered by any signature, so the invited server can read the
		// room's shape without this event having to be re-hashed on its way through.
		event["unsigned"] = map[string]any{"invite_room_state": state}
	}
	if err := events.Seal(s.name, rules, event, s.signer); err != nil {
		return nil, nil, fmt.Errorf("rooms: cannot sign the invite: %w", err)
	}
	eventID, err := events.EventID(rules, event)
	if err != nil {
		return nil, nil, fmt.Errorf("rooms: cannot name the invite event: %w", err)
	}
	event["event_id"] = eventID
	return event, room, nil
}

// inviteRoomState is the small piece of a room that travels with an invite: enough for a client
// to show a room it has only been asked into. The create event has to be in it, because the room
// version and the creator are not otherwise knowable to a server that does not hold the room.
func (s *Service) inviteRoomState(ctx context.Context, roomID string) ([]json.RawMessage, error) {
	pdus, err := s.StatePDUs(ctx, roomID)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(inviteRoomStateTypes))
	for _, pdu := range pdus {
		var probe struct {
			Type     string  `json:"type"`
			StateKey *string `json:"state_key"`
		}
		if err := json.Unmarshal(pdu, &probe); err != nil || probe.StateKey == nil {
			continue
		}
		if probe.Type == eventauth.TypeMember {
			// The inviter's membership is what authorises the invite, and the invited user's
			// previous membership is what an invite may be lifting (a ban, for instance).
			if len(out) < inviteMemberLimit {
				out = append(out, pdu)
			}
			continue
		}
		for _, wanted := range inviteRoomStateTypes {
			if probe.Type == wanted {
				out = append(out, pdu)
				break
			}
		}
	}
	return out, nil
}

// AcceptRemoteInvite stores an invite a peer made for one of this server's users, after adding
// this server's signature to it.
//
// The peer has already been checked by the endpoint: the event is an invite, it names a user who
// lives here, and the sender lives on the server that sent it. What is decided here is whether
// this server will have the room at all — an invite to a room this server does not hold makes it
// a holder of one event in that room, and nothing else — and whether the room's rules allow the
// invite.
func (s *Service) AcceptRemoteInvite(ctx context.Context, event map[string]any, roomVersion string,
	verify func(map[string]any, roomversion.Rules) error) (map[string]any, error) {
	rules, ok := roomversion.Get(roomVersion)
	if !ok {
		return nil, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"This server cannot handle room version %s.", roomVersion).WithExtra(map[string]any{"room_version": roomVersion})
	}
	roomID, _ := event["room_id"].(string)
	freshRoom := false
	if _, err := s.rooms.ByRoomID(ctx, roomID); err != nil {
		if !notFound(err) {
			return nil, fmt.Errorf("rooms: cannot look for the room an invite names: %w", err)
		}
		// The room is new to this server. Its version comes from the invite request, because
		// an invite is the first thing this server will ever hold for that room.
		if err := s.rooms.Create(ctx, &persist.Room{
			RoomID:      roomID,
			RoomVersion: rules.ID,
			Creator:     creatorOfRoom(event),
			CreatedTS:   int64Of(event["origin_server_ts"]),
		}); err != nil && !errors.Is(err, persist.ErrConflict) {
			return nil, fmt.Errorf("rooms: cannot record the room an invite names: %w", err)
		}
		freshRoom = true
	}
	if verify != nil {
		// The signature being checked is the inviting server's, and it has to be checked
		// before this server adds its own: after that, a verifier cannot tell whose word is
		// whose, and an event this server countersigned is an event this server vouched for.
		if err := verify(event, rules); err != nil {
			return nil, err
		}
	}
	stateEvents, snapshot, err := s.inviteState(ctx, event, rules, verify)
	if err != nil {
		return nil, err
	}
	if err := s.checkInviteAgainstRules(ctx, event, rules, snapshot); err != nil {
		return nil, err
	}
	if freshRoom {
		// The state that came with the invite is kept for a room this server has never held,
		// because the invited user is expected to do something with the invitation, and what
		// they do next — join, reject — has to be authorisable. An event kept here is a signed
		// event from the room's own servers, checked above; it is not the room's whole history,
		// and nothing pretends it is.
		if err := s.storeInviteState(ctx, stateEvents, rules); err != nil {
			return nil, err
		}
	}
	if err := events.Seal(s.name, rules, event, s.signer); err != nil {
		return nil, fmt.Errorf("rooms: cannot sign the invite this server was asked to countersign: %w", err)
	}
	eventID, err := events.EventID(rules, event)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot name the invite event: %w", err)
	}
	if claimed, _ := event["event_id"].(string); claimed != "" && claimed != eventID {
		return nil, matrixerr.New(matrixerr.CodeInvalidParam,
			"The invite event is not the event its event ID names.")
	}
	event["event_id"] = eventID
	if _, err := s.storeInvite(ctx, event, roomVersion); err != nil {
		return nil, err
	}
	return event, nil
}

// storeInvite writes an invite event and its state. It is shared by the two directions: the
// inviting server stores the countersigned event it got back, and the invited server stores the
// one it just signed.
func (s *Service) storeInvite(ctx context.Context, event map[string]any, roomVersion string) (string, error) {
	eventID, err := events.EventID(roomversion.MustGet(roomVersion), event)
	if err != nil {
		return "", err
	}
	event["event_id"] = eventID
	stream, err := s.events.NextStreamOrdering(ctx)
	if err != nil {
		return "", err
	}
	if err := s.persistEvent(ctx, event, stream); err != nil {
		return "", err
	}
	if err := s.rooms.SetLatestStream(ctx, event["room_id"].(string), stream); err != nil {
		return "", err
	}
	return eventID, nil
}

// checkInviteAllowed asks the room's own rules whether this invite may land here. An invite the
// room's power levels forbid is refused with the answer the specification prescribes, because the
// alternative is signing an event this server would then refuse to relay.
//
// The state the rules are read against is this server's own state for the room plus the state the
// inviting server sent along inside `unsigned.invite_room_state` — for a room this server has never
// heard of, that second part is the only way to judge the invite at all, and it is signed state
// rather than a claim in prose: an event there that does not carry the signature its own origin
// owes is not used, and the invite is refused for it.
//
// What is *not* done is to refuse an invite merely because this server cannot see enough of the
// room to judge it. The checks that already ran say a great deal — the sender lives on the server
// that sent this, the event is an invite, it is about a user who lives here, and the sender's own
// signature is good — and the room's rules are applied again to every event that follows, including
// what the user does about the invite. Refusing on a gap in local knowledge would make an
// unreachable room permanent.
// inviteState reads the state that arrived inside an invite, checks the signatures its origins
// owe, and returns it both as a list this server may keep and as a snapshot the room's rules can
// be judged against.
func (s *Service) inviteState(ctx context.Context, event map[string]any, rules roomversion.Rules,
	verify func(map[string]any, roomversion.Rules) error) ([]map[string]any, *inviteState, error) {
	roomID, _ := event["room_id"].(string)
	own, err := s.loadState(ctx, roomID)
	if err != nil {
		return nil, nil, err
	}
	snapshot := &inviteState{byID: map[string]map[string]any{},
		byKey: map[eventauth.Key]map[string]any{}, below: own}
	unsigned, _ := event["unsigned"].(map[string]any)
	list, _ := unsigned["invite_room_state"].([]any)
	var kept []map[string]any
	for _, entry := range list {
		stateEvent, _ := entry.(map[string]any)
		if stateEvent == nil {
			continue
		}
		if got, _ := stateEvent["room_id"].(string); got != "" && got != roomID {
			return nil, nil, matrixerr.New(matrixerr.CodeInvalidParam,
				"A state event sent along with the invite belongs to a different room.")
		}
		if verify != nil {
			// The invite is signed by the server that sent it, but the state around it was made
			// by other servers, possibly long ago. Their word has to be their own — so a state
			// event whose signature does not check is not used: not for the rules below, and not
			// kept as this server's copy of the room.
			//
			// What it does not do is refuse the invite. The bundled state is a convenience this
			// server asked for, not a claim the inviting server is staking its name on; the
			// invitation itself is signed by the server that holds the room, and that is the
			// promise worth checking. A peer whose older state this server cannot re-verify —
			// rotated keys, a redaction whose rules this server applies differently — would
			// otherwise be unable to invite here at all, which is the wrong way to be strict.
			if err := verify(stateEvent, rules); err != nil {
				s.log.Info("rooms: ignoring a state event sent along with an invite; its origin's signature does not check",
					"room_id", event["room_id"], "event_id", stateEvent["event_id"],
					"type", stateEvent["type"], "error", err)
				continue
			}
		}
		id, _ := stateEvent["event_id"].(string)
		kind, _ := stateEvent["type"].(string)
		if id == "" || kind == "" {
			continue
		}
		snapshot.byID[id] = stateEvent
		if stateKey, hasKey := stateEvent["state_key"].(string); hasKey {
			snapshot.byKey[eventauth.Key{Type: kind, StateKey: stateKey}] = stateEvent
		}
		kept = append(kept, stateEvent)
	}
	return kept, snapshot, nil
}

// checkInviteAgainstRules asks the room's rules about an invite. The events the invite cites are
// what those rules read; if one of them is not here, there is nothing to judge against, and the
// invite is left as the statement the inviting server made. Refusing on a gap in local knowledge
// would make an unreachable room permanent, and the checks that already ran — the sender lives on
// the server that sent this, the event is an invite, it names a user who lives here, and the
// signature is good — say a great deal already.
func (s *Service) checkInviteAgainstRules(ctx context.Context, event map[string]any, rules roomversion.Rules,
	snapshot *inviteState) error {
	for _, id := range events.ReferencedIDs(event, "auth_events") {
		if _, ok := snapshot.Event(id); !ok {
			s.log.Info("rooms: an invite arrived with a room this server cannot see well enough to judge",
				"room_id", event["room_id"], "event_id", event["event_id"], "missing_auth_event", id)
			return nil
		}
	}
	if err := eventauth.Check(rules, event, snapshot, s.authOpts(ctx, rules)); err != nil {
		return matrixerr.New(matrixerr.CodeForbidden, err.Error())
	}
	return nil
}

// storeInviteState keeps the state that arrived with an invite, for a room this server has never
// held. It is written as events and current state and nothing else: no event graph, no forward
// extremities, because this server holds no history of the room and pretending otherwise would be
// an invitation to paginate into a room it knows one event of.
func (s *Service) storeInviteState(ctx context.Context, stateEvents []map[string]any, rules roomversion.Rules) error {
	for _, event := range stateEvents {
		eventID, _ := event["event_id"].(string)
		roomID, _ := event["room_id"].(string)
		eventType, _ := event["type"].(string)
		if eventID == "" || roomID == "" || eventType == "" {
			continue
		}
		if existing, err := s.events.ByID(ctx, eventID); err == nil && existing != nil {
			continue
		}
		canonical, err := events.Canonical(event)
		if err != nil {
			return fmt.Errorf("rooms: cannot encode the state that arrived with an invite: %w", err)
		}
		if claimed, err := events.EventID(rules, event); err != nil || claimed != eventID {
			// An event whose ID is not the hash its content makes is not filed under that ID,
			// whatever the sender wrote.
			s.log.Warn("rooms: state sent with an invite is not the event its ID names",
				"room_id", roomID, "event_id", eventID)
			continue
		}
		row := &persist.Event{
			EventID:        eventID,
			RoomID:         roomID,
			Type:           eventType,
			Sender:         stringOr(event["sender"]),
			Depth:          int64Of(event["depth"]),
			OriginServerTS: int64Of(event["origin_server_ts"]),
			ReceivedTS:     s.now().UnixMilli(),
			JSON:           canonical,
		}
		stateKey, hasStateKey := event["state_key"].(string)
		if hasStateKey {
			key := stateKey
			row.StateKey = &key
		}
		stream, err := s.events.NextStreamOrdering(ctx)
		if err != nil {
			return fmt.Errorf("rooms: cannot place the state that arrived with an invite: %w", err)
		}
		row.StreamOrdering = stream
		if err := s.events.Insert(ctx, row); err != nil {
			if errors.Is(err, persist.ErrConflict) {
				continue
			}
			return fmt.Errorf("rooms: cannot keep the state that arrived with an invite: %w", err)
		}
		if hasStateKey {
			if err := s.state.Set(ctx, &persist.RoomState{
				RoomID: roomID, Type: eventType, StateKey: stateKey, EventID: eventID,
			}); err != nil {
				return fmt.Errorf("rooms: cannot record the state that arrived with an invite: %w", err)
			}
		}
	}
	return nil
}

// inviteState is the state an invite is judged against: what this server holds for the room, with
// the state the inviting server sent along sitting in front of it.
type inviteState struct {
	byID  map[string]map[string]any
	byKey map[eventauth.Key]map[string]any
	below eventauth.Snapshot
}

// State implements [eventauth.Snapshot].
func (c *inviteState) State(key eventauth.Key) (map[string]any, bool) {
	if event, ok := c.byKey[key]; ok {
		return event, true
	}
	if c.below == nil {
		return nil, false
	}
	return c.below.State(key)
}

// Event implements [eventauth.Snapshot].
func (c *inviteState) Event(id string) (map[string]any, bool) {
	if event, ok := c.byID[id]; ok {
		return event, true
	}
	if c.below == nil {
		return nil, false
	}
	return c.below.Event(id)
}

// Rejected implements [eventauth.Snapshot].
func (c *inviteState) Rejected(id string) bool {
	return c.below != nil && c.below.Rejected(id)
}

// creatorOfRoom reads who created a room, from the invite's own account of the room's state. An
// invite-only view of a room still has to record something for the creator column, and guessing
// the room's ID into it would be worse than leaving it empty.
func creatorOfRoom(event map[string]any) string {
	unsigned, _ := event["unsigned"].(map[string]any)
	list, _ := unsigned["invite_room_state"].([]any)
	for _, entry := range list {
		probe, _ := entry.(map[string]any)
		if probe == nil {
			continue
		}
		if kind, _ := probe["type"].(string); kind == eventauth.TypeCreate {
			return stringOr(probe["sender"])
		}
	}
	return ""
}

// inviteShapeProblem checks the event that came back from the invited server against the invite
// that was sent. Only signatures may differ.
func inviteShapeProblem(event map[string]any, change MembershipChange) string {
	kind, _ := event["type"].(string)
	if kind != eventauth.TypeMember {
		return "it is not an m.room.member event"
	}
	content, _ := event["content"].(map[string]any)
	if membership, _ := content["membership"].(string); membership != eventauth.MembershipInvite {
		return "its membership is not invite"
	}
	if sender, _ := event["sender"].(string); sender != change.Sender {
		return "its sender is not the user who sent the invite"
	}
	if stateKey, _ := event["state_key"].(string); stateKey != change.Target {
		return "it is not about the user who was invited"
	}
	if roomID, _ := event["room_id"].(string); roomID != change.RoomID {
		return "it belongs to a different room"
	}
	return ""
}
