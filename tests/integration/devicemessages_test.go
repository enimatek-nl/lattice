package integration

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestToDeviceMessagesReachOneDevice covers the route a Megolm session key takes: one device to another,
// with no room in between. It matters because an encrypted conversation is not built out of room events —
// the room carries the ciphertext — but out of these messages, so a server that stores them badly produces
// clients that sit in a room full of messages they cannot read.
func TestToDeviceMessagesReachOneDevice(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	aliceID, aliceToken, _ := h.register(t, "alice-td", "pw-alice-td", "")
	bobID, bobToken, bobDevice := h.register(t, "bob-td", "pw-bob-td", "")

	sendToDevice := func(t *testing.T, txnID string, body map[string]any) response {
		t.Helper()
		path := "/_matrix/client/v3/sendToDevice/m.room.encrypted/" + url.PathEscape(txnID)
		return h.raw(t, http.MethodPut, path, marshalBody(t, body), aliceToken)
	}

	// A named device gets the message, and the device is named because two devices of one user
	// read at different speeds: a key sent to one is not the other's to spend.
	res := sendToDevice(t, "txn-named", map[string]any{
		"messages": map[string]any{bobID: map[string]any{bobDevice: map[string]any{"session_id": "session-one"}}},
	})
	if res.status != http.StatusOK {
		t.Fatalf("sending to one device: %d %s", res.status, res.body)
	}

	waiting := toDeviceEvents(t, h, bobToken)
	if len(waiting) != 1 {
		t.Fatalf("bob's device was handed %d to-device messages, want 1: %v", len(waiting), waiting)
	}
	event := waiting[0]
	if event["type"] != "m.room.encrypted" {
		t.Errorf("the message arrived as type %v, want m.room.encrypted", event["type"])
	}
	if event["sender"] != aliceID {
		t.Errorf("the message arrived from %v, want %s: the payload is encrypted, so this is the only thing a recipient can see without opening it", event["sender"], aliceID)
	}
	content, _ := event["content"].(map[string]any)
	if content["session_id"] != "session-one" {
		t.Errorf("the content arrived as %v, want session-one unchanged", event["content"])
	}

	// Reading it once must not spend it for the same token, and must not deliver it again for a
	// later one. Both halves matter: a client that retries a sync would otherwise decrypt the same
	// key twice, and a client that moved on would wait forever for a message it never saw.
	next := toDeviceNextBatch(t, h, bobToken)
	if next == "" {
		t.Fatal("the sync that delivered a to-device message gave no position to resume from")
	}
	if again := toDeviceEventsSince(t, h, bobToken, next); len(again) != 0 {
		t.Errorf("resuming from %q delivered %d messages again: %v", next, len(again), again)
	}

	// A retried send with the same transaction ID is the same send. This is the difference between
	// a client seeing one key share and two, which is how a device ends up asked to decrypt a
	// session it has already opened.
	if res := sendToDevice(t, "txn-named", map[string]any{
		"messages": map[string]any{bobID: map[string]any{bobDevice: map[string]any{"session_id": "session-one"}}},
	}); res.status != http.StatusOK {
		t.Fatalf("retrying a to-device send: %d %s", res.status, res.body)
	}
	if duplicates := toDeviceEventsSince(t, h, bobToken, next); len(duplicates) != 0 {
		t.Errorf("a retried transaction delivered %d messages, want none: %v", len(duplicates), duplicates)
	}

	// `*` means every device the user has, expanded at send time. Each device keeps its own
	// position, so one row shared between them would be spent by whoever synced first.
	if res := sendToDevice(t, "txn-star", map[string]any{
		"messages": map[string]any{bobID: map[string]any{"*": map[string]any{"session_id": "session-two"}}},
	}); res.status != http.StatusOK {
		t.Fatalf("sending to every device: %d %s", res.status, res.body)
	}
	got := toDeviceEventsSince(t, h, bobToken, next)
	if len(got) != 1 {
		t.Fatalf("the star send reached %d messages on bob's device, want 1: %v", len(got), got)
	}

	// One request addressed to two devices has to reach both. This is the shape the specification's
	// transaction ID deduplicates *against*: a send is a set of messages, not one message, and a
	// retry key that ignores the recipient makes the second device look like a duplicate of the
	// first. Two fresh users are used because a first sync has no position to reason about.
	erinID, erinToken, erinDevice := h.register(t, "erin-td", "pw-erin-td", "")
	frankID, frankToken, frankDevice := h.register(t, "frank-td", "pw-frank-td", "")
	if res := sendToDevice(t, "txn-two-recipients", map[string]any{
		"messages": map[string]any{
			erinID:  map[string]any{erinDevice: map[string]any{"session_id": "shared-session"}},
			frankID: map[string]any{frankDevice: map[string]any{"session_id": "shared-session"}},
		},
	}); res.status != http.StatusOK {
		t.Fatalf("one send to two devices: %d %s", res.status, res.body)
	}
	for name, token := range map[string]string{"erin": erinToken, "frank": frankToken} {
		arrived := 0
		for _, event := range toDeviceEvents(t, h, token) {
			if content, _ := event["content"].(map[string]any); content["session_id"] == "shared-session" {
				arrived++
			}
		}
		if arrived != 1 {
			t.Errorf("%s received the shared session %d times, want exactly 1: one send to two devices has to reach both", name, arrived)
		}
	}

	// A send to a user who does not exist is accepted and files nothing. The alternative — a 404 —
	// would tell a client that a peer's localpart is wrong, which is a question this endpoint is not
	// specified to answer and a server should not be in the business of confirming.
	if res := sendToDevice(t, "txn-ghost", map[string]any{
		"messages": map[string]any{"@nobody-here:hs1": map[string]any{"AAA": map[string]any{}}},
	}); res.status != http.StatusOK {
		t.Errorf("sending to an unknown local user: %d %s, want 200", res.status, res.body)
	}

	// A type is refused only when it is not a type at all. Matrix event types are free-form
	// strings — `not-a-type` is a legal one, and a server that refused it would be refusing the
	// custom types that make the ecosystem extensible — so the line this server holds is that the
	// identifier has to be an identifier: no control characters, no spaces, nothing that would
	// break the next server that has to route on it.
	if res := h.raw(t, http.MethodPut,
		"/_matrix/client/v3/sendToDevice/"+url.QueryEscape("bad\ntype")+"/txn-bad",
		[]byte(`{"messages":{}}`), aliceToken); res.status != http.StatusBadRequest {
		t.Errorf("an event type with a control character in it returned %d (%s), want 400", res.status, res.body)
	}
}

// marshalBody encodes a request body for the raw request helper, which takes bytes so that a test can
// send something this server would never produce itself.
func marshalBody(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("a test cannot encode its own request: %v", err)
	}
	return body
}

// toDeviceEvents reads a fresh sync and returns the messages addressed to the syncing device.
func toDeviceEvents(t *testing.T, h *harness, token string) []map[string]any {
	t.Helper()
	body := h.mustStatus(t, h.get("/_matrix/client/v3/sync?timeout=0", token), http.StatusOK).decoded(t)
	section, _ := body["to_device"].(map[string]any)
	list, _ := section["events"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if event, ok := item.(map[string]any); ok {
			out = append(out, event)
		}
	}
	return out
}

// toDeviceNextBatch is the position the last delivery told the client to resume from.
func toDeviceNextBatch(t *testing.T, h *harness, token string) string {
	t.Helper()
	body := h.mustStatus(t, h.get("/_matrix/client/v3/sync?timeout=0", token), http.StatusOK).decoded(t)
	section, _ := body["to_device"].(map[string]any)
	next, _ := section["next_batch"].(string)
	return next
}

// toDeviceEventsSince resumes from a position and returns what arrives after it.
func toDeviceEventsSince(t *testing.T, h *harness, token, since string) []map[string]any {
	t.Helper()
	if since == "" {
		t.Fatal("resuming a to-device read without a position would re-read everything")
	}
	body := h.mustStatus(t, h.get("/_matrix/client/v3/sync?timeout=0&since="+url.QueryEscape(since), token), http.StatusOK).decoded(t)
	section, _ := body["to_device"].(map[string]any)
	list, _ := section["events"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if event, ok := item.(map[string]any); ok {
			out = append(out, event)
		}
	}
	return out
}
