// Package eventfilter holds the filter object a client writes, and the decision about whether
// an event survives it.
//
// Two doors take a filter: `/sync`, where it sits under `room.timeline` and `room.state`, and
// `/rooms/{room}/messages`, where the whole parameter is a room event filter. The shape is the
// same document either way, so the shape and the decision live here rather than in whichever
// package noticed them first — a filter that means one thing on one door and another thing on
// the next is the kind of difference a client cannot debug from the outside.
package eventfilter

// EventFilter is a filter over one stream of events.
//
// A list field that is absent means "no opinion"; a list field present and empty means the
// client asked for nothing of that kind. That distinction is the whole of how a client turns a
// stream off, so the fields the distinction applies to are pointers or nil-checked.
type EventFilter struct {
	Limit      *int      `json:"limit"`
	Types      []string  `json:"types"`
	NotTypes   []string  `json:"not_types"`
	Senders    *[]string `json:"senders"`
	NotSenders *[]string `json:"not_senders"`
	// URLs and NotURLs select on the URL in an event's content, which is what media events
	// carry. They are held so that a filter is stored and read back as the client wrote it.
	URLs    *[]string `json:"urls"`
	NotURLs *[]string `json:"not_urls"`
	// ContainsURL is the coarse version of the two above: keep the events that have a URL in
	// their content, or the ones that do not.
	ContainsURL *bool `json:"contains_url"`
	// LazyLoadMembers is read on a state filter: send the member events of the senders the
	// batch speaks about rather than the room's whole membership.
	LazyLoadMembers bool `json:"lazy_load_members"`
	// LazyLoadOptions is the structured form of the flag, held so it is not mistaken for a typo.
	LazyLoadOptions any `json:"lazy_load_options"`
}

// RoomEventFilter is an event filter for a stream that belongs to one room, which adds the room
// lists. They are the same filter document as everywhere else, so a client that sets them on a
// state filter is obeyed the same way.
type RoomEventFilter struct {
	EventFilter
	Rooms    []string `json:"rooms"`
	NotRooms []string `json:"not_rooms"`
}

// AllowsType reports whether an event type survives this filter. An absent `types` list means
// everything; a present one means exactly those types, even when it is empty.
func (f EventFilter) AllowsType(eventType string) bool {
	if f.Types != nil {
		for _, wanted := range f.Types {
			if wanted == eventType {
				return true
			}
		}
		return false
	}
	for _, unwanted := range f.NotTypes {
		if unwanted == eventType {
			return false
		}
	}
	return true
}

// AllowsSender reports whether an event from this user survives the filter's sender lists.
func (f EventFilter) AllowsSender(sender string) bool {
	if f.Senders != nil {
		for _, wanted := range *f.Senders {
			if wanted == sender {
				return true
			}
		}
		return false
	}
	for _, unwanted := range deref(f.NotSenders) {
		if unwanted == sender {
			return false
		}
	}
	return true
}

// AllowsContent applies the parts of the filter that need the event rather than only its type:
// the sender lists and the URL question.
func (f EventFilter) AllowsContent(event map[string]any) bool {
	if sender, _ := event["sender"].(string); !f.AllowsSender(sender) {
		return false
	}
	if f.ContainsURL == nil {
		return true
	}
	return hasURL(event) == *f.ContainsURL
}

// AllowsIn reports whether an event of this type, from this room, belongs in the stream. It is
// the whole decision for a room event filter, and it is the one `/messages` runs.
func (f RoomEventFilter) AllowsIn(roomID string, event map[string]any) bool {
	if !f.AllowsType(stringField(event, "type")) {
		return false
	}
	if !f.AllowsContent(event) {
		return false
	}
	if !roomAllowed(f.Rooms, f.NotRooms, roomID) {
		return false
	}
	return true
}

// roomAllowed applies the room lists. As with the type lists, an absent list is no opinion and
// a present one is the whole answer.
func roomAllowed(wanted, unwanted []string, roomID string) bool {
	if wanted != nil {
		for _, room := range wanted {
			if room == roomID {
				return true
			}
		}
		return false
	}
	for _, room := range unwanted {
		if room == roomID {
			return false
		}
	}
	return true
}

// hasURL is what `contains_url` asks: does this event carry a URL in its content? The reference
// servers read the `url` key, which is what a media event's content holds; a content whose `url`
// is an empty string or null is not a URL.
func hasURL(event map[string]any) bool {
	content, _ := event["content"].(map[string]any)
	if content == nil {
		return false
	}
	switch value := content["url"].(type) {
	case string:
		return value != ""
	case nil:
		return false
	default:
		return true
	}
}

func deref(list *[]string) []string {
	if list == nil {
		return nil
	}
	return *list
}

func stringField(event map[string]any, name string) string {
	value, _ := event[name].(string)
	return value
}
