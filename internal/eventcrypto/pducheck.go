package eventcrypto

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// PDUCheck answers the first of the checks the specification performs on an event that
// arrives from another server: is this a valid event for its room version at all. It is
// worth answering on its own, because an event that is not a valid event has no
// trustworthy identity - its id, its hash and its signature are all defined over the
// shape the room version gives it - and because the answer costs nothing compared to
// fetching a signing key over the network to check an event that was never an event.
//
// The check is per room version, which is why it takes the version rather than reading
// it from the event: the room's create event names the version, and an event that
// disagrees about the version is a different event under its own rules.
//
// The rules checked here are the shape of the object, not what it says. Whether the
// sender was allowed to say it is the authorization rules' question, and whether the
// event really came from the server it names is the signature's question.
//
// A problem is reported as an error naming the field, in words an operator can act on.
// The caller drops the event either way; the reason is for the log.
func PDUCheck(event matrix.Object, roomVersion string) error {
	version, err := roomVersionNumber(roomVersion)
	if err != nil {
		return fmt.Errorf("the room version %q is not one this server can check an event against", roomVersion)
	}
	if event == nil {
		return fmt.Errorf("a PDU must be a JSON object")
	}
	legacy := version <= 2
	latest := UseRoom11Algorithms(roomVersion)

	for _, field := range []string{"type", "sender", "room_id", "event_id"} {
		if _, ok := event[field]; !ok {
			// Room version 3 and later derive the event id, and a PDU arriving over
			// federation carries the derived id; a PDU without one is a caller problem
			// rather than an invalid event, so the id is only required to be a string
			// when present.
			if field == "event_id" {
				continue
			}
			return fmt.Errorf("a PDU must carry %s", field)
		}
		if _, ok := event[field].(string); !ok {
			return fmt.Errorf("the %s of a PDU must be a string", field)
		}
	}
	if sender, _ := event["sender"].(string); !strings.HasPrefix(sender, "@") || !strings.Contains(sender, ":") {
		return fmt.Errorf("the sender of a PDU must be a user id")
	}
	if room, _ := event["room_id"].(string); !strings.HasPrefix(room, "!") || !strings.Contains(room, ":") {
		return fmt.Errorf("the room_id of a PDU must be a room id")
	}
	if id, ok := event["event_id"].(string); ok && id != "" {
		if err := checkEventID(id, version); err != nil {
			return err
		}
	}
	for _, field := range []string{"prev_events", "auth_events"} {
		list, ok := event[field].([]any)
		if !ok {
			return fmt.Errorf("the %s of a PDU must be an array of event ids", field)
		}
		for _, entry := range list {
			id, ok := entry.(string)
			if !ok {
				return fmt.Errorf("the %s of a PDU must hold event ids", field)
			}
			if err := checkEventID(id, version); err != nil {
				return err
			}
		}
	}
	if _, ok := asObjectField(event, "content"); !ok {
		return fmt.Errorf("the content of a PDU must be an object")
	}
	if _, err := StoredContentHash(event); err != nil {
		return fmt.Errorf("a PDU must carry a content hash: %v", err)
	}
	signatures, ok := asObjectField(event, "signatures")
	if !ok || len(signatures) == 0 {
		return fmt.Errorf("a PDU must carry at least one signature")
	}
	if _, err := integerField(event, "origin_server_ts", 0); err != nil {
		return fmt.Errorf("the origin_server_ts of a PDU %v", err)
	}
	if _, err := integerField(event, "depth", 0); err != nil {
		return fmt.Errorf("the depth of a PDU %v", err)
	}
	if stateKey, present := event["state_key"]; present {
		if _, ok := stateKey.(string); !ok {
			return fmt.Errorf("the state_key of a PDU must be a string")
		}
	}

	// The properties only old room versions have. Newer versions define no origin and
	// no prev_state, but that is a list of what a sender has to send rather than a list
	// of what it may not: an event carrying an extra member is still the event that was
	// signed, because the hash and the signature cover the members that are there. A
	// receiver that refuses one over a member the version dropped is refusing a room it
	// could otherwise read - gomatrixserverlib puts origin in every event it builds,
	// room version 11 and all, which is how this was found: the state of a room joined
	// over federation could not be stored.
	if legacy {
		if origin, ok := event["origin"].(string); !ok || origin == "" {
			return fmt.Errorf("a PDU in room version %d must name the server it came from in origin", version)
		}
		if _, ok := event["prev_state"].([]any); !ok {
			return fmt.Errorf("a PDU in room version %d must carry prev_state", version)
		}
		if _, ok := event["event_id"].(string); !ok {
			return fmt.Errorf("a PDU in room version %d must carry an event id the origin server chose", version)
		}
	}

	if typ, _ := event["type"].(string); typ == "m.room.member" && legacy {
		content, _ := asObjectField(event, "content")
		if membership, present := content["membership"]; present {
			if _, ok := membership.(string); !ok {
				return fmt.Errorf("the membership of an m.room.member event must be a string")
			}
		}
	}
	if typ, _ := event["type"].(string); typ == "m.room.redaction" {
		content, _ := asObjectField(event, "content")
		target, inContent := content["redacts"].(string)
		_, atTop := event["redacts"].(string)
		if latest {
			if atTop {
				return fmt.Errorf("a redaction in room version %d names its target in content, not beside it", version)
			}
			if !inContent || target == "" {
				return fmt.Errorf("a redaction in room version %d must name the event it redacts in content.redacts", version)
			}
			if err := checkEventID(target, version); err != nil {
				return err
			}
		} else {
			target, atTop = event["redacts"].(string)
			if !atTop || target == "" {
				return fmt.Errorf("a redaction in room version %d must name the event it redacts in redacts", version)
			}
			if err := checkEventID(target, version); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkEventID checks the shape an event id has in a room version. Versions 1 and 2 take
// an id the origin server made up, spelled $localpart:domain; version 3 and later derive
// it as $base64url-of-the-hash, with no domain to speak of.
func checkEventID(id string, version int) error {
	if id == "" || !strings.HasPrefix(id, "$") {
		return fmt.Errorf("the event id %q is not an event id", id)
	}
	body := id[1:]
	if version <= 2 {
		if !strings.Contains(body, ":") {
			return fmt.Errorf("the event id %q is missing the domain room version %d asks for", id, version)
		}
		return nil
	}
	if strings.Contains(body, ":") {
		return fmt.Errorf("the event id %q is spelled the way room version 2 spells ids, which version %d does not",
			id, version)
	}
	return nil
}

// asObjectField reads an object property of a PDU, accepting the alias this codebase
// uses and the plain map a decoder produces.
func asObjectField(event matrix.Object, field string) (matrix.Object, bool) {
	switch typed := event[field].(type) {
	case map[string]any:
		return typed, true
	default:
		return nil, false
	}
}

// integerField reads a property that a room version insists is a whole number. A PDU
// decoded by the standard library carries float64, which cannot hold every 64-bit
// integer: a value that has already lost digits is not a valid timestamp or depth, and
// saying so is better than ordering the room by a rounded number.
func integerField(event matrix.Object, field string, minimum int64) (int64, error) {
	switch typed := event[field].(type) {
	case int64:
		if typed < minimum {
			return 0, fmt.Errorf("is %d, below the least it may be", typed)
		}
		return typed, nil
	case int:
		if int64(typed) < minimum {
			return 0, fmt.Errorf("is %d, below the least it may be", typed)
		}
		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("is not a whole number: %s", typed.String())
		}
		if parsed < minimum {
			return 0, fmt.Errorf("is %d, below the least it may be", parsed)
		}
		return parsed, nil
	case float64:
		if typed != float64(int64(typed)) {
			return 0, fmt.Errorf("is %v, which is not a whole number", typed)
		}
		if int64(typed) < minimum {
			return 0, fmt.Errorf("is %d, below the least it may be", int64(typed))
		}
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("is missing or is not a number")
	}
}
