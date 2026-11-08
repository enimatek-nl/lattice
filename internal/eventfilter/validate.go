package eventfilter

import (
	"encoding/json"
	"strconv"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// A filter is the client's own document, so a filter with a typo in it is not a filter that
// happens to match nothing: it is a client bug that this server would store, answer 200 to, and
// then apply as if nothing was wrong on every later /sync. The checks below are the ones the
// specification's filter object states — which fields are objects, which are lists of strings,
// which are lists of room IDs and user IDs — taken as the room's, not as a courtesy.
//
// What is deliberately not checked: unknown field names. The filter object is where MSCs put
// their switches (a threaded-sync filter carries a namespaced name today), and refusing a name
// this server has not read would make this server the reason a client cannot try a newer spec.
// A field this server knows must be the shape the specification gives it; a field it does not
// know is left alone.

// ValidateDocument checks a filter document of the shape `/sync` and
// POST /user/{user}/filter take: the event filters nested under `presence`, `room.timeline`,
// `room.state` and the rest.
func ValidateDocument(body []byte) error {
	top, ok := objectMembers(body)
	if !ok {
		return filterProblem("A filter must be a JSON object.")
	}
	if raw, found := top["event_format"]; found {
		var format string
		if err := json.Unmarshal(raw, &format); err != nil {
			return filterProblem(`"event_format" is a string: "client" or "federation".`)
		}
		if format != "client" && format != "federation" {
			return filterProblem(`"event_format" is "client" or "federation", not ` +
				strconv.Quote(format) + ".")
		}
	}
	if raw, found := top["presence"]; found {
		if err := ValidateEventFilter("presence", raw, false); err != nil {
			return err
		}
	}
	raw, found := top["room"]
	if !found {
		return nil
	}
	room, ok := objectMembers(raw)
	if !ok {
		return filterProblem(`"room" is an object holding "timeline", "state", "ephemeral" and "account_data".`)
	}
	// The order is fixed rather than the map's, so the same bad filter always says the same thing.
	for _, name := range []string{"timeline", "state", "ephemeral", "account_data"} {
		sub, found := room[name]
		if !found {
			continue
		}
		if err := ValidateEventFilter("room."+name, sub, name == "timeline"); err != nil {
			return err
		}
	}
	if sub, found := room["include_leave"]; found {
		var leave bool
		if err := json.Unmarshal(sub, &leave); err != nil {
			return filterProblem(`"room.include_leave" is true or false.`)
		}
	}
	return nil
}

// eventField describes one field of an event filter and what sort of value it holds.
type eventField struct {
	name string
	kind byte
}

const (
	kindLimit   = 'l' // a non-negative integer
	kindBool    = 'b' // true or false
	kindStrings = 's' // a list of strings
	kindRoomIDs = 'r' // a list of room IDs
	kindUsers   = 'u' // a list of user IDs
)

// eventFilterFields are the fields every event filter has; a timeline filter adds the room
// lists, which only make sense on the stream that is about rooms.
var (
	eventFilterFields = []eventField{
		{"limit", kindLimit},
		{"types", kindStrings},
		{"not_types", kindStrings},
		{"urls", kindStrings},
		{"not_urls", kindStrings},
		{"senders", kindUsers},
		{"not_senders", kindUsers},
		{"contains_url", kindBool},
		// MSC3874's relation lists. Known by their namespaced names only: the plain names belong
		// to a proposal the specification has not adopted, and answering both would be guessing
		// which question an older client meant.
		{"org.matrix.msc3874.rel_types", kindStrings},
		{"org.matrix.msc3874.not_rel_types", kindStrings},
	}
	timelineExtraFields = []eventField{
		{"rooms", kindRoomIDs},
		{"not_rooms", kindRoomIDs},
	}
)

func ValidateEventFilter(where string, raw json.RawMessage, timeline bool) error {
	fields, ok := objectMembers(raw)
	if !ok {
		return filterProblem(`"` + where + `" is an object.`)
	}
	fieldsToCheck := eventFilterFields
	if timeline {
		fieldsToCheck = append(fieldsToCheck, timelineExtraFields...)
	}
	for _, field := range fieldsToCheck {
		value, found := fields[field.name]
		if !found {
			continue
		}
		switch field.kind {
		case kindLimit:
			var limit int
			if err := json.Unmarshal(value, &limit); err != nil {
				return filterProblem(`"` + where + "." + field.name + `" is a number of events.`)
			}
			if limit < 0 {
				return filterProblem(`"` + where + "." + field.name + `" cannot be negative.`)
			}
		case kindStrings:
			var list []string
			if err := json.Unmarshal(value, &list); err != nil {
				return filterProblem(`"` + where + "." + field.name + `" is a list of strings.`)
			}
		case kindRoomIDs:
			var list []string
			if err := json.Unmarshal(value, &list); err != nil {
				return filterProblem(`"` + where + "." + field.name + `" is a list of room IDs.`)
			}
			for _, roomID := range list {
				if err := matrixid.RoomIDValid(roomID); err != nil {
					return filterProblem(`"` + where + "." + field.name + `" holds ` +
						strconv.Quote(roomID) + ", which is not a room ID: " + err.Error())
				}
			}
		case kindBool:
			var yes bool
			if err := json.Unmarshal(value, &yes); err != nil {
				return filterProblem(`"` + where + "." + field.name + `" is true or false.`)
			}
		case kindUsers:
			var list []string
			if err := json.Unmarshal(value, &list); err != nil {
				return filterProblem(`"` + where + "." + field.name + `" is a list of user IDs.`)
			}
			for _, userID := range list {
				if err := matrixid.UserValid(userID); err != nil {
					return filterProblem(`"` + where + "." + field.name + `" holds ` +
						strconv.Quote(userID) + ", which is not a user ID: " + err.Error())
				}
			}
		}
	}
	// The two lazy-loading switches are read as this shape everywhere it is read, so a filter
	// that spells them another way would be quietly ignored rather than refused.
	if value, found := fields["lazy_load_members"]; found {
		var lazy bool
		if err := json.Unmarshal(value, &lazy); err != nil {
			return filterProblem(`"` + where + `.lazy_load_members" is true or false.`)
		}
	}
	if value, found := fields["lazy_load_options"]; found {
		if _, ok := objectMembers(value); !ok {
			return filterProblem(`"` + where + `.lazy_load_options" is an object.`)
		}
	}
	return nil
}

// objectMembers parses a value as a JSON object, reporting whether it is one.
func objectMembers(raw []byte) (map[string]json.RawMessage, bool) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil || members == nil {
		return nil, false
	}
	return members, true
}

func filterProblem(what string) error {
	return matrixerr.New(matrixerr.CodeInvalidParam, what)
}

// ParseRoomEventFilter reads a filter given where a room event filter is expected on its own —
// the whole `filter` parameter of `/rooms/{room}/messages` — checking its shape first. A filter
// this server cannot make sense of is refused at the door rather than kept as one that quietly
// matches nothing.
func ParseRoomEventFilter(raw string) (RoomEventFilter, error) {
	if raw == "" {
		return RoomEventFilter{}, nil
	}
	if err := ValidateEventFilter("filter", []byte(raw), true); err != nil {
		return RoomEventFilter{}, err
	}
	var filter RoomEventFilter
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return RoomEventFilter{}, filterProblem("The filter is not a JSON object.")
	}
	return filter, nil
}
