package compat

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestMembershipOfTheReaderTravelsOnEvents covers MSC4115: an event served in a room tells the
// reader what *their own* membership of that room was when the event was written. It is not about
// the sender - the message alice wrote before bob joined says "leave" to bob and "join" to alice -
// and a client cannot reconstruct it afterwards, because a room that opened its history at the
// join never shows the membership events that would have said so.
func TestMembershipOfTheReaderTravelsOnEvents(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "loblaw")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	early := server.Send(alice, roomID, "early", "written before the join")
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	late := server.Send(alice, roomID, "late", "written after the join")

	sync := server.MustSync(bob, "?limit=50")
	room, ok := sync.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from bob's sync: %s", roomID)
	}
	// A join answers with a room id only, so the event that was written is taken from the timeline.
	joined := ""
	for _, event := range room.Timeline.Events {
		if event.Type == matrix.TypeMember && event.StateKey != nil && *event.StateKey == bob.UserID {
			joined = event.ID
		}
	}
	if joined == "" {
		t.Fatalf("bob's own membership event is not in his timeline: %s", roomID)
	}
	fromTimeline := membershipsOf(room.Timeline.Events)
	// Every event of a timeline carries the field: a client deciding what to grey out has to be
	// able to ask it of the state event at the top of the window as well as of a message.
	for _, event := range room.Timeline.Events {
		if fromTimeline[event.ID] == "" {
			t.Errorf("timeline event %s (%s) carries no unsigned.membership", event.ID, event.Type)
		}
	}
	for eventID, want := range map[string]string{early: "leave", joined: "join", late: "join"} {
		if got := fromTimeline[eventID]; got != want {
			t.Errorf("/sync says bob's membership was %q when %s was written, want %q", got, eventID, want)
		}
	}

	chunk := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", bob.Token, nil, http.StatusOK)
	var page struct {
		Chunk []matrix.Event `json:"chunk"`
	}
	if err := json.Unmarshal(chunk.Raw, &page); err != nil {
		t.Fatalf("GET /messages is not the shape this test reads: %v\n%s", err, chunk.Raw)
	}
	fromChunk := membershipsOf(page.Chunk)
	for eventID, want := range map[string]string{early: "leave", joined: "join", late: "join"} {
		if got := fromChunk[eventID]; got != want {
			t.Errorf("GET /messages says bob's membership was %q when %s was written, want %q", got, eventID, want)
		}
	}

	// One event on its own answers the same question, and the answer belongs to the reader: the
	// same event is a "join" for the member who wrote it and a "leave" for the one who arrived later.
	if got := membershipAt(t, server, alice, roomID, early); got != "join" {
		t.Errorf("GET /event says alice's membership was %q at her own message, want join", got)
	}
	if got := membershipAt(t, server, bob, roomID, early); got != "leave" {
		t.Errorf("GET /event says bob's membership was %q at a message older than his join, want leave", got)
	}
	if got := membershipAt(t, server, bob, roomID, late); got != "join" {
		t.Errorf("GET /event says bob's membership was %q at a message newer than his join, want join", got)
	}

	context := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/context/"+early, bob.Token, nil, http.StatusOK)
	var around struct {
		Event matrix.Event `json:"event"`
	}
	if err := json.Unmarshal(context.Raw, &around); err != nil {
		t.Fatalf("GET /context is not the shape this test reads: %v\n%s", err, context.Raw)
	}
	if got, _ := around.Event.Unsigned["membership"].(string); got != "leave" {
		t.Errorf("GET /context says bob's membership was %q at the event he asked around, want leave", got)
	}
}

// membershipsOf reads the field this test is about out of a list of events, by event id.
func membershipsOf(events []matrix.Event) map[string]string {
	out := make(map[string]string, len(events))
	for _, event := range events {
		text, _ := event.Unsigned["membership"].(string)
		out[event.ID] = text
	}
	return out
}

// membershipAt asks one endpoint for one event and reads the field back.
func membershipAt(t *testing.T, server *Server, session *Session, roomID, eventID string) string {
	t.Helper()
	resp := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, session.Token, nil, http.StatusOK)
	var body matrix.Event
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatalf("GET /event is not the shape this test reads: %v\n%s", err, resp.Raw)
	}
	text, _ := body.Unsigned["membership"].(string)
	return text
}
