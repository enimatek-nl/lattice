package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// AccountData is one user's own key/value store: the document is whatever the client put
// there, and only that user reads or writes it.
//
// It is not room state and it is not server configuration. Two of its types matter to the
// server itself: `m.direct`, which is how a user records which rooms are private
// conversations, and the client's own preferences, which a server stores without
// interpreting. That is why the content is kept as written: a server that decoded a client's
// document into its own structs would drop the keys it did not know about, and the client
// would find its settings quietly narrowed.
type AccountDataService struct {
	accountData persist.AccountDataRepo
}

// NewAccountDataService returns the service over an account data repository.
func NewAccountDataService(repo persist.AccountDataRepo) *AccountDataService {
	return &AccountDataService{accountData: repo}
}

// Get returns one type of a user's own account data, or M_NOT_FOUND when there is none.
//
// "None" now has two shapes, and both answer 404: a type that was never written, and a type
// that was removed. MSC3391 made an empty content object a removal rather than a stored
// empty document, so the empty document a client could once write and read back is gone by
// design — and a read that answered `{}` for a removed type would leave a client unable to
// tell "you never had one" from "the one you had is gone", which is the difference that
// makes a deletion mean anything on the other side of a sync.
func (s *AccountDataService) Get(ctx context.Context, userID, dataType string) (json.RawMessage, error) {
	return s.get(ctx, userID, "", dataType)
}

// GetRoom returns one type of a user's account data *within* a room. The room is not a
// namespace decoration: room-scoped values are only ever read in that room's context, and
// the same type name in two rooms is two different pieces of data.
func (s *AccountDataService) GetRoom(ctx context.Context, userID, roomID, dataType string) (json.RawMessage, error) {
	if err := matrixid.RoomIDValid(roomID); err != nil {
		return nil, matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", roomID)
	}
	return s.get(ctx, userID, roomID, dataType)
}

func (s *AccountDataService) get(ctx context.Context, userID, roomID, dataType string) (json.RawMessage, error) {
	if err := validAccountDataType(dataType); err != nil {
		return nil, err
	}
	row, err := s.accountData.GetScoped(ctx, userID, roomID, dataType)
	if errors.Is(err, persist.ErrNotFound) {
		return nil, matrixerr.Newf(matrixerr.CodeNotFound,
			"No account data of type %q has been stored for this user.", dataType)
	}
	if err != nil {
		return nil, fmt.Errorf("users: cannot read account data: %w", err)
	}
	if row.Deleted {
		return nil, matrixerr.Newf(matrixerr.CodeNotFound,
			"The account data of type %q was removed for this user.", dataType)
	}
	return json.RawMessage(row.Content), nil
}

// Put stores one type of a user's account data. Writing the same type again replaces it:
// this is a current value, not a history, and sync reports the newest one.
func (s *AccountDataService) Put(ctx context.Context, userID, dataType string, content json.RawMessage) error {
	return s.put(ctx, userID, "", dataType, content)
}

// PutRoom stores one type of a user's account data within a room.
func (s *AccountDataService) PutRoom(ctx context.Context, userID, roomID, dataType string, content json.RawMessage) error {
	if err := matrixid.RoomIDValid(roomID); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", roomID)
	}
	return s.put(ctx, userID, roomID, dataType, content)
}

// put writes one scope's value, or removes it. MSC3391: a PUT whose content is an empty
// object is a deletion, not a stored empty document. That is the only way a client on an old
// API version can remove a type at all, and it is why the removal has to be remembered
// rather than dropped: the client that already holds the value must be told to forget it.
func (s *AccountDataService) put(ctx context.Context, userID, roomID, dataType string, content json.RawMessage) error {
	if err := validAccountDataType(dataType); err != nil {
		return err
	}
	var object map[string]any
	if err := json.Unmarshal(content, &object); err != nil {
		return matrixerr.New(matrixerr.CodeInvalidParam,
			"Account data is a JSON object.")
	}
	if object == nil {
		return matrixerr.New(matrixerr.CodeInvalidParam,
			"Account data is a JSON object, not null.")
	}
	if _, err := s.accountData.PutScoped(ctx, userID, roomID, dataType, string(content), len(object) == 0); err != nil {
		return fmt.Errorf("users: cannot store account data: %w", err)
	}
	return nil
}

// Delete removes one type of a user's own account data. A type that was never written is not
// an error to remove: the client asked for a state of affairs, and it holds either way.
func (s *AccountDataService) Delete(ctx context.Context, userID, dataType string) error {
	return s.delete(ctx, userID, "", dataType)
}

// DeleteRoom removes one type of a user's account data within a room.
func (s *AccountDataService) DeleteRoom(ctx context.Context, userID, roomID, dataType string) error {
	if err := matrixid.RoomIDValid(roomID); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", roomID)
	}
	return s.delete(ctx, userID, roomID, dataType)
}

func (s *AccountDataService) delete(ctx context.Context, userID, roomID, dataType string) error {
	if err := validAccountDataType(dataType); err != nil {
		return err
	}
	if _, err := s.accountData.PutScoped(ctx, userID, roomID, dataType, "{}", true); err != nil {
		return fmt.Errorf("users: cannot remove account data: %w", err)
	}
	return nil
}

// validAccountDataType rejects the path values that would not survive being stored: an empty
// type, and one longer than the column that holds it. The specification leaves the namespace
// to clients, so nothing here decides which names are meaningful.
func validAccountDataType(dataType string) error {
	if dataType == "" {
		return matrixerr.New(matrixerr.CodeInvalidParam, "An account data type is required.")
	}
	if len(dataType) > 255 {
		return matrixerr.New(matrixerr.CodeInvalidParam, "An account data type is too long.")
	}
	return nil
}
