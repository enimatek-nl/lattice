package client

import "github.com/enimatek/lattice/internal/eventauth"

// presentEvents completes the one event in a room whose answer to "which room is this in?" is silence.
//
// A room version 12 create event carries no `room_id`. That is not an omission this server makes on the
// way out; rule 1.2 of the version refuses to authorise a create event that carries one, because the
// event's bytes are hashed into the room identifier and an event cannot contain the hash of itself. The
// Client-Server API nonetheless answers with events as objects that have identifiers, and MSC4291 is
// explicit that the endpoints returning events include the room identifier on the create event — a client
// that has just walked `/messages` or `/context` has one object in the answer whose room it would have to
// infer from the request it made to get the answer.
//
// So the identifier is added on the way out and never on the way in. Adding it to stored content would
// break the room: those bytes are what its name is a hash of, and a stored create event with a
// `room_id` is a create event that no longer hashes to the room it names. Adding it to the answer is a
// server completing a sentence about its own room, which is a thing it is allowed to know.
//
// `/sync` is the documented exception — it strips identifiers from the state it hands over — and it is a
// separate path, so it is not touched here.
//
// The events are completed in place. Every read path this is called from parses the stored bytes afresh
// for the request, so a completed map cannot leak back into storage or into another request; if a cache
// is ever put in front of those reads, this is the function that has to copy instead.
func presentEvents(roomID string, events []map[string]any) {
	for _, event := range events {
		presentEvent(roomID, event)
	}
}

// presentEvent is presentEvents for a single event, and shares everything worth knowing with it.
func presentEvent(roomID string, event map[string]any) {
	if event == nil || roomID == "" {
		return
	}
	if type_, _ := event["type"].(string); type_ != eventauth.TypeCreate {
		return
	}
	// Only where it is missing. A create event that arrives from a peer already carrying an identifier
	// is a room-version inconsistency worth leaving exactly as it is rather than quietly rewriting, and
	// rewriting one would mean this server's answer no longer matches the bytes its signature covers.
	if existing, ok := event["room_id"].(string); ok && existing != "" {
		return
	}
	event["room_id"] = roomID
}
