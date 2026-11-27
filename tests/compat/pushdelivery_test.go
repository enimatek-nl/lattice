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
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		var call gatewayCall
		found := len(g.calls) > 0
		if found {
			call = g.calls[0]
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

	server.Send(alice, roomID, "ring-1", "hello bob")

	call, ok := gateway.await(t, 5*time.Second)
	if !ok {
		t.Fatalf("the homeserver never knocked at the gateway for the message")
	}
	if call.path != "/_matrix/push/v1/notify" {
		t.Errorf("the request went to %s, not to the push gateway API", call.path)
	}
	body := notification(t, call)

	eventID, _ := body["event_id"].(string)
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
	if unread, _ := counts["unread"].(float64); unread != 1 {
		t.Errorf("counts.unread is %v, want 1 for the message that just arrived", counts["unread"])
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

	server.Send(alice, roomID, "full-1", "in full")

	call, ok := gateway.await(t, 5*time.Second)
	if !ok {
		t.Fatalf("the homeserver never knocked at the gateway")
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
	for _, ruleID := range []string{".m.rule.room_one_to_one", ".m.rule.message"} {
		server.MustDo(http.MethodPut,
			"/_matrix/client/v3/pushrules/global/underride/"+ruleID+"/enabled",
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
