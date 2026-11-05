// Package redaction implements the room-version-specific redaction algorithm.
//
// Redaction matters far more than "hiding a message": the reference hash of an
// event — and therefore its event ID in room versions 3 and later — is computed
// over the *redacted* form of the event, and the signature is computed over the
// redacted form too. If this server strips a key that other servers keep, it
// derives a different event ID for the same bytes and the room forks. The key
// lists below are therefore transcribed from the pinned specification
// (Matrix v1.19, per-version "Redactions" sections) and cross-checked against
// gomatrixserverlib, which produces byte-identical redacted forms for room
// versions 1-11.
package redaction

import (
	"github.com/enimatek/lattice/internal/roomversion"
)

// TargetOf returns the event a redaction event asks for.
//
// Where that ID lives depends on the room version: versions up to 10 carry a top-level
// `redacts`, and version 11 moved it to `content.redacts`. In version 11 and later the
// top-level key is ignored rather than tried as a fallback. That is not pedantry — an
// event carrying both keys with different targets is a claim about which event is to be
// destroyed, and letting the sender decide after the fact would mean a single signed
// event could be offered to two servers with two different results.
func TargetOf(rules roomversion.Rules, event map[string]any) (string, bool) {
	if rules.RedactsInContent {
		if content, ok := event["content"].(map[string]any); ok {
			if id, ok := content["redacts"].(string); ok && id != "" {
				return id, true
			}
		}
		return "", false
	}
	if id, ok := event["redacts"].(string); ok && id != "" {
		return id, true
	}
	return "", false
}

// ForClient adjusts the `redacts` field of a redaction event for the client reading it,
// in the direction the room version does not already cover: a version 11 client is shown a
// top-level `redacts` as well, for clients written against older versions; an older room
// version is shown `content.redacts` as well. The stored event is never changed — an
// added field would alter the reference hash, and with it the event's identity.
func ForClient(rules roomversion.Rules, event map[string]any) {
	eventType, _ := event["type"].(string)
	if eventType != "m.room.redaction" {
		return
	}
	content, _ := event["content"].(map[string]any)
	if content == nil {
		content = map[string]any{}
		event["content"] = content
	}
	if rules.RedactsInContent {
		if id, ok := content["redacts"].(string); ok && id != "" {
			if _, present := event["redacts"].(string); !present {
				event["redacts"] = id
			}
			return
		}
	}
	if id, ok := event["redacts"].(string); ok && id != "" {
		if _, present := content["redacts"].(string); !present {
			content["redacts"] = id
		}
	}
}

// Apply returns a redacted copy of event using the rules of the given room
// version. The input is never modified: callers need the unredacted form to
// verify content hashes and signatures afterwards.
func Apply(rules roomversion.Rules, event map[string]any) map[string]any {
	kept := map[string]any{}
	for _, key := range topLevelKeys(rules) {
		if value, ok := event[key]; ok {
			kept[key] = deepCopy(value)
		}
	}

	eventType, _ := event["type"].(string)
	if keepAllContent(rules, eventType) {
		if content, ok := event["content"].(map[string]any); ok {
			kept["content"] = deepCopy(content)
		} else {
			kept["content"] = map[string]any{}
		}
		return kept
	}

	content, ok := event["content"].(map[string]any)
	if !ok {
		// A missing or non-object content becomes an empty object: the
		// specification requires `content` to be present, and inventing keys is
		// worse than dropping them.
		kept["content"] = map[string]any{}
		return kept
	}

	allowed := contentKeys(rules, eventType)
	if allowed == nil {
		kept["content"] = map[string]any{}
		return kept
	}

	newContent := map[string]any{}
	for _, key := range allowed {
		if value, ok := content[key]; ok {
			newContent[key] = deepCopy(value)
		}
	}

	// Room version 11 stopped redacting the `signed` object of a third-party
	// invite: dropping it makes the membership event unverifiable, which breaks
	// the auth chain of everything that follows it.
	if rules.RedactionProfile == roomversion.RedactionV11 || rules.RedactionProfile == roomversion.RedactionV12 {
		if thirdParty, ok := content["third_party_invite"].(map[string]any); ok {
			if signed, ok := thirdParty["signed"]; ok {
				newContent["third_party_invite"] = map[string]any{"signed": deepCopy(signed)}
			}
		}
	}

	kept["content"] = newContent
	return kept
}

// topLevelKeys returns the properties protected from redaction.
func topLevelKeys(rules roomversion.Rules) []string {
	switch rules.RedactionProfile {
	case roomversion.RedactionV11, roomversion.RedactionV12:
		// origin, membership and prev_state are no longer protected from
		// room version 11 onwards.
		return []string{
			"event_id", "type", "room_id", "sender", "state_key", "content",
			"hashes", "signatures", "depth", "prev_events", "auth_events",
			"origin_server_ts",
		}
	default:
		return []string{
			"event_id", "type", "room_id", "sender", "state_key", "content",
			"hashes", "signatures", "depth", "prev_events", "prev_state",
			"auth_events", "origin", "origin_server_ts", "membership",
		}
	}
}

// contentKeys returns the content keys protected for an event type, or nil when
// every content key is dropped.
func contentKeys(rules roomversion.Rules, eventType string) []string {
	switch eventType {
	case "m.room.member":
		keys := []string{"membership"}
		if rules.RedactionKeepsAuthorisedVia {
			keys = append(keys, "join_authorised_via_users_server")
		}
		return keys

	case "m.room.create":
		// Room version 11 and later keep the whole content (see keepAllContent):
		// room_version and predecessor must survive redaction.
		if rules.CreatorInCreateContent {
			return []string{"creator"}
		}
		return []string{}

	case "m.room.join_rules":
		if rules.SupportsRestrictedJoin {
			return []string{"join_rule", "allow"}
		}
		return []string{"join_rule"}

	case "m.room.power_levels":
		keys := []string{"ban", "events", "events_default", "kick", "redact", "state_default", "users", "users_default"}
		if rules.RedactionProfile == roomversion.RedactionV11 || rules.RedactionProfile == roomversion.RedactionV12 {
			// The invite level became protected in room version 11.
			keys = append(keys, "invite")
		}
		return keys

	case "m.room.history_visibility":
		return []string{"history_visibility"}

	case "m.room.aliases":
		// m.room.aliases lost its special casing in room version 6.
		if rules.SpecialCaseAliases {
			return []string{"aliases"}
		}
		return []string{}

	case "m.room.redaction":
		if rules.RedactionProfile == roomversion.RedactionV11 || rules.RedactionProfile == roomversion.RedactionV12 {
			// The target moved into content for room version 11 and must survive.
			return []string{"redacts"}
		}
		return []string{}

	default:
		return []string{}
	}
}

// keepAllContent reports whether the whole content object is protected, which is
// true for m.room.create in room version 11 and later.
func keepAllContent(rules roomversion.Rules, eventType string) bool {
	return eventType == "m.room.create" &&
		(rules.RedactionProfile == roomversion.RedactionV11 || rules.RedactionProfile == roomversion.RedactionV12)
}

// IsRedacted reports whether an event looks redacted: a redacted event keeps a
// content object with no keys except the protected ones for its type. This is a
// heuristic used to decide whether to trust a received `redacted` flag, not an
// authorisation decision.
func IsRedacted(rules roomversion.Rules, event map[string]any) bool {
	content, ok := event["content"].(map[string]any)
	if !ok {
		return true
	}
	eventType, _ := event["type"].(string)
	if keepAllContent(rules, eventType) {
		return false
	}
	allowed := contentKeys(rules, eventType)
	if allowed == nil {
		return len(content) == 0
	}
	if len(allowed) == 0 {
		return len(content) == 0
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range content {
		if _, ok := allowedSet[key]; !ok {
			return false
		}
	}
	return true
}

// deepCopy copies JSON values so the caller's event is untouched.
func deepCopy(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = deepCopy(item)
		}
		return out
	default:
		return value
	}
}
