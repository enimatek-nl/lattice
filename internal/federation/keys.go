package federation

import (
	"context"
	"fmt"
	"strings"

	"github.com/enimatek/lattice/internal/matrixid"
)

// QueryDeviceKeys asks another homeserver for the device key documents of the users it carries.
//
// A homeserver is the only source for the key documents its own users' devices uploaded: they are
// signed by those devices, and the client that eventually reads them is the one that checks the
// signatures. So the answer is not verified further here, and cannot be — but its shape is checked,
// because the alternative is letting one server speak for another server's users. Only entries for
// the users that were asked about are kept, and only entries whose user ID sits in the namespace of
// the server that was asked. A peer that volunteers keys for somebody else's users is not believed,
// which matters most for the users nobody asked about: a poisoned entry for a user a client happens
// to trust later is exactly the sort of thing a key distribution system has to refuse at its edges.
func (c *Client) QueryDeviceKeys(ctx context.Context, destination string, users map[string][]string) (map[string]map[string]any, error) {
	if len(users) == 0 {
		return map[string]map[string]any{}, nil
	}
	answer, err := c.PostJSON(ctx, destination, "/_matrix/federation/v1/user/keys/query", map[string]any{
		"device_keys": users,
	})
	if err != nil {
		return nil, err
	}
	return deviceKeysAnswer(destination, users, answer)
}

// deviceKeysAnswer reads a key query answer, keeping only what was asked for and only from the
// server that was asked. It is separated from the request so that the keeping can be examined
// without a network in the way.
func deviceKeysAnswer(destination string, asked map[string][]string, answer map[string]any) (map[string]map[string]any, error) {
	reported, ok := answer["device_keys"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("federation: %s answered a device key query without a device_keys object", destination)
	}
	askedDomain, _, err := matrixid.SplitServerName(destination)
	if err != nil {
		return nil, fmt.Errorf("federation: %q is not a server name: %w", destination, err)
	}
	out := make(map[string]map[string]any, len(asked))
	for userID, value := range reported {
		want, askedUser := asked[userID]
		if !askedUser {
			// Not part of the question.
			continue
		}
		_, domain, err := matrixid.SplitID(matrixid.SigilUser, userID)
		if err != nil || !sameHost(domain, askedDomain) {
			// The server was asked about users in its own namespace. Anything it says
			// about somebody else's is not its answer to give.
			continue
		}
		devices, ok := value.(map[string]any)
		if !ok {
			continue
		}
		kept := map[string]any{}
		for deviceID, document := range devices {
			if len(want) > 0 && !deviceListContains(want, deviceID) {
				// A client that named the devices it wants is not handed the others.
				continue
			}
			if _, isObject := document.(map[string]any); !isObject {
				continue
			}
			kept[deviceID] = document
		}
		out[userID] = kept
	}
	// A user who was asked about and not reported still has to appear. A client cannot tell
	// "this server holds nothing about that user" apart from "no answer arrived" unless the
	// answer says which it is.
	for userID := range asked {
		if _, ok := out[userID]; !ok {
			out[userID] = map[string]any{}
		}
	}
	return out, nil
}

// sameHost compares two DNS names, which are case-insensitive unlike the rest of an identifier.
func sameHost(a, b string) bool { return strings.EqualFold(a, b) }

func deviceListContains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
