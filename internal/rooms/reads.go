package rooms

import (
	"context"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/stream"
)

// CurrentState returns the room's current state events, as stored.
//
// The events are returned in the form the protocol defines them, because a client
// compares state by event content and a server cannot rewrite an event without
// invalidating its hash and signature.
func (s *Service) CurrentState(ctx context.Context, roomID, requester string) ([]map[string]any, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipAny); err != nil {
		return nil, err
	}
	rows, err := s.stateFor(ctx, roomID, requester)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		event, err := s.eventJSONFor(ctx, row.EventID, requester)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}

// stateFor returns the state entries a requester is entitled to read: this server's current
// state for someone still in the room, and the room's state as it stood at the moment someone
// left, for someone who is out of it.
//
// The second half is what a departed member's SPEC-216 read really asks. They are not asking
// what the room looks like now with a few entries missing; they are asking what they left
// behind, and a room whose name or topic changed after they went is entitled to answer that
// rather than to say "no such state". The snapshot is the one the room's own authorisation
// rules judged the boundary event by, so the state a client is shown and the room's view of
// itself at that moment cannot disagree — which cutting today's rows by stream position does
// not achieve, because a key that was replaced afterwards has no row left to cut.
//
// A room whose boundary event carries no recorded snapshot falls back to cutting today's
// state. That is a weaker answer, and it is the answer this server used to give everywhere;
// it stays available rather than turning a missing snapshot into a refusal.
func (s *Service) stateFor(ctx context.Context, roomID, requester string) ([]persist.StateGroupEntry, error) {
	horizon, err := s.readHorizon(ctx, roomID, requester)
	if err != nil {
		return nil, err
	}
	if horizon == 0 {
		rows, err := s.state.All(ctx, roomID)
		if err != nil {
			return nil, err
		}
		entries := make([]persist.StateGroupEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, persist.StateGroupEntry{
				Type: row.Type, StateKey: row.StateKey, EventID: row.EventID,
			})
		}
		return entries, nil
	}
	boundary, err := s.events.EventsWindow(ctx, roomID, horizon+1, true, 1)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read the room at the point %s left it: %w", requester, err)
	}
	if len(boundary) == 0 {
		return nil, nil
	}
	groupID, err := s.groups.GroupOf(ctx, boundary[0].EventID)
	if err != nil {
		rows, err := s.state.All(ctx, roomID)
		if err != nil {
			return nil, err
		}
		entries := make([]persist.StateGroupEntry, 0, len(rows))
		for _, row := range rows {
			stored, err := s.events.ByID(ctx, row.EventID)
			if err != nil {
				return nil, err
			}
			if stored.StreamOrdering > horizon {
				continue
			}
			entries = append(entries, persist.StateGroupEntry{
				Type: row.Type, StateKey: row.StateKey, EventID: row.EventID,
			})
		}
		return entries, nil
	}
	return s.groups.Entries(ctx, groupID)
}

// CurrentStateEvent returns one current state entry. The boolean reports whether the
// room has state for that (type, state key) pair at all, which a handler needs in
// order to answer 404 rather than an empty object.
func (s *Service) CurrentStateEvent(ctx context.Context, roomID, eventType, stateKey, requester string) (map[string]any, bool, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipAny); err != nil {
		return nil, false, err
	}
	entries, err := s.stateFor(ctx, roomID, requester)
	if err != nil {
		return nil, false, err
	}
	for _, entry := range entries {
		if entry.Type != eventType || entry.StateKey != stateKey {
			continue
		}
		event, err := s.eventJSONFor(ctx, entry.EventID, requester)
		if err != nil {
			return nil, false, err
		}
		return event, true, nil
	}
	return nil, false, nil
}

// JoinedMembers returns the room's joined members, for a requester who is themselves
// joined.
//
// The stricter requirement is not decoration: "who is in this room right now" is the
// question a client asks in order to talk to them. Someone who was invited and never
// joined, or who left, has no claim to that list.
func (s *Service) JoinedMembers(ctx context.Context, roomID, requester string) ([]map[string]any, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipJoined); err != nil {
		return nil, err
	}
	return s.Members(ctx, roomID, requester, MemberFilter{Include: eventauth.MembershipJoin})
}

// topicContent builds an m.room.topic content object from a plain topic. The
// specification duplicates the plain text into the structured `m.topic` field so that a
// client can render a topic without guessing whether it is formatted. Only the shorthand
// parameter does this: a client that sends its own m.room.topic event in initial_state
// keeps exactly the content it wrote, because rewriting a client's own event would be a
// server editing what the client signed.
func topicContent(topic string) map[string]any {
	return map[string]any{
		"topic": topic,
		"m.topic": map[string]any{
			"m.text": []any{map[string]any{
				"body":     topic,
				"mimetype": "text/plain",
			}},
		},
	}
}

// deduplicateState keeps one entry per state key, with the last one written winning, so
// that creating a room does not write the same key twice and leave the earlier version
// as a pointless step in the room's history.
func deduplicateState(list []StateContent) []StateContent {
	positions := make(map[string]int, len(list))
	out := make([]StateContent, 0, len(list))
	for _, entry := range list {
		key := entry.Type + "\x00" + entry.StateKey
		if at, seen := positions[key]; seen {
			out[at] = entry
			continue
		}
		positions[key] = len(out)
		out = append(out, entry)
	}
	return out
}

// MemberFilter narrows a member list. Include keeps one membership, Exclude drops
// one; the Client-Server API needs both because `not_membership` excludes while a
// "who is here" view selects.
//
// At is a sync token: the roster is then the one this server's history held at that
// position rather than the current one.
type MemberFilter struct {
	Include string
	Exclude string
	At      string
}

// matches reports whether an event survives the membership half of a filter. It is shared by the
// current roster and the historical one so that `membership=` cannot mean one thing on `/members`
// and another thing on `/members?at=`.
func (f MemberFilter) matches(event map[string]any) bool {
	if f.Include == "" && f.Exclude == "" {
		return true
	}
	content, _ := event["content"].(map[string]any)
	membership, _ := content["membership"].(string)
	if f.Include != "" && membership != f.Include {
		return false
	}
	return f.Exclude == "" || membership != f.Exclude
}

// Members returns the room's m.room.member events — the current ones, unless `at` names an
// earlier point in this server's history.
func (s *Service) Members(ctx context.Context, roomID, requester string, filter MemberFilter) ([]map[string]any, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipAny); err != nil {
		return nil, err
	}
	if filter.At != "" {
		position, err := stream.Parse(filter.At)
		if err != nil {
			return nil, matrixerr.New(matrixerr.CodeInvalidParam,
				"at is not a synchronisation token this server issued.")
		}
		return s.membersAt(ctx, roomID, requester, int64(position), filter)
	}
	// Someone still in the room reads the roster as it is; someone out of it reads the roster
	// as it stood when they went out. The second is the same question `?at=` asks with the
	// token set to the moment of leaving, and it gets the same answer rather than today's
	// roster with the newcomers cut out of it — which loses the member who joined before the
	// requester left and left after, because today's state holds no row for them at all.
	entries, err := s.stateFor(ctx, roomID, requester)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != eventauth.TypeMember {
			continue
		}
		event, err := s.eventJSONFor(ctx, entry.EventID, requester)
		if err != nil {
			return nil, err
		}
		if !filter.matches(event) {
			continue
		}
		out = append(out, event)
	}
	return out, nil
}

// membersAt answers `GET /members?at=<token>`: the roster this server's history held at that
// point in the stream.
//
// The answer is the state snapshot the newest event at or before that position was sent against —
// the same snapshot the authorisation rules judged that event by — so the roster a client is shown
// and the room's own view of itself at that moment cannot disagree. Cutting today's state by stream
// position looks identical in the easy case and wrong in the interesting one: a member who joined
// before the token and left after it was a member then, and today's state does not hold them at all,
// so there is no row left to cut.
func (s *Service) membersAt(ctx context.Context, roomID, requester string, position int64, filter MemberFilter) ([]map[string]any, error) {
	asOf, err := s.readHorizon(ctx, roomID, requester)
	if err != nil {
		return nil, err
	}
	// `from` is exclusive in both directions, so asking for the window before position+1 is how
	// the event standing exactly at the token is included.
	boundary, err := s.events.EventsWindow(ctx, roomID, position+1, true, 1)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read the room at the point %q asks for: %w", filter.At, err)
	}
	if len(boundary) == 0 {
		// Nothing had happened in this room by that token, so the roster is empty. Today's
		// members would answer a different question and would be believed as though they
		// answered this one.
		return []map[string]any{}, nil
	}
	groupID, err := s.groups.GroupOf(ctx, boundary[0].EventID)
	if err != nil {
		// The token names a point this server cannot reconstruct. Guessing from what is in
		// place now would present an invented history; a client that asked a question about a
		// point it no longer holds can be told so.
		return nil, matrixerr.New(matrixerr.CodeInvalidParam,
			"at names a point in this room whose state this server cannot reconstruct.")
	}
	entries, err := s.groups.Entries(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read the state at the point %q asks for: %w", filter.At, err)
	}
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != eventauth.TypeMember {
			continue
		}
		stored, err := s.events.ByID(ctx, entry.EventID)
		if err != nil {
			return nil, err
		}
		// The requester's own horizon still applies: a roster from the past is not a licence to
		// show the room to somebody who left it. The boundary event itself is the exception — it
		// is what the token names, and the requester's view of the room includes it.
		if asOf > 0 && stored.StreamOrdering > asOf && entry.EventID != boundary[0].EventID {
			continue
		}
		event, err := s.eventJSONFor(ctx, entry.EventID, requester)
		if err != nil {
			return nil, err
		}
		if !filter.matches(event) {
			continue
		}
		out = append(out, event)
	}
	return out, nil
}

// JoinedRooms lists the rooms where the user's current membership is join.
//
// It reads the member state that names the user rather than scanning every room, which
// is the difference between work proportional to the user and work proportional to the
// server.
func (s *Service) JoinedRooms(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.state.MembershipsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		event, err := s.eventJSON(ctx, row.EventID)
		if err != nil {
			return nil, err
		}
		content, _ := event["content"].(map[string]any)
		if membership, _ := content["membership"].(string); membership == eventauth.MembershipJoin {
			out = append(out, row.RoomID)
		}
	}
	return out, nil
}

// membershipRequirement says how strict a read is about who asks.
type membershipRequirement int

const (
	// membershipAny accepts anyone with a stake in the room: a current member, a former
	// member reading the history they were part of, or any user when the room made its
	// history world readable.
	membershipAny membershipRequirement = iota
	// membershipJoined accepts only a joined member, which is what a "who is here right
	// now" view needs.
	membershipJoined
	// membershipStrict accepts only a current member, with no history-visibility
	// exception. A room's names say that it exists and what it is called; making its
	// history world readable is a decision about its history, not an announcement of its
	// names.
	membershipStrict
)

// readHorizon returns the stream position a requester's view of a room stops at, or zero
// when their view is current.
//
// A former member's entitlement ends when their membership does. This server keeps only
// the room's current state, not a snapshot per event, so it cannot reconstruct the value
// a key had at that moment. What it can do is withhold every entry that changed
// afterwards, which is the part a former member has no claim to. That is a partial
// answer — a key that was replaced after the leave reads as absent rather than as its old
// value — and it is recorded as such; the alternative, answering with the current value,
// would hand over state written after the requester left.
func (s *Service) readHorizon(ctx context.Context, roomID, requester string) (int64, error) {
	membership, err := s.MembershipOf(ctx, roomID, requester)
	if err != nil {
		return 0, err
	}
	switch membership {
	case eventauth.MembershipLeave, eventauth.MembershipBan:
		row, err := s.state.Get(ctx, roomID, eventauth.TypeMember, requester)
		if err != nil {
			if notFound(err) {
				return 0, nil
			}
			return 0, err
		}
		event, err := s.events.ByID(ctx, row.EventID)
		if err != nil {
			if notFound(err) {
				return 0, nil
			}
			return 0, err
		}
		return event.StreamOrdering, nil
	default:
		return 0, nil
	}
}

// requireMembership refuses a room read for someone with no standing in the room.
//
// A room's state and member list say who is talking and what was said, so the gate is the
// room's membership rather than the mere existence of an account. Three cases follow from
// that and from what the specification's history visibility means:
//
//   - a current member (joined, invited or knocking) may read: an invited user cannot
//     accept an invitation without seeing what they are accepting;
//   - a former member may read the state and member list of a room they left or were
//     banned from — the history they took part in stays theirs, and this is the behaviour
//     Complement pins as SPEC-216 — but not the live roster;
//   - someone who was never in the room may read only what the room itself opened up by
//     setting `history_visibility: world_readable`.
func (s *Service) requireMembership(ctx context.Context, roomID, requester string, requirement membershipRequirement) error {
	if _, err := s.requireRoom(ctx, roomID); err != nil {
		return err
	}
	membership, err := s.MembershipOf(ctx, roomID, requester)
	if err != nil {
		return err
	}
	// A room the requester has asked not to be told about is not shown to them, even though a
	// former member may otherwise read the room they left. Their standing in it has not changed
	// — they were there — but they have asked for it out of their view, and this server honours
	// that until they come back. Someone who has actually rejoined is unaffected: coming back
	// clears the request.
	if membership != eventauth.MembershipJoin {
		mark, err := s.forgottenRoom(ctx, roomID, requester)
		if err != nil {
			return err
		}
		if mark != nil {
			return matrixerr.New(matrixerr.CodeForbidden,
				"You have forgotten this room; join it again to read it.")
		}
	}
	switch membership {
	case eventauth.MembershipJoin:
		return nil
	case eventauth.MembershipInvite, eventauth.MembershipKnock:
		if requirement != membershipJoined {
			return nil
		}
	case eventauth.MembershipLeave, eventauth.MembershipBan:
		if requirement == membershipAny {
			return nil
		}
	default:
		// No membership at all: only a room that opened its history may be read.
		if requirement == membershipAny {
			visibility, err := s.historyVisibility(ctx, roomID)
			if err == nil && visibility == "world_readable" {
				return nil
			}
		}
	}
	return matrixerr.New(matrixerr.CodeForbidden,
		"You are not a member of this room, so this server will not show its state.")
}

// requireRoom turns a missing room into the protocol error, so a read of an unknown
// room cannot look like a room with empty state.
func (s *Service) requireRoom(ctx context.Context, roomID string) (*persist.Room, error) {
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
		}
		return nil, fmt.Errorf("rooms: cannot load the room: %w", err)
	}
	return room, nil
}

// eventJSON reads a stored event back as the protocol object, in the form a client is
// entitled to see: the redacted form when a redaction was applied, the `redacts` field in
// the place the client's room version expects, and — for a redacted event — a copy of the
// redaction that did it under `unsigned.redacted_because`, which is how a client learns
// why an event it once read is now empty.
//
// This is the reader-free form, and most callers want it: they are asking the event a question
// for themselves — what is this room's join rule, who counted as a member, what did the canonical
// alias point at — and are not handing the event to anyone. Those reads carry no
// `unsigned.membership`, which the specification allows (it defines the field as the requesting
// user's membership, and lets a server omit it), and keeps a room-directory scan from paying for
// a snapshot lookup per event it only wanted one field from. A read that does hand the event to a
// named user goes through eventJSONFor instead.
func (s *Service) eventJSON(ctx context.Context, eventID string) (map[string]any, error) {
	row, err := s.events.ByID(ctx, eventID)
	if err != nil {
		if notFound(err) {
			return nil, fmt.Errorf("rooms: state points at event %s, which is not stored", eventID)
		}
		return nil, err
	}
	return s.serveEvent(ctx, row)
}

// eventJSONFor is eventJSON for an event that is going to a particular user, which is what makes
// `unsigned.membership` answerable: the field is that user's membership of the room as of the
// moment of the event, so it needs a reader to exist. `/state`, `/state/{type}/{key}` and
// `/members` come through here; `/messages` and `/event` call the servicer directly because they
// already hold the rows. `/context` and `/relations` are not endpoints this server has yet.
func (s *Service) eventJSONFor(ctx context.Context, eventID, requester string) (map[string]any, error) {
	row, err := s.events.ByID(ctx, eventID)
	if err != nil {
		if notFound(err) {
			return nil, fmt.Errorf("rooms: state points at event %s, which is not stored", eventID)
		}
		return nil, err
	}
	body, err := s.serve.ServeAs(ctx, row, requester)
	if err != nil {
		if errors.Is(err, redaction.ErrWithheld) {
			return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", eventID)
		}
		return nil, err
	}
	return body, nil
}

// serveEvent is eventJSON over a row already in hand, reader-free. The decision about what a
// client may see is the redaction servicer's, not this package's: every read path and `/sync` go
// through the same one, differing only in whether they can name the reader.
func (s *Service) serveEvent(ctx context.Context, row *persist.Event) (map[string]any, error) {
	return s.serve.Serve(ctx, row)
}
