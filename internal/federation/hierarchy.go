package federation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"

	"github.com/enimatek/lattice/internal/rooms"
)

// HierarchyQuerier asks another homeserver what it says about a room this server does not hold: the
// room's own summary, the children of that room the asking server could see, and the children it could
// not. It is the outgoing half of the Server-Server API's hierarchy query, and it exists because a
// space whose child lives elsewhere is not otherwise walkable.
//
// What comes back is kept in the form the peer sent it. A summary of a remote room is that peer's word
// about its own room — its name, its topic, its join rule — and re-describing it through this server's
// own structs would quietly decide what a peer is allowed to have said. The caller in the room service
// is the one that weighs it: it checks that the answer names the room that was asked about, and it
// decides about the user in front of it from this server's own state.
type HierarchyQuerier struct {
	Client *Client
	// Name is this server's name, carried for the log lines; the request's authorization already
	// names the server that is asking.
	Log *slog.Logger
}

// Hierarchy asks `destination` for the hierarchy of a room it holds.
//
// The query is a read, so it is authenticated like every other Server-Server request and answers
// nothing about the user this server happens be acting for: the peer is being asked whether *this
// server* could see the rooms, and this server does the user's filtering afterwards.
func (q *HierarchyQuerier) Hierarchy(ctx context.Context, destination, roomID string, suggestedOnly bool) (rooms.HierarchyAnswer, error) {
	if q == nil || q.Client == nil {
		return rooms.HierarchyAnswer{}, errors.New("federation: the hierarchy query is not wired up")
	}
	target := "/_matrix/federation/v1/hierarchy/" + url.PathEscape(roomID) +
		"?suggested_only=" + strconv.FormatBool(suggestedOnly)
	payload, err := q.Client.GetJSON(ctx, destination, target)
	if err != nil {
		return rooms.HierarchyAnswer{}, err
	}
	return hierarchyAnswerOf(payload, roomID)
}

// hierarchyAnswerOf reads a peer's hierarchy answer.
//
// The pinned specification's shape is an object: the room asked about, the children the asking server
// may see, and the IDs of the children it may not. Homeservers that answered this query before it was
// stabilised answered with a flat `rooms` list instead, and that shape is still in the wild; it is read
// here only when it says what the specified shape says — the entry naming the room that was asked about
// is the room, the entries after it are children. Anything that does not name the requested room is
// refused, because an answer about a different room is not an answer about this one.
func hierarchyAnswerOf(payload map[string]any, roomID string) (rooms.HierarchyAnswer, error) {
	answer := rooms.HierarchyAnswer{}
	if room, ok := payload["room"].(map[string]any); ok {
		answer.Room = room
	}
	for _, child := range anyList(payload["children"]) {
		answer.Children = append(answer.Children, child)
	}
	for _, id := range payloadStrings(payload["inaccessible_children"]) {
		answer.InaccessibleChildren = append(answer.InaccessibleChildren, id)
	}
	if answer.Room == nil {
		entries := anyList(payload["rooms"])
		if len(entries) == 0 {
			return rooms.HierarchyAnswer{}, fmt.Errorf("federation: a hierarchy answer carries no room %q", roomID)
		}
		first := entries[0]
		if name, _ := first["room_id"].(string); name != roomID {
			return rooms.HierarchyAnswer{}, fmt.Errorf(
				"federation: a hierarchy answer for %q starts with %q", roomID, name)
		}
		answer.Room = first
		for _, child := range entries[1:] {
			answer.Children = append(answer.Children, child)
		}
	}
	return answer, nil
}

// anyList reads a JSON array of objects, ignoring anything that is not one.
func anyList(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}

// payloadStrings reads a JSON array of strings.
func payloadStrings(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}
