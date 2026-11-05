// Package roomversion is the registry of Matrix room versions and the behaviour
// that differs between them.
//
// Matrix rooms are not versioned by date or by ordering: each version is an
// independent, immutable set of rules, and a room keeps its version forever
// (it changes only by an explicit upgrade). That makes a version table the only
// safe way to implement room behaviour: any code that resolves state, authorises
// an event, redacts one or derives an event ID must ask the room's version
// first, and must never fall back to "the version we happen to prefer".
//
// The per-version differences below are transcribed from the pinned
// specification (Matrix v1.19, "Room Versions" and the per-version pages). The
// field names are ours; the semantics are the specification's.
package roomversion

import (
	"fmt"
	"regexp"
	"strings"
)

// StateRes names a state resolution algorithm. Choosing wrongly here is not a
// cosmetic bug: two servers that disagree about which algorithm a room uses will
// compute different room state and end up in a permanent split-brain.
type StateRes string

const (
	// StateResV1 is the original algorithm (room version 1 only).
	StateResV1 StateRes = "v1"
	// StateResV2 is State Resolution 2 (room versions 2 through 11).
	StateResV2 StateRes = "v2"
	// StateResV2_1 is the revised State Resolution 2 used by room version 12.
	StateResV2_1 StateRes = "v2.1"
)

// EventIDFormat says how event IDs are derived for a room version.
type EventIDFormat int

const (
	// EventIDServerAssigned is the "$localpart:server" form of room versions 1
	// and 2, where the origin server picks the identifier.
	EventIDServerAssigned EventIDFormat = iota
	// EventIDHashStdBase64 is the hash-based identifier of room version 3,
	// encoded with the standard base64 alphabet and no padding.
	EventIDHashStdBase64
	// EventIDHashURLSafeBase64 is the same hash, encoded with the URL-safe
	// alphabet and no padding (room versions 4 and later).
	EventIDHashURLSafeBase64
)

// Rules describes one room version.
type Rules struct {
	// ID is the version string as it appears in m.room.create content.
	ID string
	// Stable distinguishes versions the specification lists as stable from
	// experimental ones. Only stable versions are offered by default.
	Stable bool

	StateRes StateRes
	EventIDs EventIDFormat

	// AuthRuleset selects the authorisation rule table for this version. The
	// rule sets are grouped by the version that introduced a change: "v1" is the
	// rule table of room versions 1 and 2, which the specification gives as one
	// identical list on both pages (room version 2 changes only state resolution),
	// "v2" is the version 3 rewrite used through version 5, "v3" is the version 6
	// rewrite, "v4" adds integer power-level validation and knock_restricted
	// (version 10), and "v5" is the version 11 wording. Room version 12 uses "v6".
	// The identifier is a label, not a hierarchy.
	AuthRuleset string

	// RedactionProfile selects the strip list and content allow lists.
	RedactionProfile string
	// RedactionKeepsAuthorisedVia protects content.join_authorised_via_users_server
	// from redaction. Room version 8 introduced restricted joins but forgot to
	// protect this key, which could make a redacted join unverifiable; version 9
	// fixed it.
	RedactionKeepsAuthorisedVia bool

	// EnforceKeyValidity makes valid_until_ts on signing keys binding
	// (room versions 5 and later; it is ignored in 1-4).
	EnforceKeyValidity bool
	// StrictCanonicalJSON rejects floats, and integers outside the canonical
	// JSON range, instead of coercing them (room version 6 and later).
	StrictCanonicalJSON bool
	// LimitNotificationsPowerLevels applies the power-level ceiling to the
	// "notifications" map as well (room version 6 and later).
	LimitNotificationsPowerLevels bool
	// SpecialCaseAliases keeps the historic handling of m.room.aliases, which
	// was removed in room version 6.
	SpecialCaseAliases bool

	// SupportsKnock: the "knock" join rule exists (room version 7 and later).
	SupportsKnock bool
	// SupportsRestrictedJoin: "restricted" and join_authorised_via_users_server.
	SupportsRestrictedJoin bool
	// SupportsKnockRestricted: the combined "knock_restricted" join rule
	// (room version 10 and later).
	SupportsKnockRestricted bool
	// EnforceIntegerPowerLevels rejects non-integer power levels in
	// m.room.power_levels content (room version 10 and later).
	EnforceIntegerPowerLevels bool

	// CreatorInCreateContent: m.room.create content carries "creator"
	// (room versions 1-10). It was removed in version 11, where the sender is
	// the creator.
	CreatorInCreateContent bool
	// RedactsInContent: m.room.redaction names its target in content.redacts
	// rather than a top-level "redacts" property (room version 11 and later).
	RedactsInContent bool
	// DomainlessRoomIDs: room IDs have no server-name component
	// (room version 12).
	DomainlessRoomIDs bool
	// PrivilegedCreators: room creators hold an effectively unbounded power
	// level and can be added to the room (room version 12).
	PrivilegedCreators bool
}

// Redaction profile identifiers.
const (
	// RedactionPreV11: the strip list protects origin, membership and
	// prev_state, and the content allow lists are the historic ones.
	RedactionPreV11 = "pre-v11"
	// RedactionV11: origin, membership and prev_state are no longer protected;
	// m.room.create keeps its whole content, m.room.redaction keeps
	// content.redacts, m.room.power_levels additionally keeps "invite", and a
	// redacted m.room.member keeps the "signed" key of third_party_invite.
	RedactionV11 = "v11"
	// RedactionV12 is the version 12 profile, which additionally protects
	// relation information.
	RedactionV12 = "v12"
)

// registry holds the versions this server knows about. Keeping the table in one
// place is deliberate: a reviewer can diff it against the specification.
var registry = map[string]Rules{
	"1": {
		ID: "1", Stable: true, StateRes: StateResV1, EventIDs: EventIDServerAssigned,
		AuthRuleset: "v1", RedactionProfile: RedactionPreV11, SpecialCaseAliases: true,
		CreatorInCreateContent: true,
	},
	"2": {
		ID: "2", Stable: true, StateRes: StateResV2, EventIDs: EventIDServerAssigned,
		AuthRuleset: "v1", RedactionProfile: RedactionPreV11, SpecialCaseAliases: true,
		CreatorInCreateContent: true,
	},
	"3": {
		ID: "3", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashStdBase64,
		AuthRuleset: "v2", RedactionProfile: RedactionPreV11, SpecialCaseAliases: true,
		CreatorInCreateContent: true,
	},
	"4": {
		ID: "4", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v2", RedactionProfile: RedactionPreV11, SpecialCaseAliases: true,
		CreatorInCreateContent: true,
	},
	"5": {
		ID: "5", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v2", RedactionProfile: RedactionPreV11, SpecialCaseAliases: true,
		EnforceKeyValidity: true, CreatorInCreateContent: true,
	},
	"6": {
		ID: "6", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v3", RedactionProfile: RedactionPreV11,
		EnforceKeyValidity: true, StrictCanonicalJSON: true, LimitNotificationsPowerLevels: true,
		CreatorInCreateContent: true,
	},
	"7": {
		ID: "7", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v3", RedactionProfile: RedactionPreV11,
		EnforceKeyValidity: true, StrictCanonicalJSON: true, LimitNotificationsPowerLevels: true,
		SupportsKnock: true, CreatorInCreateContent: true,
	},
	"8": {
		ID: "8", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v3", RedactionProfile: RedactionPreV11,
		EnforceKeyValidity: true, StrictCanonicalJSON: true, LimitNotificationsPowerLevels: true,
		SupportsKnock: true, SupportsRestrictedJoin: true, CreatorInCreateContent: true,
	},
	"9": {
		ID: "9", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v3", RedactionProfile: RedactionPreV11, RedactionKeepsAuthorisedVia: true,
		EnforceKeyValidity: true, StrictCanonicalJSON: true, LimitNotificationsPowerLevels: true,
		SupportsKnock: true, SupportsRestrictedJoin: true, CreatorInCreateContent: true,
	},
	"10": {
		ID: "10", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v4", RedactionProfile: RedactionPreV11, RedactionKeepsAuthorisedVia: true,
		EnforceKeyValidity: true, StrictCanonicalJSON: true, LimitNotificationsPowerLevels: true,
		SupportsKnock: true, SupportsRestrictedJoin: true, SupportsKnockRestricted: true,
		EnforceIntegerPowerLevels: true, CreatorInCreateContent: true,
	},
	"11": {
		ID: "11", Stable: true, StateRes: StateResV2, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v5", RedactionProfile: RedactionV11, RedactionKeepsAuthorisedVia: true,
		EnforceKeyValidity: true, StrictCanonicalJSON: true, LimitNotificationsPowerLevels: true,
		SupportsKnock: true, SupportsRestrictedJoin: true, SupportsKnockRestricted: true,
		EnforceIntegerPowerLevels: true, RedactsInContent: true,
	},
	"12": {
		ID: "12", Stable: true, StateRes: StateResV2_1, EventIDs: EventIDHashURLSafeBase64,
		AuthRuleset: "v6", RedactionProfile: RedactionV12, RedactionKeepsAuthorisedVia: true,
		EnforceKeyValidity: true, StrictCanonicalJSON: true, LimitNotificationsPowerLevels: true,
		SupportsKnock: true, SupportsRestrictedJoin: true, SupportsKnockRestricted: true,
		EnforceIntegerPowerLevels: true, RedactsInContent: true,
		DomainlessRoomIDs: true, PrivilegedCreators: true,
	},
}

// supported lists the versions this server offers for new rooms, in the order it
// prefers them. Room versions 1 and 2 are last rather than first, and they are here
// because their differences are implemented rather than glossed over: the
// authorisation table those two versions share (which the later tables changed, in
// clauses that decide real events), State Resolution 1, event identifiers chosen by
// the origin server, references written as [event_id, {"sha256": hash}] pairs, and
// the pre-canonical-JSON forms a power level may take. A room in either version is a
// decade old in many cases and its rules have to be computed as that version states
// them, or two servers holding it disagree about who is in it.
//
// Room version 12 and the unstable versions are deliberately absent: this server has
// rules for them, so it can recognise and refuse them, but it does not implement the
// revised state resolution or the pseudo-identifier event format, and offering a
// version whose semantics cannot be delivered is a promise the server would break.
var supported = []string{"3", "4", "5", "6", "7", "8", "9", "10", "11", "1", "2"}

// defaultVersion is used when a client asks to create a room without naming a
// version.
const defaultVersion = "11"

// grammar is the room version grammar from the appendices: 1-32 characters from
// a-z, 0-9, "." and "-".
var grammar = regexp.MustCompile(`^[a-z0-9.-]{1,32}$`)

// Validate checks a room version string's grammar without requiring that the
// version is known. Room versions are opaque identifiers, so grammar is all that
// can be checked in general.
func Validate(id string) error {
	if id == "" {
		return fmt.Errorf("room version is empty")
	}
	if len(id) > 32 {
		return fmt.Errorf("room version %q is longer than 32 characters", id)
	}
	if !grammar.MatchString(id) {
		return fmt.Errorf("room version %q contains characters outside a-z, 0-9, . and -", id)
	}
	return nil
}

// Get returns the rules for a known version.
func Get(id string) (Rules, bool) {
	rules, ok := registry[id]
	return rules, ok
}

// MustGet returns the rules for a known version and panics if the version is
// unknown. Event processing paths use it after an explicit IsKnown check, so an
// unknown version reaching it is a programming error rather than hostile input.
func MustGet(id string) Rules {
	rules, ok := registry[id]
	if !ok {
		panic(fmt.Sprintf("roomversion: unknown room version %q", id))
	}
	return rules
}

// IsKnown reports whether this server has rules for the version.
func IsKnown(id string) bool {
	_, ok := registry[id]
	return ok
}

// IsOffered reports whether this server creates rooms in this version. A version can be
// known — its rules are registered, so its events can be read and a request for it can be
// answered honestly — and still not be offered, because part of what that version requires
// is not implemented. Keeping the two questions apart is what lets the server recognise a
// version without pretending to run it.
func IsOffered(id string) bool {
	for _, offered := range supported {
		if offered == id {
			return true
		}
	}
	return false
}

// Default returns the version used for new rooms.
func Default() Rules { return registry[defaultVersion] }

// Supported returns the versions this server offers for new rooms, in the order
// they should be preferred.
func Supported() []Rules {
	out := make([]Rules, 0, len(supported))
	for _, id := range supported {
		out = append(out, registry[id])
	}
	return out
}

// Pick chooses a room version from a client's preference: the requested version
// if this server supports it, otherwise the default. A requested version that is
// grammatically valid but unknown is reported as unsupported rather than silently
// replaced, because silently changing a room version changes the rules a client
// is operating under.
func Pick(requested string) (chosen Rules, usedDefault bool, err error) {
	if requested == "" {
		return Default(), true, nil
	}
	if err := Validate(requested); err != nil {
		return Rules{}, false, err
	}
	for _, rules := range Supported() {
		if rules.ID == requested {
			return rules, false, nil
		}
	}
	return Rules{}, false, fmt.Errorf("room version %q is not supported by this server", requested)
}

// Available renders the "available" map of the m.room_versions capability, which
// maps each version this server supports to "stable" or "unstable".
func Available() map[string]string {
	out := make(map[string]string, len(registry))
	for id, rules := range registry {
		if !isOffered(id) {
			continue
		}
		if rules.Stable {
			out[id] = "stable"
		} else {
			out[id] = "unstable"
		}
	}
	return out
}

// isOffered reports whether a version should be advertised. Only versions this
// server can actually create rooms in are advertised; the rest of the registry
// exists so that rooms and events already in another version are still handled
// with the correct rules.
func isOffered(id string) bool {
	for _, want := range supported {
		if want == id {
			return true
		}
	}
	return false
}

// All returns every version this server has rules for, including unsupported
// ones. It is used by tests and diagnostics, never for negotiation.
func All() []string {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	return ids
}

// String renders the version plus the algorithms that matter most when reading a
// log line about a room.
func (r Rules) String() string {
	var flags []string
	if r.EnforceIntegerPowerLevels {
		flags = append(flags, "int-pl")
	}
	if r.RedactsInContent {
		flags = append(flags, "redacts-in-content")
	}
	if r.DomainlessRoomIDs {
		flags = append(flags, "domainless-room-ids")
	}
	if len(flags) == 0 {
		return fmt.Sprintf("%s(state=%s,auth=%s)", r.ID, r.StateRes, r.AuthRuleset)
	}
	return fmt.Sprintf("%s(state=%s,auth=%s,%s)", r.ID, r.StateRes, r.AuthRuleset, strings.Join(flags, ","))
}
