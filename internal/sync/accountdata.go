package sync

// Reading the user's own data.
//
// Account data is not room state and not an event: it is a value the user set, such as
// their push rules, their tags, or an annotation about one particular room. It still
// carries a position in the same stream, because the question `/sync` asks — "what has
// happened to me since I last looked" — has to cover the things the user changed as well as
// the things a room did. A client that turns a notification rule off expects its next sync
// to come back with the new rules, not to time out.

import (
	"context"
	"encoding/json"
	"fmt"
)

// accountScopes reads a user's account data since a position and splits it by scope: the
// user's own values, and one list per room. One read per sync, because a sync that walks
// fifty rooms would otherwise ask fifty times about data it already has.
//
// A removed type is reported rather than hidden — but only to a client that has something
// to forget. MSC3391's rule is that an incremental sync carries the type with an empty
// content object, while an initial sync, where this server is describing the whole of a
// user's data and the client holds nothing yet, omits it. `since` says which of the two
// this is: 0 means the client asked for everything.
func (s *Service) accountScopes(ctx context.Context, userID string, since int64) ([]Event, map[string][]Event, error) {
	rows, err := s.account.ListScopesSince(ctx, userID, since)
	if err != nil {
		return nil, nil, fmt.Errorf("sync: cannot read the user's account data: %w", err)
	}
	initial := since == 0
	var global []Event
	byRoom := map[string][]Event{}
	for _, row := range rows {
		if row.Deleted && initial {
			continue
		}
		content := any(map[string]any{})
		if !row.Deleted {
			if err := json.Unmarshal([]byte(row.Content), &content); err != nil {
				return nil, nil, fmt.Errorf("sync: the stored account data %q for %s is not JSON: %w", row.DataType, userID, err)
			}
		}
		event := Event{"type": row.DataType, "content": content}
		if row.RoomID == "" {
			global = append(global, event)
			continue
		}
		byRoom[row.RoomID] = append(byRoom[row.RoomID], event)
	}
	return global, byRoom, nil
}

// roomAccountData answers one room's section. A room nothing of theirs changed in answers
// with an empty list rather than a missing field: the section is part of the room's shape,
// and a client that has to test for its presence will get it wrong in one direction or the
// other.
func roomAccountData(byRoom map[string][]Event, roomID string) Events {
	if events, ok := byRoom[roomID]; ok && len(events) > 0 {
		return Events{Events: events}
	}
	return Events{Events: []Event{}}
}
