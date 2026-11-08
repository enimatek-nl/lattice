package sync

import (
	"context"
	"fmt"

	"github.com/enimatek/lattice/internal/persist"
)

// Typing reaches a client as an ephemeral `m.typing` event in the room's section, holding the user
// IDs typing in that room at this moment. The room's section says which room it is, so the event
// carries no `room_id` — that field exists over federation, and putting it here would ask every
// client to strip it back out.
//
// The list is always the room's *current* answer, never a history of changes inside the window: an
// indicator is a present tense, and a client that has been away does not want to be told that
// three people were typing in between, it wants to know who is now.
//
// What the window decides is whether this room's typing is news to this client at all — the room's
// last typing change is a position in the same stream events use, so "did it change since my
// token" has the same answer here as anywhere else. That includes the change to nobody typing: an
// indicator timing out is swept into a position (see [Service.typingSweep]) precisely so that the
// empty list can be delivered, rather than leaving a hand raised on the client's screen forever.
func (s *Service) typingEvents(ctx context.Context, roomID string, since, until int64) ([]Event, error) {
	if err := s.typing.Sweep(ctx, roomID); err != nil {
		return nil, err
	}
	users, err := s.typing.Users(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read the typing indicators of %s: %w", roomID, err)
	}
	if since == 0 && until == 0 {
		// A first sync has no window and asks what is true now. An empty answer is not worth an
		// event in every room the user is in.
		if len(users) == 0 {
			return nil, nil
		}
		return []Event{typingEvent(users)}, nil
	}
	last, err := s.typing.LastChange(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read when %s last saw typing: %w", roomID, err)
	}
	if last == 0 || last <= since {
		// This room's typing has not changed since this client last asked.
		return nil, nil
	}
	// The sweep above may have taken a position above the one this response reports as
	// `next_batch`, in which case this change sits in the client's *next* window as well and it
	// is sent the same current list twice. That is harmless — an ephemeral event with the same
	// content is the same statement — and it is better than the alternative of not telling the
	// client that somebody stopped typing.
	return []Event{typingEvent(users)}, nil
}

func typingEvent(users []string) Event {
	if users == nil {
		users = []string{}
	}
	return Event{
		"type":    persist.TypingEDUType,
		"content": map[string]any{"user_ids": users},
	}
}
