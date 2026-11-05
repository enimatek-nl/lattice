package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Whether a redaction destroys anything is decided by each server that receives it, not
// by the server that sent it: the authorisation rules accept a redaction like any other
// event, and the specification says outright that "the ability to send a redaction event
// does not mean that the redaction itself should be performed". These tests cover what a
// client is left looking at afterwards — the event as it is now, plus a copy of what did
// it — and where the target field goes, which is the room version's business and not the
// client's.

func TestRedactionEmptiesAnEventAndSaysWhoDidIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, alice, _ := h.register(t, "redact1", "pw-redact1", "")
	roomID := createRoom(t, h, alice, map[string]any{"preset": "public_chat"})
	sent := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-offend",
		`{"msgtype":"m.text","body":"offending content"}`, alice), http.StatusOK).decoded(t)
	eventID, _ := sent["event_id"].(string)
	if eventID == "" {
		t.Fatalf("no event ID came back: %v", sent)
	}

	before := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, alice),
		http.StatusOK).decoded(t)
	if content, _ := before["content"].(map[string]any); content["body"] != "offending content" {
		t.Fatalf("reading the event before the redaction: %v", before["content"])
	}

	answer := h.mustStatus(t, h.put(t,
		"/_matrix/client/v3/rooms/"+roomID+"/redact/"+eventID+"/txn-redact",
		map[string]any{"reason": "because"}, alice), http.StatusOK).decoded(t)
	redactionID, _ := answer["event_id"].(string)
	if redactionID == "" {
		t.Fatalf("the redaction endpoint returned no event ID: %v", answer)
	}

	// A retry of the same transaction is the same redaction, not a second one.
	again := h.mustStatus(t, h.put(t,
		"/_matrix/client/v3/rooms/"+roomID+"/redact/"+eventID+"/txn-redact",
		map[string]any{"reason": "because"}, alice), http.StatusOK).decoded(t)
	if again["event_id"] != redactionID {
		t.Errorf("the same transaction produced %v and then %v", redactionID, again["event_id"])
	}

	after := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, alice),
		http.StatusOK).decoded(t)
	if content, ok := after["content"].(map[string]any); !ok || len(content) != 0 {
		t.Errorf("after the redaction the content is %v, want the empty object", after["content"])
	}
	unsigned, _ := after["unsigned"].(map[string]any)
	because, _ := unsigned["redacted_because"].(map[string]any)
	if because["type"] != "m.room.redaction" {
		t.Fatalf("the redacted event does not carry the redaction that did it: %v", unsigned)
	}
	becauseContent, _ := because["content"].(map[string]any)
	if becauseContent["reason"] != "because" {
		t.Errorf("the reason was not kept for the client: %v", becauseContent)
	}
	// Room version 11 names the target in `content`; the top-level field is added on the
	// way out for clients written against older versions.
	if becauseContent["redacts"] != eventID {
		t.Errorf("the redaction does not name its target in content: %v", becauseContent)
	}
	if because["redacts"] != eventID {
		t.Errorf("the redaction is not served with a top-level redacts for older clients: %v", because)
	}
	// What survives on the redacted event is what the room version protects: identity, not
	// what the user wrote.
	if after["sender"] == nil || after["type"] != "m.room.message" {
		t.Errorf("the redacted event lost its identity: %v", after)
	}
	if after["hashes"] == nil {
		t.Errorf("a redacted event must keep its hashes, or no server can check it: %v", after)
	}

	// The timeline shows the same event a direct read shows. A client must not be able to
	// read the destroyed content by asking a different question.
	page := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", alice),
		http.StatusOK).decoded(t)
	if event := chunkEvent(page["chunk"], eventID); event == nil {
		t.Fatalf("the paged timeline does not contain %s: %v", eventID, page["chunk"])
	} else if content, ok := event["content"].(map[string]any); !ok || len(content) != 0 {
		t.Errorf("/messages still shows %v for a redacted event", event["content"])
	}

	body, next := syncNow(t, h, alice, "")
	_ = next
	if room, ok := body["rooms"].(map[string]any); ok {
		join, _ := room["join"].(map[string]any)
		timeline, _ := join["timeline"].(map[string]any)
		if event := chunkEvent(timeline["events"], eventID); event != nil {
			if content, ok := event["content"].(map[string]any); !ok || len(content) != 0 {
				t.Errorf("/sync still shows %v for a redacted event", event["content"])
			}
		}
	}

	// Asking to redact something this server has never seen is answered, not accepted.
	h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/redact/$notreal:localhost/txn-ghost",
		map[string]any{}, alice), http.StatusNotFound)
	// So is reading an event that does not exist, or one that lives in another room: the
	// answer is the same either way, so the endpoint does not report which rooms hold
	// which event IDs.
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/$notreal:localhost", alice),
		http.StatusNotFound)
	other := createRoom(t, h, alice, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+other+"/event/"+eventID, alice),
		http.StatusNotFound)
}

// The target of a redaction lives at the top level of the event up to room version 10 and
// under `content` from version 11. A server that reads the wrong one either destroys
// nothing or destroys something the sender did not name; both are forks waiting to happen.
func TestRedactionNamesItsTargetWhereTheRoomVersionPutsIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, alice, _ := h.register(t, "redact2", "pw-redact2", "")
	roomID := createRoom(t, h, alice, map[string]any{"preset": "private_chat", "room_version": "10"})
	sent := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-old",
		`{"msgtype":"m.text","body":"older room"}`, alice), http.StatusOK).decoded(t)
	eventID, _ := sent["event_id"].(string)

	answer := h.mustStatus(t, h.put(t,
		"/_matrix/client/v3/rooms/"+roomID+"/redact/"+eventID+"/txn-old-redact",
		map[string]any{}, alice), http.StatusOK).decoded(t)
	redactionID, _ := answer["event_id"].(string)

	stored := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+redactionID, alice),
		http.StatusOK).decoded(t)
	if stored["type"] != "m.room.redaction" {
		t.Fatalf("the endpoint returned a %v, not a redaction", stored["type"])
	}
	if stored["redacts"] != eventID {
		t.Errorf("a version 10 redaction must name its target at the top level: %v", stored)
	}
	// `content.redacts` is added for newer clients reading an older room; it is not stored,
	// because an added field would change the event's hash and therefore its ID.
	if content, _ := stored["content"].(map[string]any); content["redacts"] != eventID {
		t.Errorf("a version 10 redaction is not served with content.redacts for newer clients: %v", stored)
	}

	after := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, alice),
		http.StatusOK).decoded(t)
	if content, ok := after["content"].(map[string]any); !ok || len(content) != 0 {
		t.Errorf("the version 10 event still carries %v after the redaction", after["content"])
	}
}

// A redaction whose partner has not arrived yet is kept, not guessed at: the specification
// says the server "simply waits for a valid partner event to arrive where it can then
// re-check". The arrival can be a `/send` transaction, a backfill response or a join, so
// the check belongs to persistence and not to either transport.
func TestRedactionArrivingBeforeItsTargetIsAppliedWhenTheTargetTurnsUp(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)
	ctx := context.Background()

	token := hs1.registerUser(t, "redact3", "pw-redact3")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, token)
	roomID, _ := created["room_id"].(string)
	_, sent := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txn-message",
		map[string]any{"msgtype": "m.text", "body": "arrive second"}, token)
	targetID, _ := sent["event_id"].(string)
	_, redacted := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/redact/"+targetID+"/txn-redact3",
		map[string]any{"reason": "out of order"}, token)
	redactionID, _ := redacted["event_id"].(string)

	snapshot := hs1.roomSnapshot(t, roomID, token)
	if _, answer := hs1.sendTransaction(t, hs2, "room-first", byDepth(dedupe(snapshot.State))); anyRefusal(t, answer) {
		t.Fatalf("hs2 refused the room's beginning: %v", answer.PDUs)
	}
	var state []map[string]any
	target := map[string]any{}
	redaction := map[string]any{}
	for _, event := range byDepth(dedupe(snapshot.State, snapshot.Messages)) {
		switch stringAny(event, "event_id") {
		case targetID:
			target = event
		case redactionID:
			redaction = event
		default:
			state = append(state, event)
		}
	}
	if len(target) == 0 || len(redaction) == 0 {
		t.Fatalf("%s's history is missing the message or its redaction", hs1.name)
	}
	// What hs1 serves to its own client is the redacted form; what it hands to a peer is
	// the unredacted event plus the redaction, because each server applies the strip to its
	// own database. Reconstructing the unredacted form here stands in for that difference —
	// and note that neither the event ID nor the signature changes when it is put back: both
	// are taken over the redacted form, which is exactly why a redaction does not rename an
	// event.
	target["content"] = map[string]any{"msgtype": "m.text", "body": "arrive second"}

	// The redaction arrives while hs2 has the room but not the event it names. It cites a
	// state event hs2 holds rather than its target: a redaction does not have to descend
	// from what it redacts, and that is what lets the two halves arrive in either order.
	first := state[len(state)-1]
	early := cloneEvent(t, redaction)
	early["prev_events"] = []any{stringAny(first, "event_id")}
	delete(early, "event_id")
	if err := events.Seal(hs1.name, roomversion.MustGet(snapshotRoomVersion(t, snapshot)), early, hs1.app.Keys); err != nil {
		t.Fatalf("resealing the redaction: %v", err)
	}
	earlyID, err := events.EventID(roomversion.MustGet(snapshotRoomVersion(t, snapshot)), early)
	if err != nil {
		t.Fatalf("the redaction's ID: %v", err)
	}
	early["event_id"] = earlyID

	if _, answer := hs1.sendTransaction(t, hs2, "redaction-first", []map[string]any{early}); anyRefusal(t, answer) {
		t.Fatalf("hs2 refused a valid redaction: %v", answer.PDUs)
	}
	if early, err := hs2.app.DB.Events.ByID(ctx, targetID); err == nil && early != nil {
		t.Fatalf("hs2 already has the target %s before it was sent", targetID)
	}

	// The target arrives. The link that was waiting is re-checked and applied, on the same
	// rule the sending server applied — and hs2's copy ends up redacted too.
	if _, answer := hs1.sendTransaction(t, hs2, "target-second", []map[string]any{target}); anyRefusal(t, answer) {
		t.Fatalf("hs2 refused the target event: %v", answer.PDUs)
	}
	row, err := hs2.app.DB.Events.ByID(ctx, targetID)
	if err != nil {
		t.Fatalf("hs2 did not store the target: %v", err)
	}
	if !row.Redacted || len(row.RedactedJSON) == 0 {
		t.Fatalf("hs2 stored the target but did not apply the redaction that was waiting for it")
	}
	if got := string(row.Serving()); !strings.Contains(got, `"content":{}`) {
		t.Errorf("what hs2 would now serve for the event is not emptied: %s", got)
	}
	// hs2 keeps the bytes it received. The redacted form is a view, not a replacement: the
	// content hash in `hashes` was taken over the unredacted event, and it is what lets hs2
	// — or any later reader — check that the event it holds is the one its origin signed.
	if stored := string(row.JSON); !strings.Contains(stored, "arrive second") {
		t.Errorf("hs2 threw away the content it received: %s", stored)
	}
}

// snapshotRoomVersion reads the version a snapshot's room was created with.
func snapshotRoomVersion(t *testing.T, snapshot roomEvents) string {
	t.Helper()
	for _, event := range snapshot.State {
		if stringAny(event, "type") != "m.room.create" {
			continue
		}
		if content, ok := event["content"].(map[string]any); ok {
			if version, ok := content["room_version"].(string); ok && version != "" {
				return version
			}
		}
	}
	return "11"
}

func anyRefusal(t *testing.T, answer transactionAnswer) bool {
	t.Helper()
	var refused bool
	for eventID, result := range answer.PDUs {
		if result.Error != "" {
			t.Logf("%s refused %s: %s", "hs2", eventID, result.Error)
			refused = true
		}
	}
	return refused
}

// chunkEvent finds one event in a decoded timeline or messages chunk.
func chunkEvent(chunk any, eventID string) map[string]any {
	items, _ := chunk.([]any)
	for _, item := range items {
		event, _ := item.(map[string]any)
		if event["event_id"] == eventID {
			return event
		}
	}
	return nil
}

// TestSyncExplainsWhyAnEventItAlreadySawWentEmpty covers the half of redaction a client
// cannot reconstruct on its own. `/messages` was already attaching the redaction under
// `unsigned.redacted_because`; `/sync` served the emptied event with no explanation, so a
// client watching a room saw content vanish with no record of what removed it — and the two
// read paths disagreed about the same event.
func TestSyncExplainsWhyAnEventItAlreadySawWentEmpty(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, alice, _ := h.register(t, "redact4", "pw-redact4", "")
	roomID := createRoom(t, h, alice, map[string]any{"preset": "public_chat"})
	sent := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-seev",
		`{"msgtype":"m.text","body":"there and then gone"}`, alice), http.StatusOK).decoded(t)
	eventID, _ := sent["event_id"].(string)
	answer := h.mustStatus(t, h.put(t,
		"/_matrix/client/v3/rooms/"+roomID+"/redact/"+eventID+"/txn-redact4",
		map[string]any{"reason": "explain yourself"}, alice), http.StatusOK).decoded(t)
	redactionID, _ := answer["event_id"].(string)

	body, _ := syncNow(t, h, alice, "")
	room := roomSection(t, body, "join", roomID)
	if room == nil {
		t.Fatalf("the room is missing from the sync: %v", body["rooms"])
	}
	list, _ := room["timeline"].(map[string]any)
	events, _ := list["events"].([]any)

	var seen bool
	for _, entry := range events {
		event, _ := entry.(map[string]any)
		if event["event_id"] != eventID {
			continue
		}
		seen = true
		if content, ok := event["content"].(map[string]any); !ok || len(content) != 0 {
			t.Errorf("the timeline serves the event with content still on it: %v", event["content"])
		}
		unsigned, _ := event["unsigned"].(map[string]any)
		because, ok := unsigned["redacted_because"].(map[string]any)
		if !ok {
			t.Fatalf("the sync timeline does not say what redacted the event: %v", event)
		}
		if because["event_id"] != redactionID {
			t.Errorf("redacted_because names %v, want %s", because["event_id"], redactionID)
		}
	}
	if !seen {
		t.Fatalf("the redacted event is not in the sync timeline: %v", events)
	}

	// The redaction itself is showable — both halves are in hand — and a v11 client looks
	// for the target in the content.
	var redactionSeen bool
	for _, entry := range events {
		event, _ := entry.(map[string]any)
		if event["event_id"] != redactionID {
			continue
		}
		redactionSeen = true
		content, _ := event["content"].(map[string]any)
		if content["redacts"] != eventID {
			t.Errorf("the redaction does not name its target where a v11 client looks: %v", event)
		}
	}
	if !redactionSeen {
		t.Errorf("the redaction is missing from the sync timeline: %v", events)
	}
}

// TestARedactionIsNotShownToAClientUntilItsTargetArrives is the specification's should: a
// redaction should not be sent to a client until both the redaction and the event it affects
// have been received and can be validated.
//
// The rule protects the client's picture of the room, not the server's data. A client shown
// a redaction for an event it has never seen learns that something was destroyed with no way
// to see the destruction — and if it later receives the target from elsewhere, it has to
// invent the ordering of a change it was told about out of order.
func TestARedactionIsNotShownToAClientUntilItsTargetArrives(t *testing.T) {
	t.Parallel()
	hs1, hs2 := federationPair(t)

	token := hs1.registerUser(t, "redact5", "pw-redact5")
	_, created := hs1.clientDo(t, http.MethodPost, "/_matrix/client/v3/createRoom",
		map[string]any{"preset": "public_chat"}, token)
	roomID, _ := created["room_id"].(string)
	_, sent := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/txn-late",
		map[string]any{"msgtype": "m.text", "body": "arrives after its redaction"}, token)
	targetID, _ := sent["event_id"].(string)
	_, redacted := hs1.clientDo(t, http.MethodPut,
		"/_matrix/client/v3/rooms/"+roomID+"/redact/"+targetID+"/txn-redact5",
		map[string]any{"reason": "out of order again"}, token)
	redactionID, _ := redacted["event_id"].(string)

	snapshot := hs1.roomSnapshot(t, roomID, token)
	rules := roomversion.MustGet(snapshotRoomVersion(t, snapshot))
	state := byDepth(dedupe(snapshot.State))
	var target map[string]any
	for _, event := range byDepth(dedupe(snapshot.State, snapshot.Messages)) {
		if stringAny(event, "event_id") == targetID {
			target = event
		}
	}
	if target == nil {
		t.Fatalf("%s's history is missing the message", hs1.name)
	}
	// What hs1 serves its own client is the redacted view; what it hands a peer is the event
	// as signed plus the redaction, because each server applies the strip to its own copy.
	// The content hash was taken over the unredacted form, so putting the content back is
	// what makes the handover checkable — and note the event ID does not change, which is
	// exactly why a redaction does not rename an event.
	target["content"] = map[string]any{"msgtype": "m.text", "body": "arrives after its redaction"}

	// hs2 gets the room and the redaction, but not the message the redaction names. It cites
	// a state event hs2 holds rather than its target: a redaction need not descend from the
	// event it destroys, which is what lets the two halves arrive in either order.
	first := state[len(state)-1]
	var pending map[string]any
	for _, event := range byDepth(dedupe(snapshot.Messages)) {
		if stringAny(event, "event_id") == redactionID {
			pending = cloneEvent(t, event)
		}
	}
	if pending == nil {
		t.Fatalf("%s's history is missing the redaction", hs1.name)
	}
	pending["prev_events"] = []any{stringAny(first, "event_id")}
	delete(pending, "event_id")
	if err := events.Seal(hs1.name, rules, pending, hs1.app.Keys); err != nil {
		t.Fatalf("resealing the redaction: %v", err)
	}
	pendingID, err := events.EventID(rules, pending)
	if err != nil {
		t.Fatalf("the early redaction's ID: %v", err)
	}
	pending["event_id"] = pendingID

	if _, answer := hs1.sendTransaction(t, hs2, "room-without-target", byDepth(dedupe(snapshot.State))); anyRefusal(t, answer) {
		t.Fatalf("%s refused the room's beginning: %v", hs2.name, answer.PDUs)
	}
	if _, answer := hs1.sendTransaction(t, hs2, "redaction-without-target", []map[string]any{pending}); anyRefusal(t, answer) {
		t.Fatalf("%s refused a valid redaction: %v", hs2.name, answer.PDUs)
	}

	// A local user joins the room hs2 was told about, and asks what is in it.
	bea := hs2.registerUser(t, "bea", "pw-bea")
	if status, answer := hs2.clientDo(t, http.MethodPost, "/_matrix/client/v3/join/"+roomID, map[string]any{}, bea); status != http.StatusOK {
		t.Fatalf("%s would not let its own user join a room it holds: %v", hs2.name, answer)
	}

	// The redaction is held by this server, and no client-facing path shows it — while the
	// page around it still answers, because withholding one event is not a reason to fail a
	// timeline read.
	status, page := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, bea)
	if status != http.StatusOK {
		t.Fatalf("%s refused the history of a room its own user is in: %v", hs2.name, page)
	}
	chunk := chunkList(t, page["chunk"])
	if len(chunk) < 5 {
		t.Fatalf("%s's history page is short of the state it was handed: %v", hs2.name, chunk)
	}
	for _, entry := range chunk {
		if stringAny(entry, "type") == "m.room.redaction" {
			t.Errorf("%s shows a redaction whose target it has never seen: %v", hs2.name, entry)
		}
	}
	if status, answer := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/event/"+pendingID, nil, bea); status != http.StatusNotFound {
		t.Errorf("%s answered the unshowable redaction with %d (%v), want 404", hs2.name, status, answer)
	}
	if status, body := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/sync?timeout=0", nil, bea); status != http.StatusOK {
		t.Fatalf("%s refused a sync to its own user: %v", hs2.name, body)
	} else if syncHasRedaction(t, body, roomID) {
		t.Errorf("%s put an unshowable redaction in the sync timeline", hs2.name)
	}

	// The target turns up. The redaction is applied, and both halves become showable.
	if _, answer := hs1.sendTransaction(t, hs2, "target-at-last", []map[string]any{target}); anyRefusal(t, answer) {
		t.Fatalf("%s refused the late message: %v", hs2.name, answer.PDUs)
	}
	status, page = hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?limit=50", nil, bea)
	if status != http.StatusOK {
		t.Fatalf("%s refused the history after both halves arrived: %v", hs2.name, page)
	}
	var sawRedaction, sawTarget bool
	for _, entry := range chunkList(t, page["chunk"]) {
		switch stringAny(entry, "event_id") {
		case pendingID:
			sawRedaction = true
		case targetID:
			sawTarget = true
			if content, ok := entry["content"].(map[string]any); ok && len(content) != 0 {
				t.Errorf("%s kept the content the redaction removes: %v", hs2.name, content)
			}
		}
	}
	if !sawRedaction || !sawTarget {
		t.Errorf("%s's history after both halves arrive: redaction shown %v, target shown %v",
			hs2.name, sawRedaction, sawTarget)
	}
	if status, answer := hs2.clientDo(t, http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/event/"+pendingID, nil, bea); status != http.StatusOK {
		t.Errorf("%s still refuses the redaction after both halves arrived: %d %v", hs2.name, status, answer)
	}
}

// syncHasRedaction reports whether a sync response's timeline for a room carries a redaction.
func syncHasRedaction(t *testing.T, body map[string]any, roomID string) bool {
	t.Helper()
	rooms, _ := body["rooms"].(map[string]any)
	joined, _ := rooms["join"].(map[string]any)
	room, _ := joined[roomID].(map[string]any)
	timeline, _ := room["timeline"].(map[string]any)
	list, _ := timeline["events"].([]any)
	for _, entry := range list {
		event, _ := entry.(map[string]any)
		if stringAny(event, "type") == "m.room.redaction" {
			return true
		}
	}
	return false
}

// chunkList decodes a `/messages` chunk into events.
func chunkList(t *testing.T, chunk any) []map[string]any {
	t.Helper()
	raw, _ := chunk.([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if event, ok := entry.(map[string]any); ok {
			out = append(out, event)
		}
	}
	return out
}
