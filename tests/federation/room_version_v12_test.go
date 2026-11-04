package federation

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// joinHashedRoom asks a server to join its user to a room version 12 room. The servers to
// ask have to be named: such a room id carries no server name, so the identifier alone can
// never say whom to knock on.
func joinHashedRoom(s *server, sess *session, roomID string, servers ...string) {
	s.t.Helper()
	query := ""
	for _, name := range servers {
		query += "&server_name=" + name
	}
	path := fmt.Sprintf("/_matrix/client/v3/rooms/%s/join?%s", roomID, strings.TrimPrefix(query, "&"))
	s.mustDo(http.StatusOK, http.MethodPost, path, sess.Token, map[string]any{})
}

// TestHashedRoomIDTravelsFederation is room version 12 across two servers: a room whose id
// is the hash of its create event is joined by a user on another server, and the events
// that server then makes carry that hash as their room id and are accepted by the room's
// own server.
func TestHashedRoomIDTravelsFederation(t *testing.T) {
	cluster := newCluster(t)
	host, guest := cluster.one(), cluster.two()
	defer host.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)()
	defer guest.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)()
	alice := host.register("alice", "wonderland")
	bob := guest.register("bob", "builders")

	roomID := host.createRoom(alice, map[string]any{
		"preset":       "private_chat",
		"room_version": "12",
		"invite":       []any{bob.UserID},
	})
	if strings.Contains(roomID, ":") {
		t.Fatalf("a room version 12 room id names a server: %s", roomID)
	}
	// The room's own server is asked, because the identifier cannot say who to ask.
	joinHashedRoom(guest, bob, roomID, host.name)

	// Both servers now name the room by the same hash, and the second one holds its create
	// event: the hash is only worth anything to a server that can show the event it came from.
	for _, s := range []*server{host, guest} {
		if _, err := s.db.RoomRepo.Get(roomID); err != nil {
			t.Errorf("%s holds no room row for %s: %v", s.name, roomID, err)
		}
		state, err := s.db.StateRepo.List(roomID, nil, "")
		if err != nil {
			t.Fatalf("reading the state of %s on %s: %v", roomID, s.name, err)
		}
		create := ""
		for _, row := range state {
			if row.Type == matrix.TypeCreate {
				create = row.EventID
			}
		}
		if create != "$"+roomID[1:] {
			t.Errorf("%s holds create event %q for %s, want the event the room id is a hash of",
				s.name, create, roomID)
		}
	}

	// An event made on the server that did not make the room carries the hash, and the room's
	// own server takes it.
	messageID := guest.send(bob, roomID, "from the other server")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if visible(t, host, alice, roomID)[messageID] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never showed %s's message %s to alice", host.name, guest.name, messageID)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestHashedRoomStateResolutionWeighsCreatorsFirst is the state resolution of a room
// version 12 room seen from two servers: a locally created event and a peer's event for the
// same key disagree, and the room's power events are applied in the order the version says
// - highest power first, so the peer administrator with the larger level has the last word.
func TestHashedRoomStateResolutionWeighsCreatorsFirst(t *testing.T) {
	cluster := newCluster(t)
	host, guest := cluster.one(), cluster.two()
	defer host.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)()
	defer guest.svc.StartFederationOutbox(context.Background(), 20*time.Millisecond)()
	alice := host.register("alice", "wonderland")
	bob := guest.register("bob", "builders")

	roomID := host.createRoom(alice, map[string]any{
		"preset":       "private_chat",
		"room_version": "12",
		"invite":       []any{bob.UserID},
	})
	joinHashedRoom(guest, bob, roomID, host.name)

	// Bob is given a level larger than any the room could otherwise name. The creator's own
	// power is not written down anywhere, so this does not outrank alice - it only outranks
	// every event the room weighs by level.
	host.mustDo(http.StatusOK, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.power_levels/", alice.Token,
		matrix.Object{"users": matrix.Object{bob.UserID: 9493420}})

	// Wait for the level to reach the peer: bob's own server has to be able to see it before
	// it can build an event that is weighed by it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if content := stateContent(t, guest, bob, roomID, matrix.TypePowerLevels, ""); content != nil {
			if users, ok := content["users"].(map[string]any); ok {
				if level, ok := users[bob.UserID].(float64); ok && level == 9493420 {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never learned that %s holds a level of 9493420 in %s", guest.name, bob.UserID, roomID)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// The two servers then disagree about the room's join rule, each writing its own event on
	// top of the level change.
	host.mustDo(http.StatusOK, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules/", alice.Token,
		matrix.Object{"join_rule": "invite"})
	knockID := guest.mustDo(http.StatusOK, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.join_rules/", bob.Token,
		matrix.Object{"join_rule": "knock"}).stringField("event_id")

	// The peer's event has to reach the room's own server and be seen by its creator, and the
	// state the room settles on is the knock: the power events are weighed from the highest
	// level down, so the level bob holds is applied last and wins.
	deadline = time.Now().Add(10 * time.Second)
	for {
		if visible(t, host, alice, roomID)[knockID] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never accepted or showed the peer's join rule event %s", host.name, knockID)
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Both servers settle on the knock: the power events are weighed from the highest level
	// down, so the level bob holds is applied last and wins.
	settles := func(s *server, sess *session) {
		for attempt := 0; attempt <= 20; attempt++ {
			content := stateContent(t, s, sess, roomID, matrix.TypeJoinRules, "")
			if content != nil && content["join_rule"] == "knock" {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Errorf("%s did not settle the join rule of %s on knock", s.name, roomID)
	}
	settles(host, alice)
	settles(guest, bob)
}

// stateContent reads one state event's content as a client does, and returns nil when the
// room has no such event.
func stateContent(t *testing.T, s *server, sess *session, roomID, typ, stateKey string) matrix.Object {
	t.Helper()
	got := s.do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/state/"+typ+"/"+stateKey, sess.Token, nil)
	if got.Status != http.StatusOK {
		return nil
	}
	return matrix.MustParseObject(got.Raw)
}

// handInvite builds and signs an invitation the way one server invites a user of another: the
// event is signed by the inviting server's own key, because that is the only authority an
// invited server has for a room it has never heard of.
func handInvite(t *testing.T, from *server, inviter, invitee, roomID string, depth int64) (matrix.Object, string) {
	t.Helper()
	key, err := from.svc.SigningKey(context.Background())
	if err != nil {
		t.Fatalf("the signing key of %s: %v", from.name, err)
	}
	// The authority an invitation cites, picked the way the selection algorithm picks it for
	// this version: the room's levels, its rule for letting people in, and the inviter's own
	// membership. A room version 12 room selects no create event - the room id names it.
	auth := []any{}
	for _, event := range statePDUsOf(t, from, roomID) {
		typ := federation.EventField(event, "type")
		stateKey := ""
		if key, isState := event["state_key"].(string); isState {
			stateKey = key
		}
		if typ != matrix.TypePowerLevels && typ != matrix.TypeJoinRules &&
			!(typ == matrix.TypeMember && stateKey == inviter) {
			continue
		}
		id, err := eventcrypto.EventID(event, "12")
		if err != nil {
			t.Fatalf("naming an auth event of %s: %v", roomID, err)
		}
		auth = append(auth, id)
	}
	if len(auth) != 3 {
		t.Fatalf("the state of %s offers %d of the three events an invitation cites", roomID, len(auth))
	}
	invite := matrix.Object{
		"auth_events":      auth,
		"content":          matrix.Object{"membership": matrix.MembershipInvite},
		"depth":            depth,
		"origin_server_ts": time.Now().UnixMilli(),
		"prev_events":      []any{},
		"room_id":          roomID,
		"sender":           inviter,
		"state_key":        invitee,
		"type":             matrix.TypeMember,
	}
	signed, err := eventcrypto.HashAndSign(invite, "12", key.Private, from.name, key.ID)
	if err != nil {
		t.Fatalf("%s could not sign the invitation: %v", from.name, err)
	}
	eventID, err := eventcrypto.EventID(signed, "12")
	if err != nil {
		t.Fatalf("naming the invitation: %v", err)
	}
	return signed, eventID
}

// statePDUsOf is a room's current state as complete events, which is the shape MSC4311 asks
// one server to hand another beside an invite.
func statePDUsOf(t *testing.T, s *server, roomID string) []matrix.Object {
	t.Helper()
	rows, err := s.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		t.Fatalf("reading the state of %s on %s: %v", roomID, s.name, err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EventID)
	}
	events, err := s.db.EventRepo.GetByIDs(ids)
	if err != nil {
		t.Fatalf("reading the events of %s on %s: %v", roomID, s.name, err)
	}
	out := make([]matrix.Object, 0, len(ids))
	for _, id := range ids {
		event, found := events[id]
		if !found {
			continue
		}
		out = append(out, matrix.MustParseObject([]byte(event.JSON)))
	}
	return out
}

// objectsOfAnswer reads a list of events out of a federation answer.
func objectsOfAnswer(t *testing.T, answer matrix.Object, field string) []matrix.Object {
	t.Helper()
	raw, ok := answer[field]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("the answer holds %s as %T, not a list", field, raw)
	}
	out := make([]matrix.Object, 0, len(list))
	for _, entry := range list {
		event, ok := entry.(matrix.Object)
		if !ok {
			t.Fatalf("%s holds %T, which is not an event", field, entry)
		}
		out = append(out, event)
	}
	return out
}

// TestHashedRoomInviteShipsTheRoomItNames is MSC4311 read from the side of the server being
// invited to a room version 12 room: the state beside the invitation has to be complete
// events, because the room's name is the hash of its create event and nothing else in the
// invite says what the room is called.
func TestHashedRoomInviteShipsTheRoomItNames(t *testing.T) {
	cluster := newCluster(t)
	host, guest := cluster.one(), cluster.two()
	alice := host.register("alice", "wonderland")
	bob := guest.register("bob", "builders")

	roomID := host.createRoom(alice, map[string]any{
		"preset":       "private_chat",
		"room_version": "12",
	})
	signed, eventID := handInvite(t, host, alice.UserID, bob.UserID, roomID, 3)

	answer, err := host.peer().Put(t.Context(), guest.name,
		"/_matrix/federation/v2/invite/"+url.PathEscape(roomID)+"/"+url.PathEscape(eventID),
		matrix.Object{
			"event":             signed,
			"room_version":      "12",
			"invite_room_state": anyEvents(statePDUsOf(t, host, roomID)),
		})
	if err != nil {
		t.Fatalf("%s refused an invitation carrying its room's complete state: %v", guest.name, err)
	}

	// The invited server now holds the room by its hash, and holds the event that hash is of:
	// a complete create event hashes to the room's name, and a tuple hashes to nothing.
	if _, err := guest.db.RoomRepo.Get(roomID); err != nil {
		t.Fatalf("%s holds no room row for %s: %v", guest.name, roomID, err)
	}
	room, err := guest.db.RoomRepo.Get(roomID)
	if err == nil && room.RoomVersion != "12" {
		t.Errorf("%s recorded %s as version %q, want 12", guest.name, roomID, room.RoomVersion)
	}
	rows, err := guest.db.StateRepo.List(roomID, nil, "")
	if err != nil {
		t.Fatalf("reading the state of %s on %s: %v", roomID, guest.name, err)
	}
	create := ""
	for _, row := range rows {
		if row.Type == matrix.TypeCreate {
			create = row.EventID
		}
	}
	if create != "$"+roomID[1:] {
		t.Errorf("%s holds create event %q for %s, want the event the room id hashes from",
			guest.name, create, roomID)
	}

	// The invitation itself comes back signed by the invited server as well: an invite two
	// servers have both signed is one neither of them can later deny.
	signedBack, ok := answer["event"].(matrix.Object)
	if !ok {
		t.Fatalf("%s answered the invitation with %T, not an event", guest.name, answer["event"])
	}
	if _, signed := signedBack["signatures"].(map[string]any); !signed {
		t.Errorf("%s answered the invitation with an event nobody signed", guest.name)
	}
	if _, err := guest.db.EventRepo.Get(eventID); err != nil {
		t.Errorf("%s took the invitation but did not keep the event %s", guest.name, eventID)
	}
}

// TestHashedRoomInviteAboutAnUncheckableRoomIsRefused is the same invitation with the state
// taken away: a room version 12 invite whose state cannot be hashed names a room the invited
// server cannot check, and MSC4311 asks that it be refused by name rather than kept on trust.
func TestHashedRoomInviteAboutAnUncheckableRoomIsRefused(t *testing.T) {
	cluster := newCluster(t)
	host, guest := cluster.one(), cluster.two()
	alice := host.register("alice", "wonderland")
	bob := guest.register("bob", "builders")

	roomID := host.createRoom(alice, map[string]any{
		"preset":       "private_chat",
		"room_version": "12",
	})

	// The same event trimmed to a client's stripped state: a type, a state key, a sender and a
	// content object. It is the shape a client is shown and the shape a server is not.
	trimmed := func(event matrix.Object) matrix.Object {
		return matrix.Object{
			"type":      event["type"],
			"state_key": event["state_key"],
			"sender":    event["sender"],
			"content":   objectOfContent(event),
		}
	}

	for _, tc := range []struct {
		name  string
		state func(t *testing.T) []matrix.Object
	}{
		{"no create event", func(t *testing.T) []matrix.Object {
			var kept []matrix.Object
			for _, event := range statePDUsOf(t, host, roomID) {
				if federation.EventField(event, "type") != matrix.TypeCreate {
					kept = append(kept, event)
				}
			}
			return kept
		}},
		{"the create event as a tuple", func(t *testing.T) []matrix.Object {
			var kept []matrix.Object
			for _, event := range statePDUsOf(t, host, roomID) {
				if federation.EventField(event, "type") == matrix.TypeCreate {
					kept = append(kept, trimmed(event))
					continue
				}
				kept = append(kept, event)
			}
			return kept
		}},
		{"every event as a tuple", func(t *testing.T) []matrix.Object {
			var kept []matrix.Object
			for _, event := range statePDUsOf(t, host, roomID) {
				kept = append(kept, trimmed(event))
			}
			return kept
		}},
	} {
		signed, eventID := handInvite(t, host, alice.UserID, bob.UserID, roomID, 3)
		_, err := host.peer().Put(t.Context(), guest.name,
			"/_matrix/federation/v2/invite/"+url.PathEscape(roomID)+"/"+url.PathEscape(eventID),
			matrix.Object{
				"event":             signed,
				"room_version":      "12",
				"invite_room_state": anyEvents(tc.state(t)),
			})
		if err == nil {
			t.Errorf("%s: %s accepted an invitation to %s whose state it cannot check", guest.name, tc.name, roomID)
			continue
		}
		if !strings.Contains(err.Error(), "M_MISSING_PARAM") {
			t.Errorf("%s: %s refused the invitation with %v, want M_MISSING_PARAM", guest.name, tc.name, err)
		}
	}

	// Refusing is refusing all of it: the invited server is left holding no room, not a room
	// named by an id it cannot check.
	if _, err := guest.db.RoomRepo.Get(roomID); err == nil {
		t.Errorf("%s holds a room row for %s it refused the invitation to", guest.name, roomID)
	}
}

// TestHashedRoomJoinHandshake is a room version 12 join driven over the wire by hand: the
// room's server proposes a shape, the joining server signs it, and the state that comes back
// carries the create event the room's name is hashed from.
func TestHashedRoomJoinHandshake(t *testing.T) {
	cluster := newCluster(t)
	host, guest := cluster.one(), cluster.two()
	alice := host.register("alice", "wonderland")
	bob := guest.register("bob", "builders")

	roomID := host.createRoom(alice, map[string]any{
		"preset":       "private_chat",
		"room_version": "12",
		"invite":       []any{bob.UserID},
	})

	// A room whose id names no server still has to be asked of somebody, and the version asked
	// for is the version the answer has to come back with.
	proposal, err := guest.peer().Get(t.Context(), host.name,
		"/_matrix/federation/v1/make_join/"+url.PathEscape(roomID)+"/"+url.PathEscape(bob.UserID),
		url.Values{"ver": []string{"12"}})
	if err != nil {
		t.Fatalf("%s could not ask %s to join %s: %v", guest.name, host.name, roomID, err)
	}
	if version, _ := proposal["room_version"].(string); version != "12" {
		t.Fatalf("%s proposes version %q for %s, want 12", host.name, version, roomID)
	}
	template, ok := proposal["event"].(matrix.Object)
	if !ok {
		t.Fatalf("the join proposal carries an event of %T", proposal["event"])
	}
	if got := federation.EventField(template, "room_id"); got != roomID {
		t.Errorf("the join proposal names room %q, want %q", got, roomID)
	}

	key, err := guest.svc.SigningKey(context.Background())
	if err != nil {
		t.Fatalf("the signing key of %s: %v", guest.name, err)
	}
	template["origin_server_ts"] = time.Now().UnixMilli()
	signed, err := eventcrypto.HashAndSign(template, "12", key.Private, guest.name, key.ID)
	if err != nil {
		t.Fatalf("%s could not sign the join: %v", guest.name, err)
	}
	eventID, err := eventcrypto.EventID(signed, "12")
	if err != nil {
		t.Fatalf("naming the join: %v", err)
	}

	answer, err := guest.peer().Put(t.Context(), host.name,
		"/_matrix/federation/v2/send_join/"+url.PathEscape(roomID)+"/"+url.PathEscape(eventID),
		matrix.Object{"event": signed})
	if err != nil {
		t.Fatalf("%s refused %s's join of %s: %v", host.name, guest.name, roomID, err)
	}

	// The state the room answers with is the room's graph, and in this version it has to hold
	// the create event: the joining server cannot name the room, let alone check its id,
	// without it.
	state := objectsOfAnswer(t, answer, "state")
	create := ""
	for _, event := range state {
		if federation.EventField(event, "type") != matrix.TypeCreate {
			continue
		}
		id, err := eventcrypto.EventID(event, "12")
		if err != nil {
			t.Errorf("the create event of %s cannot be hashed: %v", roomID, err)
			continue
		}
		create = id
	}
	if create != "$"+roomID[1:] {
		t.Errorf("%s answered the state of %s with create event %q, want the event the room id hashes from",
			host.name, roomID, create)
	}

	// The room's own server took the join and wrote it into the room: an event named by its
	// hash, in a room named by the hash of another event, is accepted by both servers on the
	// strength of the events they can show each other.
	stored, err := host.db.EventRepo.Get(eventID)
	if err != nil {
		t.Fatalf("%s never recorded the join event %s: %v", host.name, eventID, err)
	}
	if stored.RoomID != roomID {
		t.Errorf("%s recorded the join of %s under room %q", host.name, eventID, stored.RoomID)
	}
	if _, err := host.db.StateRepo.Get(roomID, matrix.TypeMember, bob.UserID); err != nil {
		t.Errorf("%s took the join of %s without recording the membership it names", host.name, bob.UserID)
	}
}

// anyEvents lifts a list of events into the shape a JSON body takes.
func anyEvents(events []matrix.Object) []any {
	out := make([]any, 0, len(events))
	for _, event := range events {
		out = append(out, event)
	}
	return out
}

// objectOfContent reads the content object of an event, or an empty one.
func objectOfContent(event matrix.Object) matrix.Object {
	if content, ok := event["content"].(matrix.Object); ok {
		return content
	}
	if content, ok := event["content"].(map[string]any); ok {
		return matrix.Object(content)
	}
	return matrix.Object{}
}
