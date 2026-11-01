package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// RoomState implements GET /_matrix/client/v3/rooms/{roomId}/state and returns
// the current state events of a room.
func (s *Service) RoomState(ctx context.Context, roomID, userID string, types []string) ([]matrix.Event, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.StateRepo.Events(roomID, types)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	events := make([]matrix.Event, 0, len(rows))
	for _, row := range rows {
		events = append(events, row.Matrix())
	}
	return events, nil
}

// StateContent implements GET /_matrix/client/v3/rooms/{roomId}/state/{type}
// and its /{stateKey} variant.
func (s *Service) StateContent(ctx context.Context, roomID, typ, stateKey, userID string) (matrix.RawMessage, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return nil, err
	}
	raw, err := s.db.StateRepo.Content(roomID, typ, stateKey)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no state event of type %q with key %q", typ, stateKey)
		}
		return nil, matrix.ErrInternal(err)
	}
	return raw, nil
}

// StateEventFor returns the full event that holds a state key, for the read
// endpoints that ask for the event rather than its content
// (?format=event). Membership is checked the same way as for the content, so
// asking for the whole event cannot widen what a caller may read.
func (s *Service) StateEventFor(ctx context.Context, roomID, typ, stateKey, userID string) (matrix.Event, error) {
	if err := s.RequireReadable(ctx, roomID, userID); err != nil {
		return matrix.Event{}, err
	}
	event, err := s.StateEvent(ctx, roomID, typ, stateKey)
	if err != nil {
		return matrix.Event{}, err
	}
	return event.Matrix(), nil
}

// StateEvent returns the full event that currently holds a state key.
func (s *Service) StateEvent(ctx context.Context, roomID, typ, stateKey string) (*persist.Event, error) {
	state, err := s.db.StateRepo.Get(roomID, typ, stateKey)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no state event of type %q with key %q", typ, stateKey)
		}
		return nil, matrix.ErrInternal(err)
	}
	event, err := s.db.EventRepo.Get(state.EventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("state event %s is missing", state.EventID)
		}
		return nil, matrix.ErrInternal(err)
	}
	return event, nil
}
