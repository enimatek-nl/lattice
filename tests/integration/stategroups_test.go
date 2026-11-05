package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/enimatek/lattice/internal/persist"
)

// A room's current state is a convenience; what a fork is resolved from is the state each
// branch was carrying. These tests check that the snapshots are recorded as events are
// persisted, that they are shared rather than copied, and that they agree with the current
// state the room serves.

func TestEachEventRecordsTheStateItWasSentAgainst(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()

	_, alice, _ := h.register(t, "snap1", "pw-snap1", "")
	roomID := createRoom(t, h, alice, map[string]any{"preset": "public_chat"})

	var (
		createID string
		topicID  string
		messages []string
	)
	rows, err := h.app.DB.State.All(ctx, roomID)
	if err != nil {
		t.Fatalf("reading the room's state: %v", err)
	}
	for _, row := range rows {
		if row.Type == "m.room.create" {
			createID = row.EventID
		}
	}
	if createID == "" {
		t.Fatalf("the room has no create event in its state: %v", rows)
	}

	for _, body := range []string{"one", "two", "three"} {
		sent := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-"+body,
			`{"msgtype":"m.text","body":"`+body+`"}`, alice), http.StatusOK).decoded(t)
		id, _ := sent["event_id"].(string)
		if id == "" {
			t.Fatalf("no event ID for %s", body)
		}
		messages = append(messages, id)
	}
	topic := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/state/m.room.topic/",
		map[string]any{"topic": "a topic"}, alice), http.StatusOK).decoded(t)
	topicID, _ = topic["event_id"].(string)
	if topicID == "" {
		t.Fatalf("setting the topic returned no event ID: %v", topic)
	}
	last := h.mustStatus(t, send(t, h, roomID, "m.room.message", "txn-after",
		`{"msgtype":"m.text","body":"after"}`, alice), http.StatusOK).decoded(t)
	lastID, _ := last["event_id"].(string)

	groupOf := func(eventID string) int64 {
		t.Helper()
		id, err := h.app.DB.StateGroups.GroupOf(ctx, eventID)
		if err != nil {
			t.Fatalf("which state was %s sent against: %v", eventID, err)
		}
		return id
	}
	entries := func(groupID int64) []persist.StateGroupEntry {
		t.Helper()
		list, err := h.app.DB.StateGroups.Entries(ctx, groupID)
		if err != nil {
			t.Fatalf("reading snapshot %d: %v", groupID, err)
		}
		return list
	}
	holds := func(groupID int64, eventType, stateKey, eventID string) bool {
		t.Helper()
		for _, entry := range entries(groupID) {
			if entry.Type == eventType && entry.StateKey == stateKey {
				return entry.EventID == eventID
			}
		}
		return false
	}

	// The create event's snapshot is the room as it was at the moment of creation: one
	// entry, itself. Everything the preset adds arrives afterwards and extends it.
	if got := entries(groupOf(createID)); len(got) != 1 || got[0].EventID != createID {
		t.Errorf("the create event's snapshot is %v, want only the create event", got)
	}

	// Messages do not change the state, so they share one snapshot rather than each
	// carrying a copy of it.
	first, second, third := groupOf(messages[0]), groupOf(messages[1]), groupOf(messages[2])
	if first != second || second != third {
		t.Errorf("three messages in a row took three snapshots (%d, %d, %d); a room would store its state once per message",
			first, second, third)
	}

	// The topic event is where the state changed: its snapshot holds the topic and its
	// predecessor's entries do not.
	topicGroup := groupOf(topicID)
	if topicGroup == third {
		t.Errorf("the topic event reused the previous snapshot; the topic would be invisible to a reader of that branch")
	}
	if !holds(topicGroup, "m.room.topic", "", topicID) {
		t.Errorf("the topic event's snapshot does not hold the topic: %v", entries(topicGroup))
	}
	for _, entry := range entries(third) {
		if entry.Type == "m.room.topic" {
			t.Errorf("the snapshot before the topic already holds it: %v", entries(third))
		}
	}
	if len(entries(topicGroup)) != len(entries(third))+1 {
		t.Errorf("the topic added %d entries, want exactly one",
			len(entries(topicGroup))-len(entries(third)))
	}

	// And the change carries forward.
	if groupOf(lastID) != topicGroup {
		t.Errorf("a message after the topic was recorded against a different snapshot")
	}

	// The snapshots and the current state agree. They answer different questions — where
	// the room is, and what each event was sent against — and if they drift, the room has
	// two histories.
	current, err := h.app.DB.State.All(ctx, roomID)
	if err != nil {
		t.Fatalf("reading the current state: %v", err)
	}
	for _, row := range current {
		if !holds(topicGroup, row.Type, row.StateKey, row.EventID) {
			t.Errorf("current state holds %s/%s = %s, which the newest snapshot does not",
				row.Type, row.StateKey, row.EventID)
		}
	}

	// Sharing is visible in the count: six state events plus four messages, and far fewer
	// snapshots than events.
	var groups int64
	if err := h.app.DB.DB().Model(&persist.StateGroup{}).
		Where("room_id = ?", roomID).Count(&groups).Error; err != nil {
		t.Fatalf("counting snapshots: %v", err)
	}
	var events int64
	if err := h.app.DB.DB().Model(&persist.Event{}).
		Where("room_id = ?", roomID).Count(&events).Error; err != nil {
		t.Fatalf("counting events: %v", err)
	}
	if groups >= events {
		t.Errorf("%d snapshots for %d events: the snapshots are being copied, not shared", groups, events)
	}
}
