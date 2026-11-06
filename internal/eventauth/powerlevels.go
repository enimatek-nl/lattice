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
	"math"
	"strconv"
	"strings"

	"github.com/enimatek/lattice/internal/matrixid"
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
		v, ok := asInt(rules, raw)
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
				n, ok := asInt(rules, v)
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
			n, ok := asInt(rules, v)
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
//
// A membership event is the exception, and it is an exception that decides whether a room can be
// used at all. `m.room.member` is a state event, so state_default would put it at 50 — and then an
// ordinary user could not write the event that says they left. The specification's default
// requirement for `m.room.member` is zero, because *who* may change a membership is decided by the
// membership rules themselves, not by a level on the type: a join must come from the user joining,
// an invite needs the invite level, a kick needs the kick level and a lower power than the target, a
// ban needs the ban level. Those rules are checked on their own; holding the type at 50 on top of
// them would not stop anything, it would only lock ordinary users out of their own membership.
func (p *PowerLevels) ForEvent(eventType string, isState bool) int {
	if v, ok := p.Events[eventType]; ok {
		return v
	}
	if eventType == TypeMember {
		return 0
	}
	if isState {
		return p.StateDefault
	}
	return p.EventsDefault
}

// asInt reads a JSON number that is an integer. Numbers arrive as json.Number from
// internal/events, and as int64/float64 from hand-built maps in tests.
// asInt reads one power-level value. The strict reading is the one room version 6 and later require,
// where the event had to be canonical JSON and a power level is an integer.
//
// The permissive reading is what room versions 1 through 5 allow, and it is not a curiosity: those
// rooms are old and numerous, and the pages for them document two relaxations that servers had to
// honour to use them at all. An integer-valued property may be written as a string — a bare base-10
// integer, optionally signed, optionally zero-padded, optionally surrounded by whitespace — and a
// number may be written as a float, in which case it is truncated at the decimal point. A room whose
// power levels were written that way is not a room with no power levels: reading the values as absent
// would silently demote every moderator in it, which is exactly the sort of quiet wrong answer this
// server tries not to give.
func asInt(rules roomversion.Rules, v any) (int, bool) {
	permissive := !rules.StrictCanonicalJSON
	switch n := v.(type) {
	case json.Number:
		if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
			return int(i), true
		}
		if !permissive {
			return 0, false
		}
		// A number written with a fractional part is valid JSON, and in those versions it is a valid
		// power level once it is truncated.
		return truncateFloat(n.String())
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != float64(int64(n)) {
			if !permissive || isNaNOrInfinite(n) {
				return 0, false
			}
			return int(int64(n)), true
		}
		return int(int64(n)), true
	case string:
		if !permissive {
			return 0, false
		}
		trimmed := strings.TrimSpace(n)
		i, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// truncateFloat handles a JSON number that decoded as text but carries a fractional part: the value
// is cut at the decimal point, as the room version pages describe. Anything that is not a plain
// decimal number — an exponent is plain enough to read, an infinity or a NaN is not a power level —
// has no answer here.
func truncateFloat(text string) (int, bool) {
	whole := text
	if i := strings.IndexAny(whole, ".eE"); i >= 0 {
		whole = whole[:i]
	}
	if whole == "" || whole == "-" || whole == "+" {
		return 0, false
	}
	i, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	return int(i), true
}

// isNaNOrInfinite names the values the room version pages exclude by name. A float that cannot be
// held in an integer has no power level to report.
func isNaNOrInfinite(n float64) bool {
	return math.IsNaN(n) || math.IsInf(n, 0) || n > float64(math.MaxInt64) || n < float64(math.MinInt64)
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

// MayRedact reports whether a redaction sent by redactionSender may be applied to an
// event sent by targetSender, in the way the room's own version states the rule.
//
// The first condition is the same everywhere: the sender's power level is at least the
// room's redact level. The second differs by version, and the difference is not cosmetic.
// From room version 3 on it compares *people*: a homeserver may redact an event whose
// original sender is one of its own users, which is how a server cleans up after its own
// users without granting anybody power over another server's events. Room versions 1 and 2
// compare *names*: the domain of the event_id of the event being redacted against the
// domain of the event_id of the redaction. Those come out differently for the same pair of
// events — an event placed in a room by alice:hs1 but named by hs1's own identifier, for
// instance — so the version decides which pair of names is compared, rather than one rule
// quietly standing in for the other.
//
// redactionDomain is the domain the redaction's own event ID carries; for a redaction this
// server is about to create, that is this server's name, which the caller knows and the
// event does not yet.
//
// Note what this is not: the ability to *send* a redaction is decided by the authorisation
// rules, which do not consider the redact level at all. This decides whether the redaction
// is applied once it has arrived, which is why an event can be accepted by the rules and
// still never destroy anything.
func (p *PowerLevels) MayRedact(rules roomversion.Rules, redactionSender, targetSender, redactionDomain, targetEventID string) bool {
	if p.User(redactionSender) >= p.Redact {
		return true
	}
	if rules.AuthRuleset == "v1" {
		return eventDomain(targetEventID) == redactionDomain
	}
	return sameDomain(redactionSender, targetSender)
}

// eventDomain returns the namespacing domain of an event ID: the part after the colon in
// $opaque_id:domain, which is the shape room versions 1 and 2 use. A derived identifier has
// no such part, and an unparseable one has no domain to match.
func eventDomain(eventID string) string {
	if len(eventID) < 2 || eventID[0] != '$' {
		return ""
	}
	if _, domain, ok := strings.Cut(eventID[1:], ":"); !ok {
		return ""
	} else {
		return domain
	}
}

// sameDomain compares the server part of two user IDs. An unparseable ID has no domain to
// match, so it does not match anything.
func sameDomain(left, right string) bool {
	leftDomain, err := matrixid.UserDomain(left)
	if err != nil {
		return false
	}
	rightDomain, err := matrixid.UserDomain(right)
	if err != nil {
		return false
	}
	return leftDomain == rightDomain
}
