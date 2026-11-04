package federation

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

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
