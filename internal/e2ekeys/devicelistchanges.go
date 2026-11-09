package e2ekeys

import (
	"context"
	"fmt"
	"sort"

	"github.com/enimatek/lattice/internal/persist"
)

// DeviceChanges answers the range question a client asks about device lists: between these two
// positions in this server's stream, whose device lists did this observer need to re-read, and
// whose stopped being theirs to track.
//
// The rows it reads are the ones `/sync` reads, addressed to the observer at the moment the
// change happened. That addressing is the whole reason the answer is honest: permission to hear
// about a user is room membership at the time, and it cannot be worked out afterwards — the
// person who most needs to hear that a room's members are no longer theirs to track is the one
// who is no longer in that room.
//
// The same rule as the sync section, on purpose: `GET /keys/changes` and the `device_lists`
// section of a sync are the same fact asked two ways, and a client that gets one answer from
// each would have no way to tell which to believe.
//
// It no longer drops the observer's own changes wholesale, and the comment that said it should
// was written before a multi-device client was considered: see the loop below for the rule that
// replaced it.
func (s *Service) DeviceChanges(ctx context.Context, observerID string, from, to int64) (changed, left []string, err error) {
	rows, err := s.devices.ListFor(ctx, observerID, from, to)
	if err != nil {
		return nil, nil, fmt.Errorf("e2ekeys: cannot read the device list changes of %s: %w", observerID, err)
	}
	changed, left = DeviceListSections(rows, observerID)
	return changed, left, nil
}

// DeviceListSections turns addressed change rows into the two lists a client is given. It is
// shared with the sync service for the reason above, and it drops the observer's own changes:
// a client does not need to be told to re-read the device it is holding.
func DeviceListSections(rows []*persist.DeviceListChange, observerID string) (changed, left []string) {
	seenChanged, seenLeft := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if row.UserID == "" {
			continue
		}
		// A user's own changes are kept for `changed` and dropped for `left`, which is the
		// difference between two questions: "has this user's device list changed?" is news to
		// this user's *other* devices — a device nobody announces is a device that never gets an
		// encryption key, and no room timeline carries that news — while "has this user gone?"
		// is not news to anybody who went with them.
		if row.UserID == observerID && row.Kind != persist.DeviceListChanged {
			continue
		}
		switch row.Kind {
		case persist.DeviceListChanged:
			if !seenChanged[row.UserID] {
				seenChanged[row.UserID] = true
				changed = append(changed, row.UserID)
			}
		case persist.DeviceListLeft:
			if !seenLeft[row.UserID] {
				seenLeft[row.UserID] = true
				left = append(left, row.UserID)
			}
		}
	}
	sort.Strings(changed)
	sort.Strings(left)
	return changed, left
}
