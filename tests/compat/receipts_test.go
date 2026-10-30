package compat

import (
	"net/http"
	"net/url"
	"testing"
)

// receiptListing reads the room -> event -> receipt type -> user tree of
// GET /v1/rooms/{roomId}/receipts.
func receiptListing(t *testing.T, body map[string]any, roomID string) map[string]any {
	t.Helper()
	entry, ok := body[roomID].(map[string]any)
	if !ok {
		t.Fatalf("no entry for %s in %v", roomID, body)
	}
	return entry
}

// receiptUser digs the "data" object of one receipt out of a listing.
func receiptUser(t *testing.T, listing map[string]any, eventID, receiptType, userID string) map[string]any {
	t.Helper()
	event, ok := listing[eventID].(map[string]any)
	if !ok {
		return nil
	}
	byType, ok := event[receiptType].(map[string]any)
	if !ok {
		return nil
	}
	entry, ok := byType[userID].(map[string]any)
	if !ok {
		return nil
	}
	data, _ := entry["data"].(map[string]any)
	return data
}

// TestReceiptListing covers GET /v1/rooms/{roomId}/receipts, which hands a
// client the whole receipt state of a room in one call.
func TestReceiptListing(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "kettle")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat", "name": "Ledger"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+url.PathEscape(roomID), bob.Token, nil, http.StatusOK)

	first := server.Send(alice, roomID, "r1", "first message")
	second := server.Send(bob, roomID, "r2", "second message")

	// Alice reads up to the message of Bob, Bob privately reads the one of Alice.
	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read", second), alice.Token,
		map[string]any{"ts": 1700000000000}, http.StatusOK)
	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read.private", first), bob.Token,
		map[string]any{}, http.StatusOK)

	path := "/_matrix/client/v1/rooms/" + url.PathEscape(roomID) + "/receipts"
	resp := server.MustDo(http.MethodGet, path, alice.Token, nil, http.StatusOK)
	listing := receiptListing(t, resp.Body, roomID)
	data := receiptUser(t, listing, second, "m.read", alice.UserID)
	if data == nil {
		t.Fatalf("the read receipt of alice is missing: %s", resp.Raw)
	}
	if ts, _ := data["ts"].(float64); int64(ts) != 1700000000000 {
		t.Errorf("ts = %v, want the timestamp the client sent", data["ts"])
	}
	// A private receipt is only visible to the user that posted it.
	if receiptUser(t, listing, first, "m.read.private", bob.UserID) != nil {
		t.Errorf("a private receipt of bob leaked to alice: %s", resp.Raw)
	}

	resp = server.MustDo(http.MethodGet, path, bob.Token, nil, http.StatusOK)
	listing = receiptListing(t, resp.Body, roomID)
	if data := receiptUser(t, listing, first, "m.read.private", bob.UserID); data == nil {
		t.Fatalf("bob cannot see his own private receipt: %s", resp.Raw)
	}
	if ts, _ := data["ts"].(float64); int64(ts) == 0 {
		t.Errorf("a receipt without a ts should still report one: %v", data)
	}
	if receiptUser(t, listing, second, "m.read", alice.UserID) == nil {
		t.Errorf("bob should still see the public receipt of alice: %s", resp.Raw)
	}

	// A fully read marker is not a receipt and must not show up in the listing.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/read_markers",
		alice.Token, map[string]any{"m.fully_read": second, "m.read": second}, http.StatusOK)
	resp = server.MustDo(http.MethodGet, path, alice.Token, nil, http.StatusOK)
	listing = receiptListing(t, resp.Body, roomID)
	if _, ok := listing[matrixFullyReadType]; ok {
		t.Errorf("the fully read marker appeared as a receipt: %v", listing)
	}
	for eventID := range listing {
		event, _ := listing[eventID].(map[string]any)
		if _, ok := event[matrixFullyReadType]; ok {
			t.Errorf("a fully read marker leaked into the receipts of %s: %v", eventID, event)
		}
	}
	if _, ok := listing[second]; !ok {
		t.Errorf("the read receipt vanished from the listing")
	}

	// The room may also be addressed by alias.
	alias := "#ledger:test.server"
	server.MustDo(http.MethodPut, "/_matrix/client/v3/directory/room/"+url.PathEscape(alias),
		alice.Token, map[string]any{"room_id": roomID}, http.StatusOK)
	if resp := server.MustDo(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape(alias)+"/receipts", bob.Token, nil,
		http.StatusOK); receiptListing(t, resp.Body, roomID) == nil {
		t.Errorf("reading receipts by alias: %s", resp.Raw)
	}

	// Permissions.
	if resp := server.Do(http.MethodGet, path, carol.Token, nil); resp.Status != http.StatusForbidden {
		t.Errorf("an outsider reading receipts: got %d %s, want 403", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v1/rooms/"+url.PathEscape("!nobody:test.server")+"/receipts",
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("receipts of an unknown room: got %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, path, "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated receipt listing: got %d, want 401", resp.Status)
	}
}

// matrixFullyReadType is the receipt type a read marker would use.
const matrixFullyReadType = "m.fully_read"
