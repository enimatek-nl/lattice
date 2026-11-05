package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// syncNow performs a sync with no timeout and decodes it.
func syncNow(t *testing.T, h *harness, token, query string) (map[string]any, string) {
	t.Helper()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/sync?timeout=0"+query, token), http.StatusOK)
	body := res.decoded(t)
	next, _ := body["next_batch"].(string)
	if next == "" {
		t.Fatalf("sync response has no next_batch: %v", body)
	}
	return body, next
}

// roomSection reads rooms.<section>.<roomID> out of a sync response.
func roomSection(t *testing.T, body map[string]any, section, roomID string) map[string]any {
	t.Helper()
	rooms, _ := body["rooms"].(map[string]any)
	sectionMap, _ := rooms[section].(map[string]any)
	room, _ := sectionMap[roomID].(map[string]any)
	return room
}

// eventTypes lists the "type" of every event in an events list.
func eventTypes(t *testing.T, room map[string]any, field string) []string {
	t.Helper()
	list, _ := room[field].(map[string]any)
	raw, _ := list["events"].([]any)
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		event, _ := entry.(map[string]any)
		kind, _ := event["type"].(string)
		out = append(out, kind)
	}
	return out
}

// TestSyncInitialThenIncremental covers the two things a client depends on: a first sync
// describes the rooms it is in, and an incremental sync reports only what changed.
func TestSyncInitialThenIncremental(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "sara", "pw-sara", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"name": "planning"})

	body, next := syncNow(t, h, aliceToken, "")
	room := roomSection(t, body, "join", roomID)
	if room == nil {
		t.Fatalf("the joined room is missing from the first sync: %v", body["rooms"])
	}
	timeline := room["timeline"].(map[string]any)
	if _, ok := timeline["limited"]; !ok {
		t.Errorf("timeline has no limited flag: %v", timeline)
	}
	if timeline["prev_batch"] == nil {
		t.Errorf("timeline has no prev_batch token: %v", timeline)
	}
	types := eventTypes(t, room, "timeline")
	if !containsString(types, "m.room.create") {
		t.Errorf("timeline types = %v, want the creation event", types)
	}
	if _, ok := room["state"].(map[string]any); !ok {
		t.Errorf("joined room has no state object: %v", room)
	}
	for _, field := range []string{"ephemeral", "account_data"} {
		list, ok := room[field].(map[string]any)
		if !ok {
			t.Errorf("joined room has no %s object", field)
			continue
		}
		if _, ok := list["events"].([]any); !ok {
			t.Errorf("%s.events is not an array: %v", field, list)
		}
	}
	if unread, ok := room["unread_notifications"].(map[string]any); !ok {
		t.Errorf("joined room has no unread_notifications: %v", room)
	} else if unread["notification_count"] != float64(0) {
		t.Errorf("notification_count = %v, want 0", unread["notification_count"])
	}

	// Nothing happened, so nothing is reported. A server that repeats every room on
	// every sync makes clients re-read rooms they already have.
	body, _ = syncNow(t, h, aliceToken, "&since="+next)
	if again := roomSection(t, body, "join", roomID); again != nil {
		t.Errorf("an unchanged room appeared in an incremental sync: %v", again)
	}

	// A new message now does appear, and only that message.
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "sync-1", `{"msgtype":"m.text","body":"hello"}`, aliceToken), http.StatusOK)
	body, _ = syncNow(t, h, aliceToken, "&since="+next)
	room = roomSection(t, body, "join", roomID)
	if room == nil {
		t.Fatalf("the room is missing after a message was sent: %v", body["rooms"])
	}
	types = eventTypes(t, room, "timeline")
	if len(types) != 1 || types[0] != "m.room.message" {
		t.Errorf("timeline types = %v, want exactly [m.room.message]", types)
	}
	if timeline := room["timeline"].(map[string]any); timeline["limited"] != false {
		t.Errorf("a complete window should not be limited: %v", timeline)
	}
}

// TestSyncSeesAnotherUserJoin checks that a room the client is in reports the membership
// changes of other users, which is how a client learns who is present.
func TestSyncSeesAnotherUserJoin(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "toby", "pw-toby", "")
	_, bobToken, _ := h.register(t, "ursula", "pw-ursula", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	_, next := syncNow(t, h, aliceToken, "")
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)

	body, _ := syncNow(t, h, aliceToken, "&since="+next)
	room := roomSection(t, body, "join", roomID)
	if room == nil {
		t.Fatalf("the room is missing after someone joined: %v", body["rooms"])
	}
	found := false
	list := room["timeline"].(map[string]any)["events"].([]any)
	for _, entry := range list {
		event := entry.(map[string]any)
		if event["type"] != "m.room.member" {
			continue
		}
		content, _ := event["content"].(map[string]any)
		if event["state_key"] == "@ursula:hs1" && content["membership"] == "join" {
			found = true
		}
	}
	if !found {
		t.Errorf("no join event for the other user in the timeline: %v", list)
	}
}

// TestSyncInviteSection checks what an invited user sees: the room under `invite`, with
// just enough of the room's state to show what they are being invited to, and not the
// room's member list.
func TestSyncInviteSection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "vera", "pw-vera", "")
	bob, bobToken, _ := h.register(t, "wendy", "pw-wendy", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"name": "private notes"})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": bob}, aliceToken), http.StatusOK)

	body, _ := syncNow(t, h, bobToken, "")
	room := roomSection(t, body, "invite", roomID)
	if room == nil {
		t.Fatalf("the invitation is missing from bob's sync: %v", body["rooms"])
	}
	inviteState, _ := room["invite_state"].(map[string]any)
	events, _ := inviteState["events"].([]any)
	seen := map[string]bool{}
	var membership string
	for _, entry := range events {
		event := entry.(map[string]any)
		kind, _ := event["type"].(string)
		seen[kind] = true
		if kind == "m.room.member" {
			if event["state_key"] != bob {
				t.Errorf("the invitation carries a member event for someone else: %v", event)
			}
			content, _ := event["content"].(map[string]any)
			membership, _ = content["membership"].(string)
		}
		// Stripped state must not carry the server-internal graph fields.
		for _, forbidden := range []string{"auth_events", "prev_events", "depth", "origin", "signatures", "hashes"} {
			if _, present := event[forbidden]; present {
				t.Errorf("stripped state still carries %q: %v", forbidden, event)
			}
		}
	}
	if membership != "invite" {
		t.Errorf("the member event says membership %q, want invite", membership)
	}
	for _, want := range []string{"m.room.create", "m.room.join_rules", "m.room.member", "m.room.name"} {
		if !seen[want] {
			t.Errorf("invite_state is missing %s; got types %v", want, keysOf(seen))
		}
	}
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	return out
}

// TestSyncLeaveSection checks that leaving a room reports it once, under `leave`, and does
// not keep reporting it.
func TestSyncLeaveSection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "ivy", "pw-ivy", "")
	_, bobToken, _ := h.register(t, "quinn3", "pw-quinn3", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	_, next := syncNow(t, h, bobToken, "")
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)

	body, next := syncNow(t, h, bobToken, "&since="+next)
	if roomSection(t, body, "leave", roomID) == nil {
		t.Fatalf("the left room is not in the leave section: %v", body["rooms"])
	}
	if roomSection(t, body, "join", roomID) != nil {
		t.Errorf("a room the user left is still in the join section: %v", body["rooms"])
	}

	// A later incremental sync must not repeat the leave, and must not leak the room's
	// later activity to someone who is no longer in it.
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "after-leave",
		`{"msgtype":"m.text","body":"between us"}`, aliceToken), http.StatusOK)
	body, _ = syncNow(t, h, bobToken, "&since="+next)
	if again := roomSection(t, body, "leave", roomID); again != nil {
		t.Errorf("an old leave was reported again, with %v", again["timeline"])
	}
}

// TestSyncLeftRoomStateFollowsHistoryVisibility checks the other half of the rule: a room
// that restricts its history to members shows a former member no state at all, while the
// leave event itself still appears. Otherwise leaving a room would be invisible to the
// client that left it.
func TestSyncLeftRoomStateFollowsHistoryVisibility(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "yara", "pw-yara", "")
	_, bobToken, _ := h.register(t, "zim", "pw-zim", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{
		"preset": "public_chat",
		"initial_state": []map[string]any{
			{"type": "m.room.history_visibility", "state_key": "",
				"content": map[string]any{"history_visibility": "joined"}},
		},
	})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	_, next := syncNow(t, h, aliceToken, "")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, aliceToken), http.StatusOK)

	body, _ := syncNow(t, h, aliceToken, "&since="+next)
	room := roomSection(t, body, "leave", roomID)
	if room == nil {
		t.Fatalf("the left room is missing from alice's sync: %v", body["rooms"])
	}
	if state := eventTypes(t, room, "state"); len(state) != 0 {
		t.Errorf("a room restricted to members should show a former member no state, got %v", state)
	}
	seen := false
	for _, entry := range room["timeline"].(map[string]any)["events"].([]any) {
		event := entry.(map[string]any)
		if event["type"] == "m.room.member" && event["state_key"] == alice {
			seen = true
		}
	}
	if !seen {
		t.Errorf("the leave event must stay visible to the member who left: %v", room["timeline"])
	}
}

// TestSyncFilters covers storing a filter, reading it back, and having it shape a sync.
func TestSyncFilters(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	alice, aliceToken, _ := h.register(t, "yuri", "pw-yuri", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	for i := 0; i < 3; i++ {
		h.mustStatus(t, send(t, h, roomID, "m.room.message", fmt.Sprintf("f-%d", i),
			`{"msgtype":"m.text","body":"msg"}`, aliceToken), http.StatusOK)
	}

	filter := `{"room":{"timeline":{"limit":2,"types":["m.room.message"]},"state":{"types":[]}}}`
	created := h.mustStatus(t, h.raw(t, http.MethodPost, "/_matrix/client/v3/user/"+alice+"/filter",
		[]byte(filter), aliceToken), http.StatusOK).decoded(t)
	filterID, _ := created["filter_id"].(string)
	if filterID == "" {
		t.Fatalf("no filter_id in %v", created)
	}

	stored := h.mustStatus(t, h.get("/_matrix/client/v3/user/"+alice+"/filter/"+filterID, aliceToken), http.StatusOK)
	var roundTrip map[string]any
	if err := json.Unmarshal(stored.body, &roundTrip); err != nil {
		t.Fatalf("the stored filter does not parse: %v", err)
	}
	room, _ := roundTrip["room"].(map[string]any)
	if _, ok := room["timeline"].(map[string]any); !ok {
		t.Errorf("the stored filter lost its timeline section: %v", roundTrip)
	}

	body, _ := syncNow(t, h, aliceToken, "&filter="+filterID)
	joined := roomSection(t, body, "join", roomID)
	if joined == nil {
		t.Fatalf("no joined room with filter %s: %v", filterID, body["rooms"])
	}
	types := eventTypes(t, joined, "timeline")
	if len(types) > 2 {
		t.Errorf("the timeline limit was ignored: %v", types)
	}
	for _, kind := range types {
		if kind != "m.room.message" {
			t.Errorf("the timeline types filter was ignored: %v", types)
		}
	}
	if timeline := joined["timeline"].(map[string]any); timeline["limited"] != true {
		t.Errorf("dropping events for the filter and limit must report limited: %v", timeline)
	}
	if state := eventTypes(t, joined, "state"); len(state) != 0 {
		t.Errorf(`"state": {"types": []} should return no state events, got %v`, state)
	}

	// A timeline limit of zero means the client wants no timeline events at all — it
	// came for the state. Treating zero as "the default" would ignore what it asked for.
	zeroFilter := h.mustStatus(t, h.raw(t, http.MethodPost, "/_matrix/client/v3/user/"+alice+"/filter",
		[]byte(`{"room":{"timeline":{"limit":0}}}`), aliceToken), http.StatusOK).decoded(t)
	zeroID, _ := zeroFilter["filter_id"].(string)
	body, _ = syncNow(t, h, aliceToken, "&filter="+zeroID)
	joined = roomSection(t, body, "join", roomID)
	if joined == nil {
		t.Fatalf("no joined room with the zero-limit filter: %v", body["rooms"])
	}
	if events := eventTypes(t, joined, "timeline"); len(events) != 0 {
		t.Errorf("a limit of 0 should return no timeline events, got %v", events)
	}
	if eventTypes(t, joined, "state"); len(eventTypes(t, joined, "state")) == 0 {
		t.Errorf("the zero-limit filter should still return the room's state: %v", joined["state"])
	}

	// An inline filter is the other spelling the specification allows.
	body, _ = syncNow(t, h, aliceToken, "&filter=%7B%22room%22%3A%7B%22timeline%22%3A%7B%22limit%22%3A1%7D%7D%7D")
	joined = roomSection(t, body, "join", roomID)
	if joined == nil || len(eventTypes(t, joined, "timeline")) != 1 {
		t.Errorf("an inline filter was not applied: %v", body["rooms"])
	}

	// A filter ID that does not exist is a bad request, not "no filter": pretending
	// otherwise would send more than the client asked for.
	res := h.get("/_matrix/client/v3/sync?timeout=0&filter=999999", aliceToken)
	if res.status != http.StatusBadRequest {
		t.Errorf("unknown filter id: status %d, want 400 (body %s)", res.status, res.body)
	}

	// Someone else's filters are not readable.
	other, otherToken, _ := h.register(t, "zoe2", "pw-zoe2", "")
	// Asking for another user's filter ID under one's own name answers 404 rather than
	// 403: the filter is scoped to its owner, so this neither hands over the filter nor
	// confirms that the ID belongs to someone.
	if res := h.get("/_matrix/client/v3/user/"+other+"/filter/"+filterID, otherToken); res.status != http.StatusNotFound {
		t.Errorf("guessing another user's filter id: status %d, want 404 (body %s)", res.status, res.body)
	}
	if res := h.get("/_matrix/client/v3/user/"+alice+"/filter/"+filterID, otherToken); res.status != http.StatusForbidden {
		t.Errorf("naming another user in the path: status %d, want 403 (body %s)", res.status, res.body)
	}
}

// TestSyncTokenErrors pins the answer to a token this server did not issue.
func TestSyncTokenErrors(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, token, _ := h.register(t, "adam2", "pw-adam2", "")
	for _, bogus := range []string{"0", "not-a-token", "s-1", "s999999999999999999999"} {
		res := h.get("/_matrix/client/v3/sync?timeout=0&since="+bogus, token)
		if res.status != http.StatusUnauthorized {
			t.Errorf("since=%q: status %d, want 401 (body %s)", bogus, res.status, res.body)
			continue
		}
		if code := res.errcode(t); code != "M_INVALID_TOKEN" {
			t.Errorf("since=%q: errcode %q, want M_INVALID_TOKEN", bogus, code)
		}
	}
}

// TestSyncLongPoll checks that a waiting sync comes back on its own, with no rooms and a
// token that can be used again.
func TestSyncLongPoll(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "poller", "pw-poller", "")
	createRoom(t, h, aliceToken, map[string]any{})
	_, next := syncNow(t, h, aliceToken, "")

	started := time.Now()
	res := h.mustStatus(t, h.get("/_matrix/client/v3/sync?timeout=1200&since="+next, aliceToken), http.StatusOK)
	elapsed := time.Since(started)
	if elapsed < 500*time.Millisecond {
		t.Errorf("a sync with nothing to report returned after %v; the client asked to wait", elapsed)
	}
	body := res.decoded(t)
	if _, present := body["rooms"].(map[string]any)["join"]; present {
		t.Errorf("an empty sync should not carry joined rooms: %v", body["rooms"])
	}
	if _, ok := body["next_batch"].(string); !ok {
		t.Errorf("no next_batch: %v", body)
	}

	// A bad timeout is the client's mistake.
	if res := h.get("/_matrix/client/v3/sync?timeout=-5", aliceToken); res.status != http.StatusBadRequest {
		t.Errorf("negative timeout: status %d, want 400 (body %s)", res.status, res.body)
	}
}

// TestSyncFullState checks `full_state`: every joined room comes back even when nothing
// changed, which is what a client does after losing its local copy.
func TestSyncFullState(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "cleo", "pw-cleo", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"name": "kept"})
	_, next := syncNow(t, h, aliceToken, "")

	body, _ := syncNow(t, h, aliceToken, "&since="+next+"&full_state=true")
	room := roomSection(t, body, "join", roomID)
	if room == nil {
		t.Fatalf("full_state did not include the joined room: %v", body["rooms"])
	}
	if state := eventTypes(t, room, "state"); len(state) == 0 {
		t.Errorf("full_state should carry the room's state: %v", room["state"])
	}
}

// TestSyncKnockSection checks that a knock is reported under its own section rather than
// as a membership the client has to guess at.
func TestSyncKnockSection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "dmitri", "pw-dmitri", "")
	_, bobToken, _ := h.register(t, "elena", "pw-elena", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, putState(t, h, roomID, "m.room.join_rules", "", `{"join_rule":"knock"}`, aliceToken), http.StatusOK)

	_, next := syncNow(t, h, bobToken, "")
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/knock",
		map[string]any{"reason": "let me in"}, bobToken), http.StatusOK)

	body, _ := syncNow(t, h, bobToken, "&since="+next)
	room := roomSection(t, body, "knock", roomID)
	if room == nil {
		t.Fatalf("the knock is missing from the knock section: %v", body["rooms"])
	}
	if state, _ := room["knock_state"].(map[string]any); state == nil {
		t.Errorf("the knock section has no knock_state: %v", room)
	}
}

func containsString(list []string, want string) bool {
	for _, entry := range list {
		if entry == want {
			return true
		}
	}
	return false
}

// TestSyncRoomSummary checks the room summary counts, which let a client show "3 members"
// without downloading the member list of every room it is in.
func TestSyncRoomSummary(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "grace", "pw-grace", "")
	carol, _, _ := h.register(t, "ivan", "pw-ivan", "")
	_, bobToken, _ := h.register(t, "heidi", "pw-heidi", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": carol}, aliceToken), http.StatusOK)

	body, _ := syncNow(t, h, aliceToken, "")
	room := roomSection(t, body, "join", roomID)
	if room == nil {
		t.Fatalf("the room is missing from the first sync: %v", body["rooms"])
	}
	// The counts live inside `summary`, under the dotted keys the specification names.
	summary, _ := room["summary"].(map[string]any)
	if summary == nil {
		t.Fatalf("a joined room must carry a summary: %v", room)
	}
	if got := summary["m.joined_member_count"]; got != float64(2) {
		t.Errorf("m.joined_member_count = %v, want 2", got)
	}
	if got := summary["m.invited_member_count"]; got != float64(1) {
		t.Errorf("m.invited_member_count = %v, want 1", got)
	}
}

// TestSyncLeftRoomTimelineStopsAtLeaving checks that the leave section of a sync holds
// only what the user was entitled to: events up to the point they left, and no state.
// Anything else would let a left member keep reading a room they walked out of.
func TestSyncLeftRoomTimelineStopsAtLeaving(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "vince", "pw-vince", "")
	bob, bobToken, _ := h.register(t, "wendy4", "pw-wendy4", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	_, next := syncNow(t, h, bobToken, "")

	h.mustStatus(t, send(t, h, roomID, "m.room.message", "before-1",
		`{"msgtype":"m.text","body":"before"}`, aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)
	h.mustStatus(t, send(t, h, roomID, "m.room.message", "after-1",
		`{"msgtype":"m.text","body":"after"}`, aliceToken), http.StatusOK)
	// A state change made after bob left must not reach him either: the state a left
	// room shows is the state as of leaving.
	h.mustStatus(t, putState(t, h, roomID, "m.room.topic", "", `{"topic":"decided without bob"}`, aliceToken), http.StatusOK)

	body, next := syncNow(t, h, bobToken, "&since="+next)
	room := roomSection(t, body, "leave", roomID)
	if room == nil {
		t.Fatalf("the left room is missing: %v", body["rooms"])
	}
	events := room["timeline"].(map[string]any)["events"].([]any)
	var bodies []string
	found := false
	for _, entry := range events {
		event := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		if body, ok := content["body"].(string); ok {
			bodies = append(bodies, body)
		}
		if event["type"] == "m.room.member" && event["state_key"] == bob {
			// The membership event that took the user out is in the timeline: it is the
			// event that says why the room moved from `join` to `leave`.
			found = true
		}
	}
	if len(bodies) != 1 || bodies[0] != "before" {
		t.Errorf("timeline bodies = %v, want only the message sent before leaving", bodies)
	}
	if !found {
		t.Errorf("the leave event itself is missing from the timeline: %v", events)
	}
	// The state of a left room is its state as of leaving, minus what the timeline
	// already carries: the leave event is in the timeline, and the topic that was set
	// afterwards is not something he is entitled to see.
	for _, entry := range room["state"].(map[string]any)["events"].([]any) {
		event := entry.(map[string]any)
		switch event["type"] {
		case "m.room.message":
			t.Errorf("the state of a left room carries a message event: %v", event)
		case "m.room.topic":
			t.Errorf("the state of a left room carries a topic set after he left: %v", event)
		case "m.room.member":
			if event["state_key"] == bob {
				t.Errorf("the leave event is duplicated into state, where the timeline already has it: %v", event)
			}
		}
	}

	// A second incremental sync has nothing left to say about that room.
	body, _ = syncNow(t, h, bobToken, "&since="+next)
	if again := roomSection(t, body, "leave", roomID); again != nil {
		t.Errorf("the same leave was reported twice: %v", again)
	}
}
