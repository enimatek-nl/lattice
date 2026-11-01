package service

import (
	"context"
	"encoding/json"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// AccountData returns one account data entry. roomID is empty for the global
// scope. A missing entry is reported as a 404, as the spec requires.
func (s *Service) AccountData(ctx context.Context, owner, requester, roomID, typ string) (matrix.RawMessage, error) {
	if owner != requester {
		return nil, matrix.ErrForbidden("cannot read the account data of another user")
	}
	row, err := s.db.AccountDataRepo.Get(owner, roomID, typ)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("account data %s not found", typ)
		}
		return nil, matrix.ErrInternal(err)
	}
	return matrix.RawMessage(row.Content), nil
}

// SetAccountData writes one account data entry and wakes sync.
func (s *Service) SetAccountData(ctx context.Context, owner, requester, roomID, typ string, content matrix.RawMessage) error {
	if owner != requester {
		return matrix.ErrForbidden("cannot write the account data of another user")
	}
	if !json.Valid(content) || len(content) == 0 {
		return matrix.ErrNotJSON("account data content must be valid JSON")
	}
	if err := s.db.AccountDataRepo.Set(owner, roomID, typ, string(content), s.nextStreamPos()); err != nil {
		return matrix.ErrInternal(err)
	}
	s.Notify()
	return nil
}

// Tag is a room tag with its ordering weight.
type Tag struct {
	Order any `json:"order,omitempty"`
}

// RoomTags returns the tags of a room for a user.
func (s *Service) RoomTags(ctx context.Context, owner, requester, roomID string) (map[string]Tag, error) {
	if owner != requester {
		return nil, matrix.ErrForbidden("cannot read the tags of another user")
	}
	if _, err := s.MustRoom(ctx, roomID); err != nil {
		return nil, err
	}
	types, err := s.db.AccountDataRepo.RoomTypes(owner, roomID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	tags := map[string]Tag{}
	content, ok := types[matrix.TypeTag]
	if !ok {
		return tags, nil
	}
	var body struct {
		Tags map[string]Tag `json:"tags"`
	}
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return body.Tags, nil
}

// SetRoomTag adds or updates a tag on a room.
func (s *Service) SetRoomTag(ctx context.Context, owner, requester, roomID, tag string, order any) error {
	if owner != requester {
		return matrix.ErrForbidden("cannot write the tags of another user")
	}
	tags, err := s.RoomTags(ctx, owner, requester, roomID)
	if err != nil {
		return err
	}
	tags[tag] = Tag{Order: order}
	if err := s.writeTags(ctx, owner, roomID, tags); err != nil {
		return err
	}
	return nil
}

// DeleteRoomTag removes a tag from a room.
func (s *Service) DeleteRoomTag(ctx context.Context, owner, requester, roomID, tag string) error {
	if owner != requester {
		return matrix.ErrForbidden("cannot write the tags of another user")
	}
	tags, err := s.RoomTags(ctx, owner, requester, roomID)
	if err != nil {
		return err
	}
	if _, ok := tags[tag]; !ok {
		return matrix.ErrNotFound("no such tag %s", tag)
	}
	delete(tags, tag)
	return s.writeTags(ctx, owner, roomID, tags)
}

// writeTags persists the tag map of a room.
func (s *Service) writeTags(ctx context.Context, owner, roomID string, tags map[string]Tag) error {
	content := matrix.Encode(matrix.Object{"tags": tags})
	return s.SetAccountData(ctx, owner, owner, roomID, matrix.TypeTag, content)
}

// CreateFilter stores a filter definition and returns its id.
func (s *Service) CreateFilter(ctx context.Context, owner, requester, body string) (string, error) {
	if owner != requester {
		return "", matrix.ErrForbidden("cannot create filters for another user")
	}
	if !json.Valid([]byte(body)) {
		return "", matrix.ErrNotJSON("filter body must be valid JSON")
	}
	if err := validateFilter(body); err != nil {
		return "", err
	}
	id, err := s.db.FilterRepo.NextFilterID()
	if err != nil {
		return "", matrix.ErrInternal(err)
	}
	if err := s.db.FilterRepo.Create(&persist.Filter{
		UserID:   owner,
		FilterID: id,
		Body:     body,
	}); err != nil {
		return "", matrix.ErrInternal(err)
	}
	return id, nil
}

// GetFilter returns a stored filter definition.
func (s *Service) GetFilter(ctx context.Context, owner, requester, filterID string) (matrix.Object, error) {
	if owner != requester {
		return nil, matrix.ErrForbidden("cannot read the filters of another user")
	}
	filter, err := s.db.FilterRepo.Get(owner, filterID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no such filter %s", filterID)
		}
		return nil, matrix.ErrInternal(err)
	}
	return matrix.MustParseObject([]byte(filter.Body)), nil
}
