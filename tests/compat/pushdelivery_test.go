package compat

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A push gateway the homeserver can be pointed at, and the tests of what a server owes
// it. These are the tests for the last step of the push pipeline: a client registers an
// endpoint with POST /pushers/set, and the specification says the homeserver then
// "starts sending HTTP requests to the Push Gateway using the supplied URL". Until that
// request exists, an app that is not open receives nothing at all.

// gatewayCall is one request the homeserver made.
type gatewayCall struct {
	path string
	body map[string]any
	raw  string
}

// pushGateway records what it was asked and answers how a test wants. A real gateway is
// Sygnal or the lab's own fake; both speak the same two fields back: a 2xx with a
// `rejected` list, or a 400 for a pushkey they will not take.
type pushGateway struct {
	server *httptest.Server
	mu     sync.Mutex
	status int
	answer string
	calls  []gatewayCall
}

func newPushGateway(t *testing.T) *pushGateway {
	t.Helper()
	gateway := &pushGateway{status: http.StatusOK, answer: `{"rejected":[]}`}
	gateway.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading the gateway request: %v", err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the homeserver sent the gateway unreadable JSON: %s", raw)
		}
		gateway.mu.Lock()
		gateway.calls = append(gateway.calls, gatewayCall{path: r.URL.Path, body: body, raw: string(raw)})
		status, answer := gateway.status, gateway.answer
		gateway.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(gateway.server.Close)
	return gateway
}

// url is the endpoint a pusher is registered with.
func (g *pushGateway) url() string { return g.server.URL + "/_matrix/push/v1/notify" }

// answerAs settles how the following requests are answered.
func (g *pushGateway) answerAs(status int, body string) {
	g.mu.Lock()
	g.status, g.answer = status, body
	g.mu.Unlock()
}

// count is how many requests arrived so far.
func (g *pushGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// await waits for a first request. Delivery is deliberately asynchronous, so a test that
// wants to assert the body has to wait for it; the timeout is the test saying how long
// "soon" is allowed to take.
func (g *pushGateway) await(t *testing.T, within time.Duration) (gatewayCall, bool) {
	t.Helper()
	return g.awaitWhere(t, within, func(map[string]any) bool { return true })
}

// awaitEvent waits for the knock about one particular event. A test about a message cannot
// take the first request it sees: an invitation is a knock of its own now, and it usually
// comes first, because it is what puts the pusher's owner in the room.
func (g *pushGateway) awaitEvent(t *testing.T, within time.Duration, eventID string) (gatewayCall, bool) {
	t.Helper()
	return g.awaitWhere(t, within, func(body map[string]any) bool {
		notification, _ := body["notification"].(map[string]any)
		return notification["event_id"] == eventID
	})
}

// awaitType is awaitEvent for a server-side fact a test cannot know the ID of, and it only
// works for a full pusher: `format: "event_id_only"` carries no type to look at.
func (g *pushGateway) awaitType(t *testing.T, within time.Duration, want string) (gatewayCall, bool) {
	t.Helper()
	return g.awaitWhere(t, within, func(body map[string]any) bool {
		notification, _ := body["notification"].(map[string]any)
		return notification["type"] == want
	})
}

func (g *pushGateway) awaitWhere(t *testing.T, within time.Duration,
	want func(map[string]any) bool) (gatewayCall, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		var call gatewayCall
		found := false
		for _, entry := range g.calls {
			if want(entry.body) {
				call, found = entry, true
				break
			}
		}
		g.mu.Unlock()
		if found {
			return call, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return gatewayCall{}, false
}

// registerPusher registers an http pusher whose gateway is the one under test. The
// `account` label in the pusher data is what the lab puts there, and what a gateway
// reads back out of the payload to tell two accounts of one app apart.
func registerPusher(t *testing.T, server *Server, session *Session, gatewayURL, format string) {
	t.Helper()
	data := map[string]any{"url": gatewayURL, "account": "repro-account"}
	if format != "" {
		data["format"] = format
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", session.Token, map[string]any{
		"kind":                "http",
		"app_id":              "dev.lattice.smaltino",
		"pushkey":             "repro-key",
		"app_display_name":    "Smaltino",
		"device_display_name": "Repro device",
		"lang":                "en",
		"data":                data,
	}, http.StatusOK)
}

// pushDeliveryTest is the shared setup: two users in a room of two, the delivery loop
// running, and bob's endpoint pointed at a gateway under test.
func pushDeliveryTest(t *testing.T) (*Server, *Session, *Session, *pushGateway) {
	t.Helper()
	server := NewServer(t)
	stop := server.svc.StartPushDelivery(t.Context())
	t.Cleanup(stop)

	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	gateway := newPushGateway(t)
	registerPusher(t, server, bob, gateway.url(), "event_id_only")
	return server, alice, bob, gateway
}

// twoPersonRoom is the room the specification's `.m.rule.room_one_to_one` is about: two
// members, so a message notifies with a sound.
func twoPersonRoom(t *testing.T, server *Server, alice, bob *Session) string {
	t.Helper()
	roomID := server.CreateRoom(alice, map[string]any{"invite": []string{bob.UserID}})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	return roomID
}

// notification reads the object the gateway was sent.
func notification(t *testing.T, call gatewayCall) map[string]any {
	t.Helper()
	body, ok := call.body["notification"].(map[string]any)
	if !ok {
		t.Fatalf("the gateway was sent no notification object: %s", call.raw)
	}
	return body
}

// TestPushGatewayIsKnockedAt is the report of 2026-11-26: pushers were stored and never
// asked, so `a_pusher_has_the_homeserver_knock_at_the_gateway` failed with "the homeserver
// never knocked at the gateway for the message" and no request arrived within half a
// minute.
func TestPushGatewayIsKnockedAt(t *testing.T) {
	server, alice, bob, gateway := pushDeliveryTest(t)
	roomID := twoPersonRoom(t, server, alice, bob)
	eventID := server.Send(alice, roomID, "ring-1", "hello bob")

	call, ok := gateway.awaitEvent(t, 5*time.Second, eventID)
	if !ok {
		t.Fatalf("the homeserver never knocked at the gateway for the message")
	}
	if call.path != "/_matrix/push/v1/notify" {
		t.Errorf("the request went to %s, not to the push gateway API", call.path)
	}
	body := notification(t, call)

	eventID, _ = body["event_id"].(string)
	if !strings.HasPrefix(eventID, "$") {
		t.Errorf("event_id is %q, which is not an event id", eventID)
	}
	if room, _ := body["room_id"].(string); room != roomID {
		t.Errorf("room_id is %q, want %s", room, roomID)
	}
	// The pusher was registered with format event_id_only, and the lab's gateway checks
	// exactly this: the app fetches the event itself, so the payload stops here.
	for _, field := range []string{"content", "sender", "type"} {
		if _, present := body[field]; present {
			t.Errorf("an event_id_only pusher was sent %s: %s", field, call.raw)
		}
	}

	devices, ok := body["devices"].([]any)
	if !ok || len(devices) != 1 {
		t.Fatalf("the gateway needs exactly one device, got %v", body["devices"])
	}
	device, _ := devices[0].(map[string]any)
	if key, _ := device["pushkey"].(string); key != "repro-key" {
		t.Errorf("devices[0].pushkey is %q", key)
	}
	if app, _ := device["app_id"].(string); app != "dev.lattice.smaltino" {
		t.Errorf("devices[0].app_id is %q", app)
	}
	if ts, _ := device["pushkey_ts"].(float64); ts <= 0 {
		t.Errorf("devices[0].pushkey_ts is %v, which is not a registration time", device["pushkey_ts"])
	}
	data, _ := device["data"].(map[string]any)
	if label, _ := data["account"].(string); label != "repro-account" {
		t.Errorf("the pusher's own data did not travel along: %v", device["data"])
	}
	// A room of two notifies with a sound, and the sound is the tweak a phone reads to
	// decide whether to wake up for this.
	tweaks, _ := device["tweaks"].([]any)
	if len(tweaks) == 0 {
		t.Fatalf("the matched rule set no tweaks, so the app would stay silent: %s", call.raw)
	}
	if prio, _ := body["prio"].(string); prio != "high" {
		t.Errorf("prio is %q; a notification with a sound is high priority", prio)
	}
	counts, ok := body["counts"].(map[string]any)
	if !ok {
		t.Fatalf("no counts, so the app has no badge to show: %s", call.raw)
	}
	// Two: this room's invitation is a notification too, and the app has acknowledged
	// neither. The number the gateway is sent is "the number of unacknowledged
	// notifications", not the number of messages.
	if unread, _ := counts["unread"].(float64); unread != 2 {
		t.Errorf("counts.unread is %v, want 2", counts["unread"])
	}
}

// TestPushGatewayIsNotKnockedAtForTheSender checks the other half of the same sentence:
// rules are processed "for each of the local users in the room (excluding the sender)".
func TestPushGatewayIsNotKnockedAtForTheSender(t *testing.T) {
	server, alice, bob, gateway := pushDeliveryTest(t)
	own := newPushGateway(t)
	registerPusher(t, server, alice, own.url(), "event_id_only")
	roomID := twoPersonRoom(t, server, alice, bob)

	server.Send(alice, roomID, "self-1", "to the room")

	if _, ok := gateway.await(t, 3*time.Second); !ok {
		t.Fatalf("bob was not notified of a message in a room he is in")
	}
	if got := own.count(); got != 0 {
		t.Errorf("alice was pushed her own message %d time(s)", got)
	}
}

// TestPushGatewaySeesTheEventForAFullPusher covers the other format: without
// `format: "event_id_only"` the specification wants the event itself in the request.
func TestPushGatewaySeesTheEventForAFullPusher(t *testing.T) {
	server := NewServer(t)
	stop := server.svc.StartPushDelivery(t.Context())
	t.Cleanup(stop)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	gateway := newPushGateway(t)
	registerPusher(t, server, bob, gateway.url(), "")
	roomID := twoPersonRoom(t, server, alice, bob)

	eventID := server.Send(alice, roomID, "full-1", "in full")

	call, ok := gateway.awaitEvent(t, 5*time.Second, eventID)
	if !ok {
		t.Fatalf("the homeserver never knocked at the gateway for %s", eventID)
	}
	body := notification(t, call)
	if sender, _ := body["sender"].(string); sender != alice.UserID {
		t.Errorf("sender is %q, want %s", sender, alice.UserID)
	}
	if kind, _ := body["type"].(string); kind != "m.room.message" {
		t.Errorf("type is %q", kind)
	}
	content, ok := body["content"].(map[string]any)
	if !ok {
		t.Fatalf("no content for a pusher that asked for the event: %s", call.raw)
	}
	if text, _ := content["body"].(string); text != "in full" {
		t.Errorf("content.body is %q", text)
	}
}

// TestPushGatewayAnswerIsActedOn covers the two answers a gateway gives about a pushkey,
// both of which mean: stop registering for this device. Without this a pusher for a
// device that was uninstalled is kept forever, and every message queues a request that
// is refused again.
func TestPushGatewayAnswerIsActedOn(t *testing.T) {
	for _, step := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "rejected pushkey", status: http.StatusOK, body: `{"rejected":["repro-key"]}`},
		{name: "invalid pushkey", status: http.StatusBadRequest, body: `{"errcode":"M_INVALID_PARAM","error":"pushkey is not accepted"}`},
	} {
		t.Run(step.name, func(t *testing.T) {
			server, alice, bob, gateway := pushDeliveryTest(t)
			gateway.answerAs(step.status, step.body)
			roomID := twoPersonRoom(t, server, alice, bob)

			server.Send(alice, roomID, "reject-1", "gone soon")

			if _, ok := gateway.await(t, 5*time.Second); !ok {
				t.Fatalf("the homeserver never knocked at the gateway")
			}
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				listing := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", bob.Token, nil, http.StatusOK)
				var answer struct {
					Pushers []matrix.Object `json:"pushers"`
				}
				if err := json.Unmarshal(listing.Raw, &answer); err != nil {
					t.Fatalf("reading the pusher list: %v", err)
				}
				if len(answer.Pushers) == 0 {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Errorf("a gateway that answered %d kept the pusher alive", step.status)
		})
	}
}

// TestPushGatewayIsRetried gives a gateway one bad minute: a 500 is worth another
// attempt, and a notification that arrives late still arrives.
func TestPushGatewayIsRetried(t *testing.T) {
	server, alice, bob, gateway := pushDeliveryTest(t)
	gateway.answerAs(http.StatusServiceUnavailable, `{"errcode":"M_UNKNOWN","error":"down for tea"}`)
	roomID := twoPersonRoom(t, server, alice, bob)

	server.Send(alice, roomID, "retry-1", "still coming")

	// The first retry waits the floor of the backoff, two seconds; the budget below is
	// that plus room for a slow machine.
	if _, ok := gateway.await(t, 5*time.Second); !ok {
		t.Fatalf("the first attempt never reached the gateway")
	}
	gateway.answerAs(http.StatusOK, `{"rejected":[]}`)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if gateway.count() >= 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("a gateway that answered 503 was tried %d time(s) and never again", gateway.count())
}

// TestPushRulesStillDecide is the boundary that must not move: delivery follows the
// rules, so a person who switched notifications off is not woken by a queue that wants
// to be busy.
func TestPushRulesStillDecide(t *testing.T) {
	server, alice, bob, gateway := pushDeliveryTest(t)
	for _, rule := range []struct{ kind, id string }{
		{"underride", ".m.rule.room_one_to_one"},
		{"underride", ".m.rule.message"},
		// The invitation of the room that is about to be made is itself a knock, and this
		// test wants a silent recipient, so the invitation's rule goes off too - which is
		// also the proof of which rule was behind that knock.
		{"override", ".m.rule.invite_for_me"},
	} {
		server.MustDo(http.MethodPut,
			"/_matrix/client/v3/pushrules/global/"+rule.kind+"/"+rule.id+"/enabled",
			bob.Token, map[string]any{"enabled": false}, http.StatusOK)
	}
	roomID := twoPersonRoom(t, server, alice, bob)

	server.Send(alice, roomID, "silent-1", "quiet please")
	// A message that arrives while the loop is busy would be a bug of a different kind,
	// so this waits out a window rather than asserting an immediate zero.
	time.Sleep(500 * time.Millisecond)
	if got := gateway.count(); got != 0 {
		t.Errorf("a recipient with the rules switched off was pushed %d time(s)", got)
	}
}

// TestEmailPushersAreNotKnockedAt: the kind is stored, and an email pusher wants a mail,
// which this server does not send. It must not become a gateway request.
func TestEmailPushersAreNotKnockedAt(t *testing.T) {
	server, alice, bob, gateway := pushDeliveryTest(t)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", bob.Token, map[string]any{
		"kind":                "email",
		"app_id":              "dev.lattice.mail",
		"pushkey":             "mail-key",
		"app_display_name":    "Mail",
		"device_display_name": "Inbox",
		"lang":                "en",
		"data":                map[string]any{"url": gateway.url(), "format": "event_id_only"},
	}, http.StatusOK)
	roomID := twoPersonRoom(t, server, alice, bob)

	server.Send(alice, roomID, "mail-1", "post, not push")

	call, ok := gateway.await(t, 5*time.Second)
	if !ok {
		t.Fatalf("the http pusher was not knocked at either")
	}
	body := notification(t, call)
	devices, _ := body["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("the request carried %d devices: an email pusher is not a gateway device", len(devices))
	}
	if key, _ := devices[0].(map[string]any)["pushkey"].(string); key != "repro-key" {
		t.Errorf("the device was %v, not the http pusher", devices[0])
	}
}

// TestPushGatewayNamesTheRoomAndTheSender covers the sentence the first version of this
// loop missed: "the homeserver is expected to include all of the event-related fields in
// the /notify request". A backgrounded app has no room to read a name out of, so a
// notification without `room_name` and `sender_display_name` is a banner that says
// "Message". The lab asked for exactly these two on the same run.
func TestPushGatewayNamesTheRoomAndTheSender(t *testing.T) {
	server := NewServer(t)
	stop := server.svc.StartPushDelivery(t.Context())
	t.Cleanup(stop)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	server.MustDo(http.MethodPut, profilePath(alice.UserID, "displayname"), alice.Token,
		map[string]any{"displayname": "Major Tom"}, http.StatusOK)

	gateway := newPushGateway(t)
	registerPusher(t, server, bob, gateway.url(), "")
	roomID := server.CreateRoom(alice, map[string]any{"name": "Mission Control", "invite": []string{bob.UserID}})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	eventID := server.Send(alice, roomID, "named-1", "the written word")

	call, ok := gateway.awaitEvent(t, 5*time.Second, eventID)
	if !ok {
		t.Fatalf("the homeserver never knocked at the gateway for %s", eventID)
	}
	body := notification(t, call)
	if name, _ := body["room_name"].(string); name != "Mission Control" {
		t.Errorf("room_name is %v, want the name the room carries", body["room_name"])
	}
	if name, _ := body["sender_display_name"].(string); name != "Major Tom" {
		t.Errorf("sender_display_name is %v", body["sender_display_name"])
	}
	if target, present := body["user_is_target"]; present {
		t.Errorf("a message names no target, so user_is_target should be absent, got %v", target)
	}
	// The gateway device carries the pusher's data "minus the url key": the URL is where
	// this server knocked, and a payload that continues on to a push provider has no
	// business carrying an internal address.
	devices, _ := body["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("one device expected, got %v", body["devices"])
	}
	data, _ := devices[0].(map[string]any)["data"].(map[string]any)
	if _, present := data["url"]; present {
		t.Errorf("the pusher's gateway URL travelled inside the payload: %v", data)
	}
	if label, _ := data["account"].(string); label != "repro-account" {
		t.Errorf("the rest of the pusher data should still travel along: %v", data)
	}
}

// TestPushGatewayCountsOmitZero is the other half of the same table: "counts whose value is
// zero should be omitted".
func TestPushGatewayCountsOmitZero(t *testing.T) {
	server, alice, bob, gateway := pushDeliveryTest(t)
	roomID := twoPersonRoom(t, server, alice, bob)
	eventID := server.Send(alice, roomID, "count-1", "one unread")

	call, ok := gateway.awaitEvent(t, 5*time.Second, eventID)
	if !ok {
		t.Fatalf("the homeserver never knocked at the gateway")
	}
	counts, ok := notification(t, call)["counts"].(map[string]any)
	if !ok {
		t.Fatalf("no counts, so the app has no badge to show: %s", call.raw)
	}
	if _, present := counts["highlight"]; present {
		t.Errorf("a highlight count of zero was sent: %v", counts)
	}
	// Two, and that is right: the invitation of this room is a notification the app has not
	// acknowledged either. "The number of unacknowledged notifications" counts it.
	if unread, _ := counts["unread"].(float64); unread != 2 {
		t.Errorf("counts.unread is %v, want 2", counts["unread"])
	}
}

// TestPushGatewayIsKnockedAtForAnInvite is the second half of the report of 2026-11-27: an
// invitation never reached a gateway, so `a_browser_s_pusher_carries_its_subscription_and_the_names`
// failed with "the homeserver never knocked at the gateway for m.room.member".
//
// The rule that asks for it, `.m.rule.invite_for_me`, "matches any invites to a new room for
// this user" - and the person it invites is by definition not in the room yet. A server that
// asks only "each of the local users in the room" therefore has nobody left to ask, and an
// invitation for an app that is not open is exactly what a push is for.
func TestPushGatewayIsKnockedAtForAnInvite(t *testing.T) {
	server := NewServer(t)
	stop := server.svc.StartPushDelivery(t.Context())
	t.Cleanup(stop)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	server.MustDo(http.MethodPut, profilePath(alice.UserID, "displayname"), alice.Token,
		map[string]any{"displayname": "Major Tom"}, http.StatusOK)

	gateway := newPushGateway(t)
	// No format, so the whole notification is asked for: this is the browser pusher of the
	// lab's test, and the one that has to carry the room's name.
	registerPusher(t, server, bob, gateway.url(), "")

	// Bob is deliberately left outside the room: the invitation is what is being delivered.
	server.CreateRoom(alice, map[string]any{"name": "Kiln", "invite": []string{bob.UserID}})

	call, ok := gateway.awaitType(t, 5*time.Second, matrix.TypeMember)
	if !ok {
		t.Fatalf("the homeserver never knocked at the gateway for the invitation: %s", call.raw)
	}
	body := notification(t, call)
	if kind, _ := body["type"].(string); kind != matrix.TypeMember {
		t.Errorf("type is %v, want %s", body["type"], matrix.TypeMember)
	}
	if target, _ := body["user_is_target"].(bool); !target {
		// "This is true if the user receiving the notification is the subject of a member
		// event": it is how an app tells an invite for itself from a change it only watches.
		t.Errorf("user_is_target is %v, want true: %s", body["user_is_target"], call.raw)
	}
	content, _ := body["content"].(map[string]any)
	if membership, _ := content["membership"].(string); membership != "invite" {
		t.Errorf("content.membership is %v, want invite: %v", content["membership"], content)
	}
	if name, _ := body["room_name"].(string); name != "Kiln" {
		t.Errorf("room_name is %v: an invited app has no room to read a name out of", body["room_name"])
	}
	if sender, _ := body["sender"].(string); sender != alice.UserID {
		t.Errorf("sender is %v, want %s", body["sender"], alice.UserID)
	}
	if name, _ := body["sender_display_name"].(string); name != "Major Tom" {
		t.Errorf("sender_display_name is %v", body["sender_display_name"])
	}

	// The rule gives `notify` and a sound, and nothing else: v1.11 lists no highlight tweak
	// for `.m.rule.invite_for_me`, so an invitation belongs to the room list rather than to
	// the red badge, which is what /notifications?only=highlight reports.
	answer := server.Do(http.MethodGet, "/_matrix/client/v3/notifications", bob.Token, nil)
	if list, _ := answer.Body["notifications"].([]any); len(list) != 1 {
		t.Fatalf("the invited user should hold one notification, got %v", answer.Body["notifications"])
	}
	highlighted := server.Do(http.MethodGet,
		"/_matrix/client/v3/notifications?only=highlight", bob.Token, nil)
	if list, _ := highlighted.Body["notifications"].([]any); len(list) != 0 {
		t.Errorf("an invitation was stored as a highlight, though v1.11 gives the rule no "+
			"highlight tweak: %v", highlighted.Body["notifications"])
	}
}

// TestPushGatewayIsQuietForAMembershipAboutSomebodyElse keeps the door opened above from
// opening too far. Membership events are evaluated now, so a room's other members see them
// too, and the default rules answer them with `.m.rule.member_event`, whose action list is
// empty. A join that woke every device in a room is exactly the noise the specification asks
// silence on.
func TestPushGatewayIsQuietForAMembershipAboutSomebodyElse(t *testing.T) {
	server, alice, bob, gateway := pushDeliveryTest(t)
	roomID := twoPersonRoom(t, server, alice, bob)

	// One message first: it proves the gateway is reachable and gives a count to compare with.
	eventID := server.Send(alice, roomID, "quiet-1", "one message")
	if _, ok := gateway.awaitEvent(t, 5*time.Second, eventID); !ok {
		t.Fatalf("a message in a room of two did not knock at the gateway")
	}
	before := gateway.count()

	// A member event about alice, watched by bob: she leaves the room they share.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/leave", alice.Token,
		map[string]any{}, http.StatusOK)
	// Delivery is asynchronous, so a quiet gateway has to be given its moment before being
	// called quiet; the message above showed that a knock that is coming arrives in seconds.
	time.Sleep(500 * time.Millisecond)
	if got := gateway.count(); got != before {
		t.Errorf("a leave knocked at the gateway %d time(s): the default rules suppress a "+
			"membership event about somebody else", got-before)
	}
}
