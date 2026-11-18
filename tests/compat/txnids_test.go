package compat

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The transaction id a client sent with an event is its own note about that event: it is how a
// client that lost the answer to PUT /send recognises the event it already sent. The
// specification therefore lets the server hand it back - in `unsigned.transaction_id`, and to
// the device that wrote it and nobody else, because what one device meant by "txn 42" is none
// of the business of another device, not even one belonging to the same user.
//
// Two things are needed to keep that promise: the append has to remember which device spoke,
// and the reads have to look that up again. Complement measures both halves
// (TestTxnInEvent, TestTxnScopeOnLocalEcho).

// syncTimelineEvent is the part of a /sync timeline this test reads.
type syncTimelineEvent struct {
	EventID  string         `json:"event_id"`
	Type     string         `json:"type"`
	Unsigned map[string]any `json:"unsigned"`
}

// syncTransactionID returns the `unsigned.transaction_id` a sync carried for one event, and
// whether the event was in the timeline at all.
func syncTransactionID(t *testing.T, server *Server, token, roomID, eventID string) (string, bool, bool) {
	t.Helper()
	resp := server.Do(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", token, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("sync answered %d: %s", resp.Status, resp.Raw)
	}
	var body struct {
		Rooms struct {
			Join map[string]struct {
				Timeline struct {
					Events []syncTimelineEvent `json:"events"`
				} `json:"timeline"`
			} `json:"join"`
		} `json:"rooms"`
	}
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatalf("sync is not the shape this test reads: %v\n%s", err, resp.Raw)
	}
	for _, event := range body.Rooms.Join[roomID].Timeline.Events {
		if event.EventID != eventID {
			continue
		}
		value, ok := event.Unsigned["transaction_id"]
		text, _ := value.(string)
		return text, ok, true
	}
	return "", false, false
}

// eventTransactionID is the same question about GET /rooms/{roomID}/event/{eventID}.
func eventTransactionID(t *testing.T, server *Server, token, roomID, eventID string) (string, bool) {
	t.Helper()
	resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/event/"+eventID, token, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /event answered %d: %s", resp.Status, resp.Raw)
	}
	var body struct {
		Unsigned map[string]any `json:"unsigned"`
	}
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatalf("GET /event is not the shape this test reads: %v\n%s", err, resp.Raw)
	}
	value, ok := body.Unsigned["transaction_id"]
	text, _ := value.(string)
	return text, ok
}

// chunkTransactionID is the same question about a page of GET /rooms/{roomID}/messages: paging
// back through a room one has sent in is exactly where a client needs its own note again.
func chunkTransactionID(t *testing.T, server *Server, token, roomID, eventID string) (string, bool) {
	t.Helper()
	resp := server.Do(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=50", token, nil)
	if resp.Status != http.StatusOK {
		t.Fatalf("GET /messages answered %d: %s", resp.Status, resp.Raw)
	}
	var body struct {
		Chunk []syncTimelineEvent `json:"chunk"`
	}
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatalf("GET /messages is not the shape this test reads: %v\n%s", err, resp.Raw)
	}
	for _, event := range body.Chunk {
		if event.EventID != eventID {
			continue
		}
		value, ok := event.Unsigned["transaction_id"]
		text, _ := value.(string)
		return text, ok
	}
	t.Fatalf("the page did not carry %s at all: %s", eventID, resp.Raw)
	return "", false
}

func TestTransactionIDSentWithTheEventComesBackToItsOwnDevice(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	const txnID = "txn-42-of-alices"
	path := "/_matrix/client/v3/rooms/" + roomID + "/send/m.room.message/" + txnID
	eventID := server.MustDo(http.MethodPut, path, alice.Token, map[string]any{
		"msgtype": "m.text",
		"body":    "the note is hers",
	}, http.StatusOK).String("event_id")

	// The device that sent it gets the note back on both doors it might ask at.
	if got, ok := eventTransactionID(t, server, alice.Token, roomID, eventID); !ok || got != txnID {
		t.Errorf("GET /event for the sending device: unsigned.transaction_id = %q present=%v, want %q", got, ok, txnID)
	}
	if got, ok, inTimeline := syncTransactionID(t, server, alice.Token, roomID, eventID); !inTimeline {
		t.Fatalf("the event never reached alice's sync timeline")
	} else if !ok || got != txnID {
		t.Errorf("sync for the sending device: unsigned.transaction_id = %q present=%v, want %q", got, ok, txnID)
	}
	if got, ok := chunkTransactionID(t, server, alice.Token, roomID, eventID); !ok || got != txnID {
		t.Errorf("GET /messages for the sending device: unsigned.transaction_id = %q present=%v, want %q", got, ok, txnID)
	}

	// Another device of the same user is another device: it has no idea what "txn-42" meant,
	// and telling it so invites it to believe it knows which send an event answers.
	second := server.Login("alice", "wonderland")
	if second.DeviceID == alice.DeviceID {
		t.Fatalf("a second login handed out the same device (%s); this test needs two", second.DeviceID)
	}
	if got, ok := eventTransactionID(t, server, second.Token, roomID, eventID); ok {
		t.Errorf("GET /event for another device of the same user carried %q; a transaction id belongs to the device that wrote it", got)
	}
	if got, ok, _ := syncTransactionID(t, server, second.Token, roomID, eventID); ok {
		t.Errorf("sync for another device of the same user carried %q; a transaction id belongs to the device that wrote it", got)
	}
	if got, ok := chunkTransactionID(t, server, second.Token, roomID, eventID); ok {
		t.Errorf("GET /messages for another device of the same user carried %q", got)
	}

	// And neither does anybody else in the room.
	if got, ok := eventTransactionID(t, server, bob.Token, roomID, eventID); ok {
		t.Errorf("GET /event for another user carried %q", got)
	}
	if got, ok, _ := syncTransactionID(t, server, bob.Token, roomID, eventID); ok {
		t.Errorf("sync for another user carried %q", got)
	}
}
