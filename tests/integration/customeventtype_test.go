package integration

import (
	"net/http"
	"net/url"
	"testing"
)

// An event type without a dot is unqualified, not malformed. The dotted namespace is a
// convention whose point is keeping two servers' names apart, which is a problem for whoever
// invents a name rather than for the server that carries it. This server used to refuse
// undotted types on the way in, and the answer it gave was a 400 before the room saw
// anything — so a client could not store, filter or react to an event the specification
// describes as ordinary, and the endpoints for it were unreachable rather than missing.

func TestCustomEventTypeWithoutADotIsAccepted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "wren", "pw-wren", "")
	roomID := createRoom(t, h, token, map[string]any{"name": "filters"})

	sent := h.mustStatus(t, h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/send/please_filter_me/undotted-1",
		map[string]any{"body": "an unqualified type"}, token), http.StatusOK)
	eventID, _ := sent.decoded(t)["event_id"].(string)
	if eventID == "" {
		t.Fatalf("sending an undotted event type returned no event_id")
	}

	// It arrives in a sync like any other event.
	room := roomSection(t, syncBody(t, h, token, ""), "join", roomID)
	if !containsString(eventTypes(t, room, "timeline"), "please_filter_me") {
		t.Errorf("the undotted event type is missing from the timeline: %v", eventTypes(t, room, "timeline"))
	}

	// And it can be filtered out — which is the point of the Complement case this came
	// from: a filter names the type, so the type has to be storable in the first place.
	filter := `{"room":{"timeline":{"not_types":["please_filter_me"]}}}`
	filtered := roomSection(t, syncBody(t, h, token, "&filter="+url.QueryEscape(filter)), "join", roomID)
	if containsString(eventTypes(t, filtered, "timeline"), "please_filter_me") {
		t.Errorf("the timeline filter did not exclude the event type it named: %v", eventTypes(t, filtered, "timeline"))
	}
	// Excluded from a sync is not deleted. The event is still the room's, and a client that
	// filtered it out of a batch can still go and read it.
	found := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/event/"+url.PathEscape(eventID), token),
		http.StatusOK).decoded(t)
	if found["type"] != "please_filter_me" {
		t.Errorf("the stored event is %v, want the undotted type", found["type"])
	}

	// The reserved namespace is still enforced: `m.` means the specification's own shape,
	// and a state entry created under a mistyped reserved name is one nothing matches.
	for index, bad := range []string{"m.Room.Message", "m.", "m..room", "bad type"} {
		res := h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/send/"+url.PathEscape(bad)+
			"/undotted-bad-"+itoa(index), map[string]any{"body": "reserved shape"}, token)
		if res.status != http.StatusBadRequest {
			t.Errorf("the reserved-shape type %q got %d, want 400: %s", bad, res.status, res.body)
		}
	}
}
