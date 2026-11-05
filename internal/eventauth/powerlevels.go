// Package eventauth implements the room-version-specific authorisation rules of
// the Matrix specification.
//
// Authorisation is a subsystem of its own (AGENTS.md §7): it is pure logic over an
// event plus the room state it is being authorised against, so it can be tested
// without a database or a network, and reused unchanged by the Client-Server API,
// the federation API and state resolution's iterative auth checks.
//
// The rules implemented here are those of room versions 3 through 11, taken from
// the per-version pages of the pinned specification (v1.19). Room version 12 is
// treated as version 11 plus the privileged-creator adjustment to the create rule.
// Room version 1 keeps a rule table of its own and is reported as unsupported
// rather than approximated.
package eventauth

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/enimatek/lattice/internal/roomversion"
)

// Default power levels, as defined by the m.room.power_levels section of the
// Client-Server specification. They are repeated here because the authorisation
// rules depend on them, and because a missing value in the event content means
// "use the default", not "zero".
const (
	DefaultUsersDefault     = 0
	DefaultEventsDefault    = 0
	DefaultStateDefault     = 50
	DefaultBan              = 50
	DefaultKick             = 50
	DefaultRedact           = 50
	DefaultInvite           = 0
	DefaultNotifications    = 0
	notificationRoomMention = 3
	notificationReply       = 0
)

// PowerLevels is the parsed content of the current m.room.power_levels event, with
// defaults applied.
type PowerLevels struct {
	Users         map[string]int
	UsersDefault  int
	Events        map[string]int
	EventsDefault int
	StateDefault  int
	Ban           int
	Kick          int
	Redact        int
	Invite        int
	Notifications map[string]int
}

// DefaultPowerLevels returns the levels that apply when a room has no
// m.room.power_levels event.
func DefaultPowerLevels() *PowerLevels {
	return &PowerLevels{
		Users:         map[string]int{},
		UsersDefault:  DefaultUsersDefault,
		Events:        map[string]int{},
		EventsDefault: DefaultEventsDefault,
		StateDefault:  DefaultStateDefault,
		Ban:           DefaultBan,
		Kick:          DefaultKick,
		Redact:        DefaultRedact,
		Invite:        DefaultInvite,
		Notifications: defaultNotifications(),
	}
}

func defaultNotifications() map[string]int {
	return map[string]int{
		"m.room.mention":  DefaultNotifications,
		"m.room.reply":    notificationReply,
		"m.room.message":  DefaultNotifications,
		"m.room.feedback": DefaultNotifications,
	}
}

// LoadPowerLevels parses the content of an m.room.power_levels event.
//
// strict selects the room version 10 behaviour: a non-integer power level makes the
// event invalid. In earlier room versions the authorisation rules do not reject it,
// so the value is read as it can be and the caller is told what happened through
// the returned error being nil while the map holds the coerced value. Keeping that
// difference visible here is the point: silently coercing everywhere would make the
// server accept events a version 10 room must reject, and rejecting everywhere would
// make it discard events in older rooms.
func LoadPowerLevels(rules roomversion.Rules, content map[string]any) (*PowerLevels, error) {
	pl := DefaultPowerLevels()
	if content == nil {
		return pl, nil
	}

	var errs []error
	optInt := func(key string, dst *int) {
		raw, ok := content[key]
		if !ok {
			return
		}
		v, ok := asInt(raw)
		if !ok {
			if rules.EnforceIntegerPowerLevels {
				errs = append(errs, fmt.Errorf("power level %q is not an integer", key))
			}
			return
		}
		*dst = v
	}
	optInt("users_default", &pl.UsersDefault)
	optInt("events_default", &pl.EventsDefault)
	optInt("state_default", &pl.StateDefault)
	optInt("ban", &pl.Ban)
	optInt("kick", &pl.Kick)
	optInt("redact", &pl.Redact)
	optInt("invite", &pl.Invite)

	if raw, ok := content["users"]; ok {
		// Keys must be valid user IDs and values integers; a malformed entry in a
		// version 10 room makes the event invalid.
		entries, ok := stringKeyedMap(raw)
		if !ok {
			if rules.EnforceIntegerPowerLevels {
				errs = append(errs, fmt.Errorf("power level users is not an object"))
			}
		} else {
			for userID, v := range entries {
				n, ok := asInt(v)
				if !ok {
					if rules.EnforceIntegerPowerLevels {
						errs = append(errs, fmt.Errorf("power level for %s is not an integer", userID))
					}
					continue
				}
				pl.Users[userID] = n
			}
		}
	}

	for _, key := range []struct {
		name string
		dst  map[string]int
	}{{"events", pl.Events}, {"notifications", pl.Notifications}} {
		raw, ok := content[key.name]
		if !ok {
			continue
		}
		entries, ok := stringKeyedMap(raw)
		if !ok {
			if rules.EnforceIntegerPowerLevels {
				errs = append(errs, fmt.Errorf("power level %s is not an object", key.name))
			}
			continue
		}
		for eventType, v := range entries {
			n, ok := asInt(v)
			if !ok {
				if rules.EnforceIntegerPowerLevels {
					errs = append(errs, fmt.Errorf("power level for %s is not an integer", eventType))
				}
				continue
			}
			key.dst[eventType] = n
		}
	}

	if len(errs) > 0 {
		return pl, errs[0]
	}
	return pl, nil
}

// User returns the power level of a user in the room.
func (p *PowerLevels) User(userID string) int {
	if v, ok := p.Users[userID]; ok {
		return v
	}
	return p.UsersDefault
}

// ForEvent returns the power level required to send an event of the given type.
// State events use state_default unless the events map names the type; message
// events use events_default.
func (p *PowerLevels) ForEvent(eventType string, isState bool) int {
	if v, ok := p.Events[eventType]; ok {
		return v
	}
	if isState {
		return p.StateDefault
	}
	return p.EventsDefault
}

// asInt reads a JSON number that is an integer. Numbers arrive as json.Number from
// internal/events, and as int64/float64 from hand-built maps in tests.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		return int(i), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int(int64(n)), true
	default:
		return 0, false
	}
}

func stringKeyedMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// contentOf returns an event's content object, or nil when absent or not an object.
func contentOf(event map[string]any) map[string]any {
	if event == nil {
		return nil
	}
	c, _ := event["content"].(map[string]any)
	return c
}

// stringOf reads a top-level string property.
func stringOf(event map[string]any, key string) (string, bool) {
	if event == nil {
		return "", false
	}
	s, ok := event[key].(string)
	return s, ok
}
