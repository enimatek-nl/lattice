package sync

// Reading the user's own data.
//
// Account data is not room state and not an event: it is a value the user set, such as
// their push rules. It still carries a position in the same stream, because the question
// `/sync` asks — "what has happened to me since I last looked" — has to cover the things
// the user changed as well as the things a room did. A client that turns a notification
// rule off expects its next sync to come back with the new rules, not to time out.

import (
	"context"
	"encoding/json"
	"fmt"
)

// accountEvents returns the user's account data events that are newer than since.
//
// The stored content is handed on as it was stored. Re-decoding it into a struct this
// server prefers would let us drop keys we do not model, and the user would find their
// own data changed by having read it.
func (s *Service) accountEvents(ctx context.Context, userID string, since int64) ([]Event, error) {
	rows, err := s.account.ListSince(ctx, userID, since, nil)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read the user's account data: %w", err)
	}
	events := make([]Event, 0, len(rows))
	for _, row := range rows {
		var content any
		if err := json.Unmarshal([]byte(row.Content), &content); err != nil {
			return nil, fmt.Errorf("sync: the stored account data %q for %s is not JSON: %w", row.DataType, userID, err)
		}
		events = append(events, Event{
			"type":    row.DataType,
			"content": content,
		})
	}
	return events, nil
}
