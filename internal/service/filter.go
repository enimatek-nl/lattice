package service

import (
	"encoding/json"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A sync filter is a small grammar the specification spells out: sections that
// are objects, lists of event types and Matrix identifiers, and integer limits.
// Clients build them from form fields, so a filter with the wrong shape is a
// client bug, and the two ways a server can answer it are very different.
//
// Storing the filter and staying silent would answer 200 and then read the
// definition as "no filter", because a JSON value that does not fit a field
// simply is not decoded into it: the client believes its limit applied while the
// server returned everything. Checking the shape on the way in is what makes the
// difference visible.

// validateFilter checks a stored filter definition against the filter grammar.
// It reports the first key that is not what it claims to be; keys it does not
// know are left alone, because a filter is a forward compatible document and a
// namespaced key belongs to a proposal this server has not adopted, not to the
// client that wrote it.
func validateFilter(body string) error {
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return matrix.ErrNotJSON("filter body must be valid JSON: %v", err)
	}
	for key, value := range root {
		switch key {
		case "presence", "account_data":
			filter, ok := jsonObject(value)
			if !ok {
				return matrix.ErrInvalidParam("filter key %q must be an object", key)
			}
			if err := validateEventFilter(key, filter); err != nil {
				return err
			}
		case "room":
			room, ok := jsonObject(value)
			if !ok {
				return matrix.ErrInvalidParam("filter key %q must be an object", key)
			}
			for _, section := range []string{"timeline", "state", "ephemeral", "account_data"} {
				raw, present := room[section]
				if !present || raw == nil {
					continue
				}
				filter, ok := jsonObject(raw)
				if !ok {
					return matrix.ErrInvalidParam("filter key room.%s must be an object", section)
				}
				if err := validateEventFilter("room."+section, filter); err != nil {
					return err
				}
			}
		case "event_fields":
			if _, err := filterStringList("event_fields", value, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateEventFilter checks one section of a filter: the presence, account data,
// timeline, state or ephemeral block, which all share the same fields.
func validateEventFilter(section string, filter map[string]any) error {
	for key, value := range filter {
		switch key {
		case "limit":
			if _, ok := integerOf(value); !ok {
				return matrix.ErrInvalidParam("filter key %s.limit must be an integer", section)
			}
		case "limit_type":
			text, ok := value.(string)
			if !ok || (text != "recent" && text != "fuzzy") {
				return matrix.ErrInvalidParam("filter key %s.limit_type must be recent or fuzzy", section)
			}
		case "types", "not_types":
			if _, err := filterStringList(section+"."+key, value, nil); err != nil {
				return err
			}
		case "senders", "not_senders":
			if _, err := filterStringList(section+"."+key, value, func(item string) error {
				if !matrix.IsUserID(item) {
					return matrix.ErrInvalidParam("filter key %s.%s holds %q, which is not a user ID",
						section, key, item)
				}
				return nil
			}); err != nil {
				return err
			}
		case "rooms", "not_rooms":
			if _, err := filterStringList(section+"."+key, value, func(item string) error {
				if !matrix.IsRoomID(item) {
					return matrix.ErrInvalidParam("filter key %s.%s holds %q, which is not a room ID",
						section, key, item)
				}
				return nil
			}); err != nil {
				return err
			}
		case "lazy_load_options":
			if _, ok := jsonObject(value); !ok {
				return matrix.ErrInvalidParam("filter key %s.lazy_load_options must be an object", section)
			}
		}
	}
	return nil
}

// filterStringList reads a JSON array of strings, running an optional check over every
// entry. label names the key in the error so a client can tell which field of its
// own filter it has to fix.
func filterStringList(label string, value any, check func(string) error) ([]string, error) {
	raw, ok := value.([]any)
	if !ok {
		return nil, matrix.ErrInvalidParam("filter key %s must be a list of strings", label)
	}
	items := make([]string, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(string)
		if !ok {
			return nil, matrix.ErrInvalidParam("filter key %s holds a value that is not a string", label)
		}
		if check != nil {
			if err := check(item); err != nil {
				return nil, err
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// jsonObject reads a JSON object.
func jsonObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

// integerOf reads a JSON number that has no fractional part.
func integerOf(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		if number != float64(int64(number)) {
			return 0, false
		}
		return int64(number), true
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}
