package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Knock and Leave, asked of a server that holds the room.
//
// These are the join handshake pointed at a room this server is not in, and the reason they exist
// is the same asymmetry that makes `/make_join` exist: a user's own server knows what the user wants
// and can sign for them, but it does not know where the room's history currently ends, which of its
// events authorise a change, or how deep the new one sits. Only a resident server can say those, and
// only the user's server can sign — so the change is described by one server and signed by the other,
// and neither can decide it alone.
//
// A leave is the case where skipping the first half is most tempting and most wrong. A server that
// has never held the room can write an `m.room.member` event with itself as sender and state key;
// what it cannot do is place that event in a history it has never seen. The event it invents cites
// the wrong predecessors and is resolved away by every server in the room, so the user's departure
// silently never happened, and the room goes on believing they are in it.

// The result type these handshakes answer with is [rooms.RemoteMembershipResult]: the room service
// is what asks for them, and a second shape for the same answer is how two components end up
// disagreeing about what a handshake brought back.

// Knock carries one of this server's users to a knock on a room this server does not hold.
func (j *Joiner) Knock(ctx context.Context, roomID, userID string, destinations []string) (rooms.RemoteMembershipResult, error) {
	return j.remoteMembership(ctx, roomID, userID, eventauth.MembershipKnock, destinations)
}

// Leave takes one of this server's users out of a room this server does not hold — including the
// common case where they are leaving because they were invited and do not want to join.
func (j *Joiner) Leave(ctx context.Context, roomID, userID string, destinations []string) (rooms.RemoteMembershipResult, error) {
	return j.remoteMembership(ctx, roomID, userID, eventauth.MembershipLeave, destinations)
}

// remoteMembership runs the handshake against the servers worth asking, in the order the client
// named them.
//
// The rule about a refusal is the join's rule and for the same reason: a resident server that says
// the room does not permit this is answering about the room, not about itself, and asking its
// neighbours asks the same question again.
func (j *Joiner) remoteMembership(ctx context.Context, roomID, userID, membership string, destinations []string) (rooms.RemoteMembershipResult, error) {
	if j.Client == nil || j.Keys == nil {
		return rooms.RemoteMembershipResult{}, errors.New("federation: the membership handshake is not wired up")
	}
	var lastErr error
	for _, destination := range j.candidates(ctx, roomID, destinations) {
		result, err := j.membershipVia(ctx, destination, roomID, userID, membership)
		if err == nil {
			return result, nil
		}
		var refusal *RemoteRefusal
		if errors.As(err, &refusal) {
			return rooms.RemoteMembershipResult{}, refusal.toMembershipError(membership)
		}
		lastErr = err
		j.Log.Warn("federation: a server could not carry a "+membership,
			slog.String("destination", destination), slog.String("room_id", roomID),
			slog.Any("error", err))
	}
	if lastErr != nil {
		return rooms.RemoteMembershipResult{}, fmt.Errorf("federation: no server in the room would take the %s: last attempt: %v",
			membership, lastErr)
	}
	return rooms.RemoteMembershipResult{}, fmt.Errorf("federation: no server in the room would take the %s", membership)
}

// membershipVia runs both halves of the handshake against one server.
func (j *Joiner) membershipVia(ctx context.Context, destination, roomID, userID, membership string) (rooms.RemoteMembershipResult, error) {
	template, version, err := j.makeMembership(ctx, destination, roomID, userID, membership)
	if err != nil {
		return rooms.RemoteMembershipResult{}, err
	}
	rules, ok := roomversion.Get(version)
	if !ok {
		return rooms.RemoteMembershipResult{}, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot %s in.", roomID, version, membership).
			WithExtra(map[string]any{"room_version": version})
	}

	// What only this server can supply: which server speaks for this user, and when the change was
	// made. Everything else came from a server that holds the room.
	template["origin"] = j.Name
	template["origin_server_ts"] = j.now().UnixMilli()
	if err := events.Seal(j.Name, rules, template, j.Keys); err != nil {
		return rooms.RemoteMembershipResult{}, fmt.Errorf("federation: cannot sign the %s for %s: %w", membership, roomID, err)
	}
	eventID, err := events.EventID(rules, template)
	if err != nil {
		return rooms.RemoteMembershipResult{}, fmt.Errorf("federation: cannot name the %s event: %w", membership, err)
	}
	template["event_id"] = eventID
	sealed, err := events.Canonical(template)
	if err != nil {
		return rooms.RemoteMembershipResult{}, fmt.Errorf("federation: cannot encode the %s: %w", membership, err)
	}

	answer, err := j.sendMembership(ctx, destination, roomID, eventID, membership, sealed)
	if err != nil {
		return rooms.RemoteMembershipResult{}, err
	}
	if membership == eventauth.MembershipKnock {
		// What the resident server hands back is the room's identity — the state a room shows to
		// somebody it has not let in. It is read here and logged, and it is not stored, and the
		// reason is worth keeping next to the code that does not store it.
		//
		// This server is not a holder of a knocked room, and the handover cannot be ingested the way
		// a join's is: `knock_room_state` deliberately carries no `m.room.member` events, and in every
		// room version that has knock, the member event of whoever wrote a state event is one of that
		// event's auth events. Hand the state over as full PDUs and none of it can be authorised here,
		// because the events that would authorise it are exactly the ones a knock is not allowed to
		// reveal; the whole set, including this server's own knock, would sit unresolved.
		//
		// What is missing is a knock's own record — the shape an invite-hanging room is for the
		// invited user — rather than a copy of a room this server has no business holding. Until that
		// exists, the user who knocked on a remote room gets a 200 and nothing to look at: the room
		// does not appear in her sync, and the knock she made is invisible to her own server. Tracked
		// as T-4075, with the Complement knock test that asks for it named there.
		state := pdusFrom(answer, "knock_room_state")
		j.Log.Info("federation: a local user knocked on a room held elsewhere",
			slog.String("room_id", roomID), slog.String("user_id", userID),
			slog.String("destination", destination), slog.String("event_id", eventID),
			slog.Int("room_state_events", len(state)))
		return rooms.RemoteMembershipResult{EventID: eventID, RoomState: len(state)}, nil
	}
	j.Log.Info("federation: a local user left a room held elsewhere",
		slog.String("room_id", roomID), slog.String("user_id", userID),
		slog.String("destination", destination), slog.String("event_id", eventID))
	return rooms.RemoteMembershipResult{EventID: eventID}, nil
}

// makeJoin asks a resident server what a join by this user would look like, and checks the answer
// before any signing is done.
//
// The checks are not paranoia. The template becomes an event this server signs, so a template that
// names a different room, or a different user, is a request to sign somebody else's join. An
// already-signed or already-hashed template is the same problem in another form: this server would be
// adding its name to an event somebody else had already made.
func (j *Joiner) makeJoin(ctx context.Context, destination, roomID, userID string) (map[string]any, string, error) {
	return j.makeMembership(ctx, destination, roomID, userID, eventauth.MembershipJoin)
}

// makeMembership asks for the template of a join, a leave or a knock and checks it.
//
// Checking it is the load-bearing part of the asking half. Whatever arrives here becomes an event
// this server signs with its own key and puts its name on; a template that quietly names another
// room, another user, or a membership the user did not ask for is a forged event as far as every
// other server in the room is concerned.
func (j *Joiner) makeMembership(ctx context.Context, destination, roomID, userID, membership string) (map[string]any, string, error) {
	target := "/_matrix/federation/v1/make_" + membership + "/" + roomID + "/" + userID
	response, err := j.Client.Do(ctx, destination, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	if refusal := refusalFrom(destination, response); refusal != nil {
		return nil, "", refusal
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, "", fmt.Errorf("federation: %s answered the %s template with %d",
			destination, membership, response.StatusCode)
	}
	// The leave handshake is spelled two ways in the wild — `event` by gomatrixserverlib, which is
	// what Dendrite and Complement answer with, and `membership_event` by the specification — so
	// either name is read. What matters is that one event arrives, not which key carried it.
	var offer struct {
		Event           map[string]any `json:"event"`
		MembershipEvent map[string]any `json:"membership_event"`
		RoomVersion     string         `json:"room_version"`
	}
	if err := json.Unmarshal(response.Body, &offer); err != nil {
		return nil, "", fmt.Errorf("federation: %s answered the %s template with something unreadable: %v",
			destination, membership, err)
	}
	event := offer.Event
	if event == nil {
		event = offer.MembershipEvent
	}
	if event == nil {
		return nil, "", fmt.Errorf("federation: %s answered the %s template with no event", destination, membership)
	}
	if kind, _ := event["type"].(string); kind != eventauth.TypeMember {
		return nil, "", fmt.Errorf("federation: %s offered a %s as a %s", destination, kind, membership)
	}
	if got, _ := event["room_id"].(string); got != roomID {
		return nil, "", fmt.Errorf("federation: %s offered a %s template for room %q", destination, membership, got)
	}
	if got, _ := event["sender"].(string); got != userID {
		return nil, "", fmt.Errorf("federation: %s offered a %s by %s", destination, membership, got)
	}
	if got, _ := event["state_key"].(string); got != userID {
		return nil, "", fmt.Errorf("federation: %s offered a member event for %s", destination, got)
	}
	content, _ := event["content"].(map[string]any)
	if claimed, _ := content["membership"].(string); claimed != membership {
		return nil, "", fmt.Errorf("federation: %s offered a membership of %v for a %s",
			destination, content["membership"], membership)
	}
	// An already-hashed or already-signed template would mean this server adding its name to an
	// event somebody else had already made.
	for _, key := range []string{"event_id", "signatures", "hashes"} {
		if _, present := event[key]; present {
			return nil, "", fmt.Errorf("federation: %s offered a %s template that already carries %s",
				destination, membership, key)
		}
	}
	if offer.RoomVersion == "" {
		return nil, "", fmt.Errorf("federation: %s offered a %s template with no room version", destination, membership)
	}
	return event, offer.RoomVersion, nil
}

// sendMembership hands the signed event back to the resident server.
//
// v2 is asked for first. A server that answers 404 to it is one that only knows the older spelling,
// which is still a correct question for a room version 1 or 2 room; unlike the invite, the v1 body of
// these endpoints is the event itself, so the same bytes go out twice.
func (j *Joiner) sendMembership(ctx context.Context, destination, roomID, eventID, membership string, pdu []byte) (map[string]any, error) {
	target := "/_matrix/federation/v2/send_" + membership + "/" + roomID + "/" + eventID
	response, err := j.Client.Do(ctx, destination, http.MethodPut, target, pdu)
	if err != nil {
		return nil, err
	}
	if refusal := refusalFrom(destination, response); refusal != nil {
		return nil, refusal
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusBadRequest {
		fallback := "/_matrix/federation/v1/send_" + membership + "/" + roomID + "/" + eventID
		j.Log.Debug("federation: asking a server to accept a "+membership+" over the v1 endpoint",
			slog.String("destination", destination), slog.String("room_id", roomID))
		response, err = j.Client.Do(ctx, destination, http.MethodPut, fallback, pdu)
		if err != nil {
			return nil, err
		}
		if refusal := refusalFrom(destination, response); refusal != nil {
			return nil, refusal
		}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("federation: %s refused the signed %s with %d: %s",
			destination, membership, response.StatusCode, strings.TrimSpace(string(response.Body)))
	}
	var answer map[string]any
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return nil, fmt.Errorf("federation: %s answered the %s with something unreadable: %v",
			destination, membership, err)
	}
	return answer, nil
}
