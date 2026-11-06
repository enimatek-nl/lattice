package unit

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
)

// The read paths used to decide separately what a client may see for a redacted event, and
// they drifted: `/messages` emptied an event and named the redaction that did it, while
// `/sync` served the emptied form with no explanation at all. These tests pin the one place
// that decides, so a path cannot quietly go its own way again.

func openServicer(t *testing.T) (*redaction.Servicer, *persist.Database) {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "serve.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	for _, room := range []persist.Room{{RoomID: "!v11:example.org", RoomVersion: "11", Creator: "@alice:example.org"},
		{RoomID: "!v10:example.org", RoomVersion: "10", Creator: "@alice:example.org"}} {
		if err := db.Rooms.Create(ctx, &room); err != nil {
			t.Fatalf("create %s: %v", room.RoomID, err)
		}
	}
	return redaction.NewServicer(db.Events, db.Rooms, db.StateGroups), db
}

// store writes an event row in the shape the read paths see it.
func store(t *testing.T, db *persist.Database, roomID, eventID, raw string, redacted bool, redactedRaw string) *persist.Event {
	t.Helper()
	row := &persist.Event{
		EventID: eventID, RoomID: roomID, Type: eventTypeOf(raw),
		JSON: []byte(raw), Redacted: redacted, RedactedJSON: []byte(redactedRaw),
	}
	if err := db.Events.Insert(context.Background(), row); err != nil {
		t.Fatalf("insert %s: %v", eventID, err)
	}
	return row
}

func eventTypeOf(raw string) string {
	for _, candidate := range []string{"m.room.redaction", "m.room.message"} {
		if strings.Contains(raw, `"type":"`+candidate+`"`) {
			return candidate
		}
	}
	return "m.room.message"
}

// TestServeNamesTheRedactionThatEmptiedAnEvent is the half a client cannot reconstruct on
// its own: an event it had already read comes back empty, and `redacted_because` is the only
// thing that says why.
func TestServeNamesTheRedactionThatEmptiedAnEvent(t *testing.T) {
	t.Parallel()
	serve, db := openServicer(t)
	ctx := context.Background()

	target := store(t, db, "!v11:example.org", "$target:example.org",
		`{"type":"m.room.message","event_id":"$target:example.org","room_id":"!v11:example.org","content":{"body":"hello"}}`,
		true, `{"type":"m.room.message","event_id":"$target:example.org","room_id":"!v11:example.org","content":{},"unsigned":{}}`)
	store(t, db, "!v11:example.org", "$redact:example.org",
		`{"type":"m.room.redaction","event_id":"$redact:example.org","room_id":"!v11:example.org","content":{"redacts":"$target:example.org"}}`,
		false, "")
	if err := db.Events.RecordRedaction(ctx, &persist.Redaction{
		RedactionID: "$redact:example.org", TargetID: "$target:example.org", RoomID: "!v11:example.org", Applied: true,
	}); err != nil {
		t.Fatalf("record the link: %v", err)
	}

	event, err := serve.Serve(ctx, target)
	if err != nil {
		t.Fatalf("serving the redacted event: %v", err)
	}
	content, _ := event["content"].(map[string]any)
	if len(content) != 0 {
		t.Errorf("a redacted event still carries content: %v", content)
	}
	unsigned, _ := event["unsigned"].(map[string]any)
	because, ok := unsigned["redacted_because"].(map[string]any)
	if !ok {
		t.Fatalf("no unsigned.redacted_because: %v", event)
	}
	if got, _ := because["event_id"].(string); got != "$redact:example.org" {
		t.Errorf("redacted_because names %v, want the redaction that did it", got)
	}
	// Room version 11 puts `redacts` in the content; a client reading the copy under
	// `unsigned` should not have to know that.
	becauseContent, _ := because["content"].(map[string]any)
	if id, _ := becauseContent["redacts"].(string); id != "$target:example.org" {
		t.Errorf("the copy under unsigned puts redacts nowhere a v11 client looks: %v", because)
	}
}

// TestServeWithholdsARedactionWhoseTargetHasNotArrived is the specification's should: a
// redaction should not reach a client until the event it affects has arrived too. A client
// shown the redaction alone learns that something was destroyed with no way to see the
// destruction.
func TestServeWithholdsARedactionWhoseTargetHasNotArrived(t *testing.T) {
	t.Parallel()
	serve, db := openServicer(t)
	ctx := context.Background()

	message := store(t, db, "!v11:example.org", "$message:example.org",
		`{"type":"m.room.message","event_id":"$message:example.org","room_id":"!v11:example.org","content":{"body":"arrive later"}}`,
		false, "")
	pending := store(t, db, "!v11:example.org", "$pending:example.org",
		`{"type":"m.room.redaction","event_id":"$pending:example.org","room_id":"!v11:example.org","content":{"redacts":"$missing:example.org"}}`,
		false, "")
	if err := db.Events.RecordRedaction(ctx, &persist.Redaction{
		RedactionID: "$pending:example.org", TargetID: "$missing:example.org", RoomID: "!v11:example.org", Applied: false,
	}); err != nil {
		t.Fatalf("record the link: %v", err)
	}

	if _, err := serve.Serve(ctx, pending); !errors.Is(err, redaction.ErrWithheld) {
		t.Errorf("serving a redaction with no target in hand returned %v, want ErrWithheld", err)
	}
	chunk, err := serve.ServeChunk(ctx, []*persist.Event{message, pending})
	if err != nil {
		t.Fatalf("serving the chunk: %v", err)
	}
	if len(chunk) != 1 {
		t.Fatalf("the chunk holds %d events, want the message alone: %v", len(chunk), chunk)
	}
	if got, _ := chunk[0]["event_id"].(string); got != "$message:example.org" {
		t.Errorf("the chunk's survivor is %v, want the message", got)
	}

	// The target turns up, the link is settled, and the redaction becomes showable.
	target := store(t, db, "!v11:example.org", "$missing:example.org",
		`{"type":"m.room.message","event_id":"$missing:example.org","room_id":"!v11:example.org","content":{"body":"late"}}`,
		true, `{"type":"m.room.message","event_id":"$missing:example.org","room_id":"!v11:example.org","content":{},"unsigned":{}}`)
	if err := db.Events.MarkRedactionApplied(ctx, "$pending:example.org"); err != nil {
		t.Fatalf("settle the link: %v", err)
	}
	chunk, err = serve.ServeChunk(ctx, []*persist.Event{message, pending, target})
	if err != nil {
		t.Fatalf("serving the chunk after the target arrived: %v", err)
	}
	if len(chunk) != 3 {
		t.Errorf("after both halves are in hand the chunk holds %d events, want all three", len(chunk))
	}
}

// TestServePlacesRedactsWhereTheRoomVersionPutsIt keeps the room-version difference in one
// place. A v10 client looks for a top-level `redacts`, a v11 client looks in the content, and
// the servicer makes sure each finds it there whatever the stored bytes happened to look
// like. The stored event is never rewritten — an added field would change the reference hash
// and with it the event's identity — so this is a view, applied on the way out.
func TestServePlacesRedactsWhereTheRoomVersionPutsIt(t *testing.T) {
	t.Parallel()
	serve, db := openServicer(t)
	ctx := context.Background()

	v10 := store(t, db, "!v10:example.org", "$redact10:example.org",
		`{"type":"m.room.redaction","event_id":"$redact10:example.org","room_id":"!v10:example.org","redacts":"$target:example.org","content":{}}`,
		false, "")
	if err := db.Events.RecordRedaction(ctx, &persist.Redaction{
		RedactionID: "$redact10:example.org", TargetID: "$target:example.org", RoomID: "!v10:example.org", Applied: true,
	}); err != nil {
		t.Fatalf("record the link: %v", err)
	}
	event, err := serve.Serve(ctx, v10)
	if err != nil {
		t.Fatalf("serving the v10 redaction: %v", err)
	}
	if got, _ := event["redacts"].(string); got != "$target:example.org" {
		t.Errorf("a v10 room's redaction should answer with a top-level redacts, got %v", event)
	}

	v11 := store(t, db, "!v11:example.org", "$redact11:example.org",
		`{"type":"m.room.redaction","event_id":"$redact11:example.org","room_id":"!v11:example.org","redacts":"$target:example.org","content":{}}`,
		false, "")
	if err := db.Events.RecordRedaction(ctx, &persist.Redaction{
		RedactionID: "$redact11:example.org", TargetID: "$target:example.org", RoomID: "!v11:example.org", Applied: true,
	}); err != nil {
		t.Fatalf("record the link: %v", err)
	}
	event, err = serve.Serve(ctx, v11)
	if err != nil {
		t.Fatalf("serving the v11 redaction: %v", err)
	}
	content, _ := event["content"].(map[string]any)
	if id, _ := content["redacts"].(string); id != "$target:example.org" {
		t.Errorf("a v11 room's redaction should answer with content.redacts, got %v", event)
	}
	// The view adds a place for a v11 client to look; it does not take away the field the
	// event arrived with, because those bytes are what the origin signed.
	if got, _ := event["redacts"].(string); got != "$target:example.org" {
		t.Errorf("serving the event should not hide the field it arrived with: %v", event)
	}
}

// A chunk is served as a unit, and one event in it can be withheld: a redaction whose target
// has not reached this server is left out rather than shown as a destruction the client cannot
// place. That is only safe if what stays behind keeps its own company. Pairing output position
// to input position — which this code used to do — attaches one event's `unsigned` to the next
// event's body as soon as anything is withheld: a topic's `prev_content` on a message, a
// transaction ID on a state event that never had one.
func TestServeChunkKeepsEachEventWithItsOwnUnsigned(t *testing.T) {
	t.Parallel()
	servicer, db := openServicer(t)
	ctx := context.Background()
	roomID := "!v11:example.org"

	insert := func(eventID, eventType, raw string, txnID string, prevContent []byte, prevSender string) *persist.Event {
		t.Helper()
		row := &persist.Event{
			EventID: eventID, RoomID: roomID, Type: eventType, JSON: []byte(raw),
			TxnID: txnID, PrevContent: prevContent, PrevSender: prevSender,
		}
		if err := db.Events.Insert(ctx, row); err != nil {
			t.Fatalf("insert %s: %v", eventID, err)
		}
		return row
	}

	message := insert("$message:e", "m.room.message",
		`{"event_id":"$message:e","room_id":"!v11:example.org","type":"m.room.message","content":{"msgtype":"m.text","body":"kept"}}`,
		"txn-on-message", nil, "")
	// A redaction with nothing stored that it redacts: not yet showable, so it leaves the chunk.
	insert("$redaction:e", "m.room.redaction",
		`{"event_id":"$redaction:e","room_id":"!v11:example.org","type":"m.room.redaction","redacts":"$elsewhere:e","content":{}}`,
		"", nil, "")
	topic := insert("$topic:e", "m.room.topic",
		`{"event_id":"$topic:e","room_id":"!v11:example.org","type":"m.room.topic","state_key":"","content":{"topic":"now"}}`,
		"", []byte(`{"topic":"was"}`), "@alice:example.org")

	withheldRow, err := db.Events.ByID(ctx, "$redaction:e")
	if err != nil {
		t.Fatalf("reading the redaction back: %v", err)
	}
	out, err := servicer.ServeChunk(ctx, []*persist.Event{message, withheldRow, topic})
	if err != nil {
		t.Fatalf("serving the chunk: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("the chunk holds %d events, want 2 (the redaction is withheld): %v", len(out), eventIDsOf(out))
	}
	for i, want := range []string{"$message:e", "$topic:e"} {
		if got, _ := out[i]["event_id"].(string); got != want {
			t.Fatalf("position %d of the served chunk is %s, want %s", i, got, want)
		}
	}
	// Each event carries its own addition, and only its own.
	if unsigned, _ := out[0]["unsigned"].(map[string]any); unsigned["transaction_id"] != "txn-on-message" {
		t.Errorf("the message lost its transaction ID: %#v", out[0]["unsigned"])
	} else if _, ok := unsigned["prev_content"]; ok {
		t.Errorf("the message carries a prev_content that belongs to the topic event: %#v", unsigned)
	}
	unsigned, _ := out[1]["unsigned"].(map[string]any)
	prev, _ := unsigned["prev_content"].(map[string]any)
	if prev == nil || prev["topic"] != "was" {
		t.Errorf("the topic event does not carry what it replaced: %#v", out[1]["unsigned"])
	}
	if _, ok := unsigned["transaction_id"]; ok {
		t.Errorf("the topic event carries the message's transaction ID: %#v", unsigned)
	}
}

func eventIDsOf(events []map[string]any) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		id, _ := event["event_id"].(string)
		ids = append(ids, id)
	}
	return ids
}
