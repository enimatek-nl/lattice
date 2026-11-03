package compat

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// TestProfileLifecycle covers the profile endpoints and the member events a
// display name change produces.
func TestProfileLifecycle(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	// A fresh account has an empty, publicly readable profile.
	fresh := server.MustDo(http.MethodGet, profilePath(alice.UserID), "", nil, http.StatusOK)
	if len(fresh.Body) != 0 {
		t.Errorf("a fresh profile should be empty: %s", fresh.Raw)
	}
	// A profile key that was never set is reported as missing.
	if resp := server.Do(http.MethodGet, profilePath(alice.UserID, "displayname"), "", nil); resp.Status != http.StatusNotFound {
		t.Errorf("reading an unset profile key returned %d %s, want 404", resp.Status, resp.Raw)
	}

	server.MustDo(http.MethodPut, profilePath(alice.UserID, "displayname"), alice.Token,
		map[string]any{"displayname": "Alice in Wonderland"}, http.StatusOK)
	server.MustDo(http.MethodPut, profilePath(alice.UserID, "avatar_url"), alice.Token,
		map[string]any{"avatar_url": "mxc://test.server/avatar"}, http.StatusOK)

	full := server.MustDo(http.MethodGet, profilePath(alice.UserID), "", nil, http.StatusOK)
	if full.String("displayname") != "Alice in Wonderland" {
		t.Errorf("profile displayname = %q: %s", full.String("displayname"), full.Raw)
	}
	if full.String("avatar_url") != "mxc://test.server/avatar" {
		t.Errorf("profile avatar_url = %q: %s", full.String("avatar_url"), full.Raw)
	}
	single := server.MustDo(http.MethodGet, profilePath(alice.UserID, "displayname"), "", nil, http.StatusOK)
	if single.String("displayname") != "Alice in Wonderland" {
		t.Errorf("displayname key = %q: %s", single.String("displayname"), single.Raw)
	}

	// Only the owner may write a profile.
	if resp := server.Do(http.MethodPut, profilePath(alice.UserID, "displayname"), bob.Token,
		map[string]any{"displayname": "Mallory"}); resp.Status == http.StatusOK {
		t.Errorf("another user wrote alice's profile: %s", resp.Raw)
	}
	if resp := server.Do(http.MethodPut, profilePath(bob.UserID, "displayname"), alice.Token,
		map[string]any{"displayname": "Mallory"}); resp.Status == http.StatusOK {
		t.Errorf("alice wrote bob's profile: %s", resp.Raw)
	}

	// A display name change refreshes the m.room.member events of every room
	// the user joined, so other members see the new name.
	joined := server.MustDo(http.MethodGet, "/_matrix/client/v3/rooms/"+roomID+"/joined_members", bob.Token, nil, http.StatusOK)
	members, _ := joined.Body["joined"].(map[string]any)
	entry, _ := members[alice.UserID].(map[string]any)
	if entry["display_name"] != "Alice in Wonderland" {
		t.Errorf("joined_members display_name = %v: %s", entry["display_name"], joined.Raw)
	}
	state := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+alice.UserID, bob.Token, nil, http.StatusOK)
	if state.String("displayname") != "Alice in Wonderland" {
		t.Errorf("member event displayname = %q: %s", state.String("displayname"), state.Raw)
	}
	if state.String("avatar_url") != "mxc://test.server/avatar" {
		t.Errorf("member event avatar_url = %q: %s", state.String("avatar_url"), state.Raw)
	}

	// Deleting a profile key hides it again.
	server.MustDo(http.MethodDelete, profilePath(alice.UserID, "displayname"), alice.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, profilePath(alice.UserID, "displayname"), "", nil); resp.Status != http.StatusNotFound {
		t.Errorf("a deleted profile key still reads: %d %s", resp.Status, resp.Raw)
	}
	after := server.MustDo(http.MethodGet, profilePath(alice.UserID), "", nil, http.StatusOK)
	if _, ok := after.Body["displayname"]; ok {
		t.Errorf("the deleted key is still in the profile: %s", after.Raw)
	}

	// Unknown users have no profile at all.
	if resp := server.Do(http.MethodGet, profilePath("@nobody:test.server"), "", nil); resp.Status != http.StatusNotFound {
		t.Errorf("profile of an unknown user: %d %s, want 404", resp.Status, resp.Raw)
	}
}

// TestAccountDataScopes covers global and room scoped account data.
func TestAccountDataScopes(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})

	// Reading account data that was never written is a 404.
	if resp := server.Do(http.MethodGet, accountDataPath(alice.UserID, "dev.lattice.pref"), alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("unset global account data: %d %s, want 404", resp.Status, resp.Raw)
	}

	server.MustDo(http.MethodPut, accountDataPath(alice.UserID, "dev.lattice.pref"), alice.Token,
		map[string]any{"theme": "dark"}, http.StatusOK)
	global := server.MustDo(http.MethodGet, accountDataPath(alice.UserID, "dev.lattice.pref"), alice.Token, nil, http.StatusOK)
	if global.String("theme") != "dark" {
		t.Errorf("global account data = %s", global.Raw)
	}

	// The same type may hold different content per room.
	server.MustDo(http.MethodPut, roomAccountDataPath(alice.UserID, roomID, "dev.lattice.pref"), alice.Token,
		map[string]any{"theme": "light"}, http.StatusOK)
	scoped := server.MustDo(http.MethodGet, roomAccountDataPath(alice.UserID, roomID, "dev.lattice.pref"), alice.Token, nil, http.StatusOK)
	if scoped.String("theme") != "light" {
		t.Errorf("room account data = %s", scoped.Raw)
	}
	if global.String("theme") != "dark" {
		t.Errorf("the room write clobbered the global value: %s", global.Raw)
	}

	// Account data is private to its owner.
	if resp := server.Do(http.MethodGet, accountDataPath(alice.UserID, "dev.lattice.pref"), bob.Token, nil); resp.Status == http.StatusOK {
		t.Errorf("bob read alice's account data: %s", resp.Raw)
	}
	if resp := server.Do(http.MethodPut, accountDataPath(alice.UserID, "dev.lattice.pref"), bob.Token,
		map[string]any{"theme": "hotdog"}); resp.Status == http.StatusOK {
		t.Errorf("bob wrote alice's account data: %s", resp.Raw)
	}

	// Both scopes reach the client through sync.
	first, resp := server.Sync(alice, "?timeout=0")
	if first.AccountData == nil {
		t.Fatalf("no global account data in sync: %s", resp.Raw)
	}
	if eventOfType(t, first.AccountData.Events, "dev.lattice.pref") == nil {
		t.Errorf("the global account data is missing from sync: %s", resp.Raw)
	}
	room, ok := first.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from sync: %s", resp.Raw)
	}
	if room.AccountData == nil || eventOfType(t, room.AccountData.Events, "dev.lattice.pref") == nil {
		t.Errorf("the room account data is missing from sync: %s", resp.Raw)
	}
	alice.NextBatch = first.NextBatch

	// Account data is not repeated on the next sync.
	again, resp := server.SyncSince(alice, 0)
	if again.AccountData != nil && eventOfType(t, again.AccountData.Events, "dev.lattice.pref") != nil {
		t.Errorf("account data was resent: %s", resp.Raw)
	}
	if room, ok := again.Rooms.Join[roomID]; ok && room.AccountData != nil &&
		eventOfType(t, room.AccountData.Events, "dev.lattice.pref") != nil {
		t.Errorf("room account data was resent: %s", resp.Raw)
	}

	// A body that is not a JSON object is refused.
	if resp := server.DoRaw(http.MethodPut, accountDataPath(alice.UserID, "dev.lattice.broken"), alice.Token,
		"application/json", []byte("not json")); resp.Status == http.StatusOK {
		t.Errorf("invalid account data JSON was accepted: %d %s", resp.Status, resp.Raw)
	}
}

// TestRoomTags covers the room tag endpoints.
func TestRoomTags(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})

	tags := server.MustDo(http.MethodGet, tagsPath(alice.UserID, roomID), alice.Token, nil, http.StatusOK)
	if listed, _ := tags.Body["tags"].(map[string]any); len(listed) != 0 {
		t.Errorf("a new room already has tags: %s", tags.Raw)
	}

	server.MustDo(http.MethodPut, tagsPath(alice.UserID, roomID, "u.favourite"), alice.Token,
		map[string]any{"order": 0.5}, http.StatusOK)
	server.MustDo(http.MethodPut, tagsPath(alice.UserID, roomID, "u.lowpriority"), alice.Token,
		map[string]any{}, http.StatusOK)

	listed := server.MustDo(http.MethodGet, tagsPath(alice.UserID, roomID), alice.Token, nil, http.StatusOK)
	body, _ := listed.Body["tags"].(map[string]any)
	favourite, _ := body["u.favourite"].(map[string]any)
	if favourite["order"] != 0.5 {
		t.Errorf("the tag order was not stored: %s", listed.Raw)
	}
	if _, ok := body["u.lowpriority"]; !ok {
		t.Errorf("the second tag is missing: %s", listed.Raw)
	}

	// Tags travel to the client as m.tag room account data.
	synced, resp := server.Sync(alice, "?timeout=0")
	room, ok := synced.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from sync: %s", resp.Raw)
	}
	if room.AccountData == nil {
		t.Fatalf("no room account data after tagging: %s", resp.Raw)
	}
	tagEvent := eventOfType(t, room.AccountData.Events, matrix.TypeTag)
	if tagEvent == nil {
		t.Fatalf("no m.tag event in the sync: %s", resp.Raw)
	}
	content := objectContent(t, *tagEvent)
	if _, ok := content["tags"].(map[string]any)["u.favourite"]; !ok {
		t.Errorf("the m.tag event lost the tag: %v", content)
	}

	// Tags belong to their owner.
	if resp := server.Do(http.MethodGet, tagsPath(alice.UserID, roomID), bob.Token, nil); resp.Status == http.StatusOK {
		t.Errorf("bob read alice's tags: %s", resp.Raw)
	}

	server.MustDo(http.MethodDelete, tagsPath(alice.UserID, roomID, "u.favourite"), alice.Token, nil, http.StatusOK)
	after := server.MustDo(http.MethodGet, tagsPath(alice.UserID, roomID), alice.Token, nil, http.StatusOK)
	body, _ = after.Body["tags"].(map[string]any)
	if _, ok := body["u.favourite"]; ok {
		t.Errorf("the deleted tag is still listed: %s", after.Raw)
	}
	if _, ok := body["u.lowpriority"]; !ok {
		t.Errorf("deleting one tag removed the others: %s", after.Raw)
	}
	if resp := server.Do(http.MethodDelete, tagsPath(alice.UserID, roomID, "u.favourite"), alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("deleting a missing tag: %d %s, want 404", resp.Status, resp.Raw)
	}
}

// TestPresence covers PUT and GET /presence/{userId}/status.
func TestPresence(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	// Users without a presence record read as offline.
	quiet := server.MustDo(http.MethodGet, presencePath(bob.UserID), alice.Token, nil, http.StatusOK)
	if quiet.String("presence") != "offline" {
		t.Errorf("presence of an idle user = %q: %s", quiet.String("presence"), quiet.Raw)
	}
	if quiet.Body["currently_active"] != false {
		t.Errorf("an idle user is currently_active: %s", quiet.Raw)
	}

	server.MustDo(http.MethodPut, presencePath(alice.UserID), alice.Token,
		map[string]any{"presence": "online", "status_msg": "reading"}, http.StatusOK)
	live := server.MustDo(http.MethodGet, presencePath(alice.UserID), bob.Token, nil, http.StatusOK)
	if live.String("presence") != "online" {
		t.Errorf("presence = %q: %s", live.String("presence"), live.Raw)
	}
	if live.String("status_msg") != "reading" {
		t.Errorf("status_msg = %q: %s", live.String("status_msg"), live.Raw)
	}
	if live.Body["currently_active"] != true {
		t.Errorf("an online user is not currently_active: %s", live.Raw)
	}
	if value, ok := live.Body["last_active_ago"].(float64); !ok || value < 0 {
		t.Errorf("last_active_ago is missing or negative: %s", live.Raw)
	}

	server.MustDo(http.MethodPut, presencePath(alice.UserID), alice.Token,
		map[string]any{"presence": "unavailable"}, http.StatusOK)
	if got := server.MustDo(http.MethodGet, presencePath(alice.UserID), alice.Token, nil, http.StatusOK); got.String("presence") != "unavailable" {
		t.Errorf("presence after going unavailable = %q: %s", got.String("presence"), got.Raw)
	}

	for _, body := range []map[string]any{
		{"presence": "sleeping"},
		{"presence": ""},
	} {
		if resp := server.Do(http.MethodPut, presencePath(alice.UserID), alice.Token, body); resp.Status != http.StatusBadRequest {
			t.Errorf("presence %v: got %d %s, want 400", body, resp.Status, resp.Raw)
		}
	}

	// Presence may only be written for oneself.
	if resp := server.Do(http.MethodPut, presencePath(bob.UserID), alice.Token,
		map[string]any{"presence": "online"}); resp.Status == http.StatusOK {
		t.Errorf("alice set bob's presence: %s", resp.Raw)
	}
	// Remote users are unknown to this server.
	if resp := server.Do(http.MethodGet, presencePath("@someone:elsewhere.example"), alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("presence of a remote user: %d %s, want 404", resp.Status, resp.Raw)
	}
}

// TestReadReceipts covers POST /rooms/{roomId}/receipt/{receiptType}/{eventId}
// and the m.receipt ephemeral event it produces.
func TestReadReceipts(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	bob.NextBatch = server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", bob.Token, nil, http.StatusOK).String("next_batch")

	eventID := server.Send(alice, roomID, "txn-receipt", "read me")

	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read", eventID), bob.Token,
		map[string]any{"ts": 1700000000000}, http.StatusOK)
	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read.private", eventID), bob.Token, nil, http.StatusOK)

	synced, resp := server.SyncSince(bob, 0)
	room, ok := synced.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from the receipt sync: %s", resp.Raw)
	}
	if room.Ephemeral == nil {
		t.Fatalf("no ephemeral events after a receipt: %s", resp.Raw)
	}
	receipt := eventOfType(t, room.Ephemeral.Events, matrix.TypeReceipt)
	if receipt == nil {
		t.Fatalf("no m.receipt event in the sync: %s", resp.Raw)
	}
	content := objectContent(t, *receipt)
	perEvent, _ := content[eventID].(map[string]any)
	public, _ := perEvent["m.read"].(map[string]any)
	byUser, _ := public[bob.UserID].(map[string]any)
	if stamp, ok := byUser["ts"].(float64); !ok || stamp <= 0 {
		t.Errorf("the m.read receipt carries no timestamp: %v", perEvent)
	}
	if _, ok := perEvent["m.read.private"]; !ok {
		t.Errorf("the private receipt is missing: %v", perEvent)
	}

	// A third party may not post receipts in a room it is not part of.
	carol := server.Register("carol", "kettle")
	if resp := server.Do(http.MethodPost, receiptPath(roomID, "m.read", eventID), carol.Token, nil); resp.Status == http.StatusOK {
		t.Errorf("a stranger posted a receipt: %d %s", resp.Status, resp.Raw)
	}
	// Only the receipt types this server understands are accepted.
	if resp := server.Do(http.MethodPost, receiptPath(roomID, "m.unsupported", eventID), bob.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("unknown receipt type: %d %s, want 400", resp.Status, resp.Raw)
	}
	// An event from another room is not a receipt target.
	other := server.CreateRoom(alice, map[string]any{"preset": "private_chat"})
	otherEvent := server.Send(alice, other, "txn-other", "elsewhere")
	if resp := server.Do(http.MethodPost, receiptPath(roomID, "m.read", otherEvent), bob.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("a receipt for an event in another room: %d %s, want 404", resp.Status, resp.Raw)
	}
}

// TestReceiptsReachTheOtherSide pins who a read receipt is delivered to. A receipt is not
// a note to self: its purpose is to tell the author of a message that somebody read it,
// and it is also how a receipt that arrived from another homeserver reaches a client. A
// server that reports only the reader's own receipts leaves every other member of the
// room unable to see anybody reading anything.
//
// The same sync also carries the typing indicator of that reader, which pins the other
// half of the bug: ephemeral events of one kind must not replace the ones of another.
func TestReceiptsReachTheOtherSide(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	aliceSince := server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", alice.Token, nil, http.StatusOK).String("next_batch")

	message := server.Send(alice, roomID, "txn-reach-1", "read me")
	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read", message), bob.Token,
		map[string]any{"ts": 1700000004000}, http.StatusOK)
	server.MustDo(http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/typing/"+bob.UserID,
		bob.Token, map[string]any{"typing": true, "timeout": 30000}, http.StatusOK)

	after, resp := server.Sync(alice, "?since="+aliceSince+"&timeout=0")
	room, ok := after.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from the sync: %s", resp.Raw)
	}
	if room.Ephemeral == nil {
		t.Fatalf("the sync of the message's author carries no ephemeral events: %s", resp.Raw)
	}
	var typing, receipts *matrix.Event
	for i, event := range room.Ephemeral.Events {
		switch event.Type {
		case matrix.TypeTyping:
			typing = &room.Ephemeral.Events[i]
		case matrix.TypeReceipt:
			receipts = &room.Ephemeral.Events[i]
		}
	}
	if typing == nil {
		t.Errorf("the author never learned that bob is typing: %s", resp.Raw)
	} else if users, _ := objectContent(t, *typing)["user_ids"].([]any); len(users) == 0 || users[0] != bob.UserID {
		t.Errorf("the typing event names %v, want bob", objectContent(t, *typing)["user_ids"])
	}
	if receipts == nil {
		t.Fatalf("the author never learned that bob read the message: %s", resp.Raw)
	}
	perEvent, _ := objectContent(t, *receipts)[message].(map[string]any)
	public, _ := perEvent["m.read"].(map[string]any)
	if _, ok := public[bob.UserID]; !ok {
		t.Errorf("the receipt of bob's is missing from the author's sync: %v", perEvent)
	}
}

// TestReadMarkers covers POST /rooms/{roomId}/read_markers and the m.fully_read
// account data it writes.
func TestReadMarkers(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	start := server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", bob.Token, nil, http.StatusOK).String("next_batch")

	first := server.Send(alice, roomID, "txn-mark-1", "hello bob")
	latest := server.Send(alice, roomID, "txn-mark-2", "hello again bob")

	before, resp := server.Sync(bob, "?since="+start+"&timeout=0")
	room, ok := before.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from the sync: %s", resp.Raw)
	}
	if room.UnreadNotifications.NotificationCount < 2 {
		t.Fatalf("the mentions produced no unread notifications: %s", resp.Raw)
	}

	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", bob.Token,
		map[string]any{"fully_read": latest}, http.StatusOK)

	marker := server.MustDo(http.MethodGet, roomAccountDataPath(bob.UserID, roomID, matrix.TypeFullyRead), bob.Token, nil, http.StatusOK)
	if marker.String("event_id") != latest {
		t.Errorf("the m.fully_read marker points at %q: %s", marker.String("event_id"), marker.Raw)
	}

	after, resp := server.Sync(bob, "?since="+start+"&timeout=0")
	room, ok = after.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room disappeared after the marker: %s", resp.Raw)
	}
	if room.UnreadNotifications.NotificationCount != 0 {
		t.Errorf("the read marker did not clear the unread count: %s", resp.Raw)
	}
	if room.AccountData == nil || eventOfType(t, room.AccountData.Events, matrix.TypeFullyRead) == nil {
		t.Errorf("the read marker is not in the sync account data: %s", resp.Raw)
	}

	// A marker may also move the read receipt.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", bob.Token,
		map[string]any{"read_receipt": first}, http.StatusOK)
	receipted, resp := server.Sync(bob, "?since="+start+"&timeout=0")
	if room, ok := receipted.Rooms.Join[roomID]; !ok || room.Ephemeral == nil ||
		eventOfType(t, room.Ephemeral.Events, matrix.TypeReceipt) == nil {
		t.Errorf("the read_receipt of a marker produced no receipt: %s", resp.Raw)
	}

	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", bob.Token, map[string]any{}); resp.Status != http.StatusBadRequest {
		t.Errorf("an empty read_markers body: %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", bob.Token,
		map[string]any{"fully_read": "$nope"}); resp.Status != http.StatusNotFound {
		t.Errorf("a marker for an unknown event: %d %s, want 404", resp.Status, resp.Raw)
	}
}

// TestNotificationsForMentions checks that a mention highlights a user while an
// ordinary message only notifies, and that both count towards the unread count.
func TestNotificationsForMentions(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	carol := server.Register("carol", "kettle")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, carol.Token, nil, http.StatusOK)
	start := server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", bob.Token, nil, http.StatusOK).String("next_batch")

	mentioned := server.Send(alice, roomID, "note-1", "hello bob")
	quiet := server.Send(alice, roomID, "note-2", "nothing to see here")

	listed := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", bob.Token, nil, http.StatusOK)
	entries, _ := listed.Body["notifications"].([]any)
	if len(entries) != 2 {
		t.Fatalf("bob has %d notifications, want two: %s", len(entries), listed.Raw)
	}
	// Every entry carries the room, the event and its actions.
	byEvent := map[string]map[string]any{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["room_id"] != roomID {
			t.Errorf("notification room_id = %v: %s", entry["room_id"], listed.Raw)
		}
		event, _ := entry["event"].(map[string]any)
		id, _ := event["event_id"].(string)
		byEvent[id] = entry
		if !actionsOf(entry["actions"]).notifies() {
			t.Errorf("the notification actions do not notify: %v", entry["actions"])
		}
	}
	if len(byEvent) != 2 {
		t.Errorf("the notifications cover %d distinct events, want 2: %s", len(byEvent), listed.Raw)
	}
	// Lattice lists notifications oldest first. The specification does not pin an
	// order, so the test pins this one and turns a change into a decision.
	oldest, _ := entries[0].(map[string]any)
	oldestEvent, _ := oldest["event"].(map[string]any)
	if oldestEvent["event_id"] != mentioned {
		t.Errorf("the oldest notification should be %s: %s", mentioned, listed.Raw)
	}
	quietEntry := byEvent[quiet]
	if quietEntry == nil {
		t.Errorf("the plain message produced no notification, want %s: %s", quiet, listed.Raw)
	} else if actionsOf(quietEntry["actions"]).hasTweak("highlight") {
		t.Errorf("an ordinary message highlighted: %v", quietEntry["actions"])
	}
	mentionEntry := byEvent[mentioned]
	if mentionEntry == nil {
		t.Errorf("the mention produced no notification, want %s: %s", mentioned, listed.Raw)
	} else if !actionsOf(mentionEntry["actions"]).hasTweak("highlight") {
		t.Errorf("a direct mention should highlight: %v", mentionEntry["actions"])
	}

	// The sender never notifies themselves; an unmentioned member is notified
	// but not highlighted.
	if resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", alice.Token, nil, http.StatusOK); lenList(resp) != 0 {
		t.Errorf("the sender received a notification: %s", resp.Raw)
	}
	if resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", carol.Token, nil, http.StatusOK); lenList(resp) != 2 {
		t.Errorf("an unmentioned member received %d notifications, want two: %s", lenList(resp), resp.Raw)
	}

	// The unread count of the room reflects the notification.
	synced, resp := server.Sync(bob, "?since="+start+"&timeout=0")
	room, ok := synced.Rooms.Join[roomID]
	if !ok {
		t.Fatalf("the room is missing from the sync: %s", resp.Raw)
	}
	if room.UnreadNotifications.NotificationCount != 2 {
		t.Errorf("notification_count = %d, want 2: %s", room.UnreadNotifications.NotificationCount, resp.Raw)
	}
	if room.UnreadNotifications.HighlightCount != 1 {
		t.Errorf("highlight_count = %d: %s", room.UnreadNotifications.HighlightCount, resp.Raw)
	}

	// A rule change reaches the next event: bob highlights his display name.
	server.MustDo(http.MethodPut, profilePath(bob.UserID, "displayname"), bob.Token,
		map[string]any{"displayname": "Bobby"}, http.StatusOK)
	server.Send(alice, roomID, "note-3", "nice work Bobby")
	if got := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", bob.Token, nil, http.StatusOK); lenList(got) != 3 {
		t.Errorf("the display name rule did not fire: %s", got.Raw)
	}
	if got := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", carol.Token, nil, http.StatusOK); lenList(got) != 3 {
		t.Errorf("carol should have three notifications: %s", got.Raw)
	}
}

// notificationEntries decodes the notifications array of a response.
func notificationEntries(t *testing.T, resp Response) []map[string]any {
	t.Helper()
	raw, _ := resp.Body["notifications"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if decoded, ok := entry.(map[string]any); ok {
			out = append(out, decoded)
		}
	}
	return out
}

// notificationEventID returns the event id a notification entry carries.
func notificationEventID(entry map[string]any) string {
	event, _ := entry["event"].(map[string]any)
	id, _ := event["event_id"].(string)
	return id
}

// TestNotificationsOrderAndRead pins the pagination order of /notifications.
// The specification defines a token to continue from but not the direction, so
// Lattice stores notifications in arrival order and pages forward from the row
// id it hands back: oldest first, and a `from` token always continues downwards.
// Changing this is a decision to make here, not an accident to notice later.
// The read flag is the mandatory field the same endpoint asks for, computed from
// how far the requester has read in that room.
func TestNotificationsOrderAndRead(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)

	first := server.Send(alice, roomID, "order-1", "one")
	second := server.Send(alice, roomID, "order-2", "two")
	// A mention, so this one also carries the highlight tweak.
	third := server.Send(alice, roomID, "order-3", "three bob")

	page := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications?limit=2", bob.Token, nil, http.StatusOK)
	entries := notificationEntries(t, page)
	if len(entries) != 2 {
		t.Fatalf("the first page holds %d notifications, want two: %s", len(entries), page.Raw)
	}
	if got := []string{notificationEventID(entries[0]), notificationEventID(entries[1])}; got[0] != first || got[1] != second {
		t.Errorf("the first page = %v, want oldest first (%s, %s)", got, first, second)
	}
	// Timestamps never go backwards across a page.
	for i := 1; i < len(entries); i++ {
		before, _ := entries[i-1]["ts"].(float64)
		after, _ := entries[i]["ts"].(float64)
		if after < before {
			t.Errorf("notifications at %d and %d are out of order: %v then %v", i-1, i, before, after)
		}
	}
	token := page.String("next_token")
	if token == "" {
		t.Fatalf("a full page carries no next_token: %s", page.Raw)
	}
	tail := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications?limit=2&from="+token,
		bob.Token, nil, http.StatusOK)
	rest := notificationEntries(t, tail)
	if len(rest) != 1 || notificationEventID(rest[0]) != third {
		t.Errorf("the page after %s = %s, want only %s", token, tail.Raw, third)
	}
	if tail.String("next_token") != "" {
		t.Errorf("the last page should not carry a next_token: %s", tail.Raw)
	}

	// Nothing has been read yet, and the field is present on every entry.
	for _, entry := range entries {
		read, ok := entry["read"].(bool)
		if !ok {
			t.Fatalf("a notification has no read flag: %v", entry)
		}
		if read {
			t.Errorf("an unread notification says it was read: %v", entry)
		}
	}

	// A read receipt up to the second event marks the first two as read.
	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read", second), bob.Token, nil, http.StatusOK)
	listed := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", bob.Token, nil, http.StatusOK)
	flags := map[string]bool{}
	for _, entry := range notificationEntries(t, listed) {
		flags[notificationEventID(entry)] = entry["read"] == true
	}
	if !flags[first] || !flags[second] {
		t.Errorf("the receipt did not mark the notifications it covers as read: %s", listed.Raw)
	}
	if flags[third] {
		t.Errorf("the receipt marked a notification beyond it as read: %s", listed.Raw)
	}

	// The fully read marker counts too.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/rooms/"+roomID+"/read_markers", bob.Token,
		map[string]any{"m.fully_read": third}, http.StatusOK)
	listed = server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications", bob.Token, nil, http.StatusOK)
	for _, entry := range notificationEntries(t, listed) {
		if entry["read"] != true {
			t.Errorf("after the read marker %s is still unread: %s",
				notificationEventID(entry), listed.Raw)
		}
	}

	// only=highlight is the one filter the specification defines.
	only := server.MustDo(http.MethodGet, "/_matrix/client/v3/notifications?only=highlight", bob.Token,
		nil, http.StatusOK)
	highlighted := notificationEntries(t, only)
	if len(highlighted) != 1 || notificationEventID(highlighted[0]) != third {
		t.Errorf("only=highlight = %s, want only the mention", only.Raw)
	}
	// An unknown value is a client error rather than a silently ignored filter.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/notifications?only=everything",
		bob.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("only=everything: got %d %s, want 400", resp.Status, resp.Raw)
	}
}

// TestPushRuleCRUD covers the push rule endpoints, with and without the
// deprecated "global" scope segment.
func TestPushRuleCRUD(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	all := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/", alice.Token, nil, http.StatusOK)
	global, _ := all.Body["global"].(map[string]any)
	for _, kind := range []string{"override", "content", "room", "sender", "underride"} {
		rules, ok := global[kind].([]any)
		if !ok {
			t.Fatalf("the rule set has no %s list: %s", kind, all.Raw)
		}
		if len(rules) == 0 && kind != "room" && kind != "sender" {
			t.Errorf("the default %s rules are missing: %s", kind, all.Raw)
		}
	}

	scoped := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/global/override", alice.Token, nil, http.StatusOK)
	if len(scopedList(scoped.Body, "override")) == 0 {
		t.Errorf("the scoped override listing is empty: %s", scoped.Raw)
	}

	master := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/global/override/.m.rule.master", alice.Token, nil, http.StatusOK)
	if master.String("rule_id") != ".m.rule.master" || master.Body["default"] != true || master.Body["enabled"] != false {
		t.Errorf("the master rule reads %s", master.Raw)
	}

	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent", alice.Token,
		map[string]any{
			"conditions": []any{map[string]any{"kind": "event_match", "key": "content.body", "pattern": "urgent"}},
			"actions":    []any{"notify", map[string]any{"set_tweak": "sound", "value": "default"}},
		}, http.StatusOK)
	created := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent", alice.Token, nil, http.StatusOK)
	if created.Body["default"] != false || created.Body["enabled"] != true {
		t.Errorf("a new rule must be enabled and not default: %s", created.Raw)
	}
	if actions := actionsOf(created.Body["actions"]); len(actions) != 2 || !actions.hasTweak("sound") {
		t.Errorf("the rule actions were not stored: %s", created.Raw)
	}
	if conditions, _ := created.Body["conditions"].([]any); len(conditions) != 1 {
		t.Errorf("the rule conditions were not stored: %s", created.Raw)
	}

	// The pattern shorthand turns into a content.body condition.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/content/dev.lattice.pings", alice.Token,
		map[string]any{"pattern": "ping", "actions": []any{"notify"}}, http.StatusOK)
	shorthand := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/global/content/dev.lattice.pings", alice.Token, nil, http.StatusOK)
	if shorthand.String("pattern") != "ping" {
		t.Errorf("the pattern shorthand was not kept: %s", shorthand.Raw)
	}

	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent/enabled", alice.Token,
		map[string]any{"enabled": false}, http.StatusOK)
	if got := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent", alice.Token, nil, http.StatusOK); got.Body["enabled"] != false {
		t.Errorf("disabling a rule did not stick: %s", got.Raw)
	}
	server.MustDo(http.MethodPut, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent/actions", alice.Token,
		map[string]any{"actions": []any{"dont_notify"}}, http.StatusOK)
	if got := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent", alice.Token, nil, http.StatusOK); actionsOf(got.Body["actions"])[0] != "dont_notify" {
		t.Errorf("replacing the actions did not stick: %s", got.Raw)
	}

	// A rule without actions is a client error.
	if resp := server.Do(http.MethodPut, "/_matrix/client/v3/pushrules/global/override/dev.lattice.broken", alice.Token,
		map[string]any{"pattern": "x"}); resp.Status != http.StatusBadRequest {
		t.Errorf("a rule without actions: %d %s, want 400", resp.Status, resp.Raw)
	}
	// Unknown rules, kinds and paths are refused.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/pushrules/global/override/dev.lattice.nothing", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("reading an unknown rule: %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/pushrules/global/nonsense", alice.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("reading an unknown kind: %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent/nonsense", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("reading an unknown rule sub path: %d %s, want 404", resp.Status, resp.Raw)
	}

	server.MustDo(http.MethodDelete, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent", alice.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/pushrules/global/override/dev.lattice.urgent", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("the deleted rule still reads: %d %s", resp.Status, resp.Raw)
	}
	// Default rules may be removed as well.
	server.MustDo(http.MethodDelete, "/_matrix/client/v3/pushrules/global/underride/.m.rule.encrypted", alice.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/pushrules/global/underride/.m.rule.encrypted", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("the deleted default rule still reads: %d %s", resp.Status, resp.Raw)
	}
	// Rules are private to their owner.
	bob := server.Register("bob", "rubberduck")
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/pushrules/global/content/dev.lattice.pings", bob.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("bob read alice's rule: %d %s", resp.Status, resp.Raw)
	}
}

// TestPushers covers POST /pushers/set and GET /pushers.
func TestPushers(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")

	if resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil, http.StatusOK); lenPushers(resp) != 0 {
		t.Errorf("a new account already has pushers: %s", resp.Raw)
	}

	pusher := map[string]any{
		"pushkey":          "device-token",
		"app_id":           "dev.lattice.test",
		"app_display_name": "Lattice Test",
		"device_id":        alice.DeviceID,
		"kind":             "http",
		"lang":             "en",
		"data":             map[string]any{"url": "https://push.example/lattice"},
	}
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token, pusher, http.StatusOK)

	listed := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil, http.StatusOK)
	entries, _ := listed.Body["pushers"].([]any)
	if len(entries) != 1 {
		t.Fatalf("pushers = %s", listed.Raw)
	}
	stored, _ := entries[0].(map[string]any)
	if stored["pushkey"] != "device-token" || stored["app_id"] != "dev.lattice.test" {
		t.Errorf("the pusher identity was lost: %s", listed.Raw)
	}
	if stored["kind"] != "http" || stored["app_display_name"] != "Lattice Test" {
		t.Errorf("the pusher details were lost: %s", listed.Raw)
	}
	data, _ := stored["data"].(map[string]any)
	if data["url"] != "https://push.example/lattice" {
		t.Errorf("the pusher data was lost: %s", listed.Raw)
	}

	// The same pushkey and app id update the entry instead of adding one.
	pusher["app_display_name"] = "Lattice Test Two"
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token, pusher, http.StatusOK)
	if got := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil, http.StatusOK); lenPushers(got) != 1 {
		t.Errorf("re-setting a pushkey added a second pusher: %s", got.Raw)
	} else if entries, _ := got.Body["pushers"].([]any); len(entries) == 1 {
		first, _ := entries[0].(map[string]any)
		if first["app_display_name"] != "Lattice Test Two" {
			t.Errorf("the pusher was not updated: %s", got.Raw)
		}
	}

	// Pushers are private to their owner.
	if resp := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", bob.Token, nil, http.StatusOK); lenPushers(resp) != 0 {
		t.Errorf("bob sees alice's pusher: %s", resp.Raw)
	}

	// An incomplete pusher is a client error.
	if resp := server.Do(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token,
		map[string]any{"app_id": "dev.lattice.test"}); resp.Status != http.StatusBadRequest {
		t.Errorf("a pusher without a pushkey: %d %s, want 400", resp.Status, resp.Raw)
	}

	// A body that only carries the identity deletes the pusher.
	server.MustDo(http.MethodPost, "/_matrix/client/v3/pushers/set", alice.Token,
		map[string]any{"pushkey": "device-token", "app_id": "dev.lattice.test"}, http.StatusOK)
	if got := server.MustDo(http.MethodGet, "/_matrix/client/v3/pushers", alice.Token, nil, http.StatusOK); lenPushers(got) != 0 {
		t.Errorf("the pusher survived its deletion: %s", got.Raw)
	}
}

// TestMediaUploadAndDownload covers uploads, downloads, the reserved upload
// flow and thumbnails.
func TestMediaUploadAndDownload(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	payload := []byte("the quick brown fox jumps over the lazy dog")
	uploaded := server.DoRaw(http.MethodPost, "/_matrix/media/v3/upload?filename=note.txt", alice.Token,
		"text/plain", payload)
	if uploaded.Status != http.StatusOK {
		t.Fatalf("upload failed: %d %s", uploaded.Status, uploaded.Raw)
	}
	mediaID := mustMediaID(t, uploaded.String("content_uri"))

	downloaded := server.Do(http.MethodGet, "/_matrix/media/v3/download/test.server/"+mediaID, "", nil)
	if downloaded.Status != http.StatusOK {
		t.Fatalf("download failed: %d %s", downloaded.Status, downloaded.Raw)
	}
	if !bytes.Equal(downloaded.Raw, payload) {
		t.Errorf("the download returned %q, want the uploaded bytes", downloaded.Raw)
	}
	if got := downloaded.Headers.Get("Content-Type"); got != "text/plain" {
		t.Errorf("download content type = %q", got)
	}
	if got := downloaded.Headers.Get("Content-Disposition"); !strings.Contains(got, "note.txt") {
		t.Errorf("download content disposition = %q", got)
	}

	// Authenticated media, with and without a file name suffix.
	for _, path := range []string{
		"/_matrix/client/v1/media/download/test.server/" + mediaID,
		"/_matrix/client/v1/media/download/test.server/" + mediaID + "/note.txt",
	} {
		if resp := server.Do(http.MethodGet, path, alice.Token, nil); resp.Status != http.StatusOK {
			t.Errorf("authenticated download of %s: %d %s", path, resp.Status, resp.Raw)
		}
	}

	// A download that asks to be saved under another name is labelled with that name, on the
	// unauthenticated endpoint as on the authenticated one. The name is the requester's suggestion:
	// it does not decide which bytes are served. With no suffix, the uploader's name is used.
	for path, want := range map[string]string{
		"/_matrix/media/v3/download/test.server/" + mediaID:                   "note.txt",
		"/_matrix/media/v3/download/test.server/" + mediaID + "/other.txt":    "other.txt",
		"/_matrix/client/v1/media/download/test.server/" + mediaID + "/a b;c": "a b;c",
	} {
		resp := server.Do(http.MethodGet, path, alice.Token, nil)
		if resp.Status != http.StatusOK {
			t.Errorf("download of %s: %d %s", path, resp.Status, resp.Raw)
			continue
		}
		disposition := resp.Headers.Get("Content-Disposition")
		if !strings.Contains(disposition, "filename=\""+want+"\"") {
			t.Errorf("%s answered Content-Disposition %q, want the name %q",
				path, disposition, want)
		}
	}

	// Unknown media ids and foreign origins are not served here.
	if resp := server.Do(http.MethodGet, "/_matrix/media/v3/download/test.server/nosuchid", "", nil); resp.Status != http.StatusNotFound {
		t.Errorf("an unknown media id: %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/media/v3/download/elsewhere.example/"+mediaID, "", nil); resp.Status != http.StatusNotFound {
		t.Errorf("a foreign media id: %d %s, want 404", resp.Status, resp.Raw)
	}

	config := server.MustDo(http.MethodGet, "/_matrix/media/v3/config", "", nil, http.StatusOK)
	if size, ok := config.Body["m.upload.size"].(float64); !ok || size <= 0 {
		t.Errorf("the media config has no upload size: %s", config.Raw)
	}

	// Reserved uploads: create mints an id, the PUT stores the bytes. Element and
	// matrix-js-sdk use this flow for every upload, including the room avatar, so
	// the create response has to promise where to send the bytes and for how long
	// the id waits for them.
	created := server.MustDo(http.MethodPost, "/_matrix/media/v3/create", alice.Token,
		map[string]any{"name": "note.txt", "type": "text/plain", "size": len(payload)}, http.StatusOK)
	reserved := mustMediaID(t, created.String("content_uri"))
	if created.String("upload_url") == "" {
		t.Errorf("the reservation does not say where to upload: %s", created.Raw)
	}
	if expires, ok := created.Body["expires_ms"].(float64); !ok || expires <= 0 {
		t.Errorf("the reservation has no expiry: %s", created.Raw)
	}

	// Committing before the bytes arrive waits for the upload, then reports the id
	// as unknown rather than hanging on a client that never shows up.
	tooEarly := server.Do(http.MethodPost,
		"/_matrix/media/v3/commit/test.server/"+reserved+"?timeout=250", alice.Token, map[string]any{})
	if tooEarly.Status != http.StatusNotFound {
		t.Errorf("committing an empty reservation: got %d %s, want 404", tooEarly.Status, tooEarly.Raw)
	}

	if resp := server.DoRaw(http.MethodPut, "/_matrix/media/v3/upload/test.server/"+reserved, alice.Token,
		"text/plain", payload); resp.Status != http.StatusOK {
		t.Fatalf("the reserved upload failed: %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/media/v3/commit/test.server/"+reserved, alice.Token,
		map[string]any{}); resp.Status != http.StatusOK {
		t.Errorf("committing the upload: got %d %s, want 200", resp.Status, resp.Raw)
	}
	stored := server.Do(http.MethodGet, "/_matrix/media/v3/download/test.server/"+reserved, "", nil)
	if !bytes.Equal(stored.Raw, payload) {
		t.Errorf("the reserved media holds %q", stored.Raw)
	}
	if resp := server.DoRaw(http.MethodPut, "/_matrix/media/v3/upload/test.server/"+reserved, alice.Token,
		"text/plain", payload); resp.Status != http.StatusConflict {
		t.Errorf("reusing a media id: %d %s, want 409", resp.Status, resp.Raw)
	}
	// A reservation may not be committed by another user once it has content.
	bob := server.Register("bob", "rubberduck")
	other := server.MustDo(http.MethodPost, "/_matrix/media/v3/create", bob.Token,
		map[string]any{"name": "other.txt", "type": "text/plain", "size": 3}, http.StatusOK)
	otherID := mustMediaID(t, other.String("content_uri"))
	if resp := server.Do(http.MethodPost, "/_matrix/media/v3/commit/test.server/"+otherID, alice.Token,
		map[string]any{}); resp.Status != http.StatusNotFound {
		t.Errorf("committing a reservation with no content: got %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.DoRaw(http.MethodPut, "/_matrix/media/v3/upload/test.server/"+otherID, bob.Token,
		"text/plain", []byte("abc")); resp.Status != http.StatusOK {
		t.Fatalf("bob's reserved upload failed: %d %s", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/media/v3/commit/test.server/"+otherID, alice.Token,
		map[string]any{}); resp.Status != http.StatusForbidden {
		t.Errorf("committing another user's upload: got %d %s, want 403", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodPost, "/_matrix/media/v3/commit/test.server/"+otherID, bob.Token,
		map[string]any{}); resp.Status != http.StatusOK {
		t.Errorf("bob committing his own upload: got %d %s, want 200", resp.Status, resp.Raw)
	}
	// A size beyond the limit is refused before an id is minted.
	if resp := server.Do(http.MethodPost, "/_matrix/media/v3/create", alice.Token,
		map[string]any{"name": "huge.bin", "type": "application/octet-stream",
			"size": 1 << 40}); resp.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("reserving more than the limit: got %d %s, want 413", resp.Status, resp.Raw)
	}
}

// TestMediaThumbnails covers thumbnail generation for image content.
func TestMediaThumbnails(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	mediaID := mustMediaID(t, server.DoRaw(http.MethodPost, "/_matrix/media/v3/upload", alice.Token,
		"image/png", testPNG(t, 40, 20)).String("content_uri"))

	scaled := server.Do(http.MethodGet, "/_matrix/media/v3/thumbnail/test.server/"+mediaID+"?width=10&height=10&method=scale", "", nil)
	if scaled.Status != http.StatusOK {
		t.Fatalf("thumbnail failed: %d %s", scaled.Status, scaled.Raw)
	}
	if got := scaled.Headers.Get("Content-Type"); got != "image/png" {
		t.Errorf("the thumbnail is a %q", got)
	}
	if bounds := pngBounds(t, scaled.Raw); bounds.Dx() != 10 || bounds.Dy() != 5 {
		t.Errorf("the scaled thumbnail is %v, want 10x5 from a 40x20 source", bounds)
	}

	cropped := server.Do(http.MethodGet, "/_matrix/media/v3/thumbnail/test.server/"+mediaID+"?width=8&height=8&method=crop", "", nil)
	if bounds := pngBounds(t, cropped.Raw); bounds.Dx() != 8 || bounds.Dy() != 8 {
		t.Errorf("the cropped thumbnail is %v, want 8x8", bounds)
	}

	// The authenticated variant reaches the same media.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v1/media/thumbnail/test.server/"+mediaID, alice.Token, nil); resp.Status != http.StatusOK {
		t.Errorf("the authenticated thumbnail failed: %d %s", resp.Status, resp.Raw)
	}

	// Non-image content has no thumbnail, and bad sizes are a client error.
	text := mustMediaID(t, server.DoRaw(http.MethodPost, "/_matrix/media/v3/upload", alice.Token,
		"text/plain", []byte("plain text")).String("content_uri"))
	if resp := server.Do(http.MethodGet, "/_matrix/media/v3/thumbnail/test.server/"+text, "", nil); resp.Status != http.StatusNotFound {
		t.Errorf("a thumbnail of text content: %d %s, want 404", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/media/v3/thumbnail/test.server/"+mediaID+"?width=zero", "", nil); resp.Status != http.StatusBadRequest {
		t.Errorf("a bad thumbnail width: %d %s, want 400", resp.Status, resp.Raw)
	}
}

// profilePath builds a path below /profile, optionally for a single key.
func profilePath(userID string, key ...string) string {
	path := "/_matrix/client/v3/profile/" + userID
	if len(key) > 0 {
		path += "/" + key[0]
	}
	return path
}

// accountDataPath builds the global account data path.
func accountDataPath(userID, typ string) string {
	return "/_matrix/client/v3/user/" + userID + "/account_data/" + typ
}

// roomAccountDataPath builds the room scoped account data path.
func roomAccountDataPath(userID, roomID, typ string) string {
	return "/_matrix/client/v3/user/" + userID + "/rooms/" + roomID + "/account_data/" + typ
}

// tagsPath builds the room tag path, optionally for a single tag.
func tagsPath(userID, roomID string, tag ...string) string {
	path := "/_matrix/client/v3/user/" + userID + "/rooms/" + roomID + "/tags"
	if len(tag) > 0 {
		path += "/" + tag[0]
	}
	return path
}

// presencePath builds the presence status path of a user.
func presencePath(userID string) string {
	return "/_matrix/client/v3/presence/" + userID + "/status"
}

// receiptPath builds the receipt path of a room.
func receiptPath(roomID, receiptType, eventID string) string {
	return "/_matrix/client/v3/rooms/" + roomID + "/receipt/" + receiptType + "/" + eventID
}

// eventOfType returns the first event of a type, or nil.
func eventOfType(t *testing.T, events []matrix.Event, typ string) *matrix.Event {
	t.Helper()
	for i := range events {
		if events[i].Type == typ {
			return &events[i]
		}
	}
	return nil
}

// objectContent decodes an event content block into a generic object.
func objectContent(t *testing.T, event matrix.Event) matrix.Object {
	t.Helper()
	var content matrix.Object
	if err := matrix.DecodeContent(event.Content, &content); err != nil {
		t.Fatalf("decoding the content of %s: %v", event.Type, err)
	}
	return content
}

// actionList is a push rule action list with the queries the tests need.
type actionList []any

// actionsOf reads an action list out of a decoded JSON value.
func actionsOf(value any) actionList {
	entries, _ := value.([]any)
	return actionList(entries)
}

// notifies reports whether the list asks the client to notify.
func (a actionList) notifies() bool {
	for _, action := range a {
		if entry, ok := action.(string); ok && entry == "notify" {
			return true
		}
	}
	return false
}

// hasTweak reports whether the list sets the named tweak.
func (a actionList) hasTweak(name string) bool {
	for _, action := range a {
		if entry, ok := action.(map[string]any); ok && entry["set_tweak"] == name {
			return true
		}
	}
	return false
}

// scopedList reads the rule list of one kind out of a push rules response body.
func scopedList(body map[string]any, kind string) []any {
	global, _ := body["global"].(map[string]any)
	rules, _ := global[kind].([]any)
	return rules
}

// lenList counts the entries of a notification listing.
func lenList(resp Response) int {
	entries, _ := resp.Body["notifications"].([]any)
	return len(entries)
}

// lenPushers counts the entries of a pusher listing.
func lenPushers(resp Response) int {
	pushers, _ := resp.Body["pushers"].([]any)
	return len(pushers)
}

// mustMediaID extracts the media id of an mxc:// URI.
func mustMediaID(t *testing.T, uri string) string {
	t.Helper()
	const prefix = "mxc://test.server/"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("content_uri %q is not a %s URI", uri, prefix)
	}
	return strings.TrimPrefix(uri, prefix)
}

// testPNG renders a solid colour PNG of the requested size.
func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.NRGBA{R: byte(x * 255 / width), G: byte(y * 255 / height), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding the test image: %v", err)
	}
	return buf.Bytes()
}

// pngBounds reports the size of a PNG payload.
func pngBounds(t *testing.T, raw []byte) image.Rectangle {
	t.Helper()
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding the thumbnail: %v", err)
	}
	return decoded.Bounds()
}

// TestReceiptsDoNotMoveBackwards checks that an older receipt cannot undo a
// newer one, which clients rely on to keep read markers stable.
func TestReceiptsDoNotMoveBackwards(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")
	bob := server.Register("bob", "rubberduck")
	roomID := server.CreateRoom(alice, map[string]any{"preset": "public_chat"})
	server.MustDo(http.MethodPost, "/_matrix/client/v3/join/"+roomID, bob.Token, nil, http.StatusOK)
	start := server.MustDo(http.MethodGet, "/_matrix/client/v3/sync?timeout=0", bob.Token, nil, http.StatusOK).String("next_batch")

	first := server.Send(alice, roomID, "txn-old", "the older message")
	last := server.Send(alice, roomID, "txn-new", "the newer message")

	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read", last), bob.Token, nil, http.StatusOK)
	// An attempt to rewind the receipt to an earlier event is accepted but has
	// no effect on the stored position.
	server.MustDo(http.MethodPost, receiptPath(roomID, "m.read", first), bob.Token, nil, http.StatusOK)

	synced, resp := server.Sync(bob, "?since="+start+"&timeout=0")
	room, ok := synced.Rooms.Join[roomID]
	if !ok || room.Ephemeral == nil {
		t.Fatalf("no receipt ephemeral events: %s", resp.Raw)
	}
	content := objectContent(t, *eventOfType(t, room.Ephemeral.Events, matrix.TypeReceipt))
	if _, ok := content[first].(map[string]any); ok {
		t.Errorf("the receipt moved backwards to the older event: %v", content)
	}
	read, _ := content[last].(map[string]any)
	if read == nil {
		t.Fatalf("the receipt no longer points at the newest event: %v", content)
	}
	if _, ok := read["m.read"].(map[string]any)[bob.UserID]; !ok {
		t.Errorf("the surviving receipt lost its user: %v", read)
	}
}

// TestAccount3PIDs covers the third-party identifier endpoints. Lattice keeps no
// email addresses or phone numbers, so the listing is empty and everything that
// would change it refuses with a reason. Routing them matters anyway: Element
// shows "Unable to load email addresses and phone numbers" for an endpoint it
// cannot reach, where an empty settings row belongs.
func TestAccount3PIDs(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	listed := server.MustDo(http.MethodGet, "/_matrix/client/v3/account/3pid", alice.Token, nil, http.StatusOK)
	raw, ok := listed.Body["threepids"]
	if !ok {
		t.Fatalf("the listing has no threepids key: %s", listed.Raw)
	}
	ids, ok := raw.([]any)
	if !ok || len(ids) != 0 {
		t.Errorf("the listing is not an empty list: %s", listed.Raw)
	}

	cases := []struct {
		path string
		want int
	}{
		{"/_matrix/client/v3/account/3pid", http.StatusForbidden},
		{"/_matrix/client/v3/account/3pid/add", http.StatusForbidden},
		{"/_matrix/client/v3/account/3pid/bind", http.StatusForbidden},
		{"/_matrix/client/v3/account/3pid/unbind", http.StatusForbidden},
		{"/_matrix/client/v3/account/3pid/delete", http.StatusNotFound},
		{"/_matrix/client/v3/account/3pid/email/requestToken", http.StatusForbidden},
		{"/_matrix/client/v3/account/3pid/msisdn/requestToken", http.StatusForbidden},
	}
	for _, tc := range cases {
		resp := server.Do(http.MethodPost, tc.path, alice.Token,
			map[string]any{"medium": "email", "address": "alice@example.org",
				"client_secret": "s3cr3t", "send_attempt": 1})
		if resp.Status != tc.want {
			t.Errorf("%s: got %d %s, want %d", tc.path, resp.Status, resp.Raw, tc.want)
		}
		if resp.ErrCode() == "" {
			t.Errorf("%s: the refusal carries no error code: %s", tc.path, resp.Raw)
		}
		if !strings.Contains(resp.String("error"), "third-party") &&
			!strings.Contains(resp.String("error"), "verification") &&
			!strings.Contains(resp.String("error"), "third-party identifiers") {
			t.Errorf("%s: the refusal does not say why: %s", tc.path, resp.Raw)
		}
	}

	// The endpoints belong to the account the token was issued for.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/account/3pid", "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("listing third-party identifiers without a token: got %d %s", resp.Status, resp.Raw)
	}
}
