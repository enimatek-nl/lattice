package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/enimatek/lattice/internal/matrixerr"
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

// Get returns one type of a user's account data, or an M_NOT_FOUND error when the user has
// never written that type. An empty document the client wrote is a real answer and is
// returned as such; only an unwritten type is missing.
func (s *AccountDataService) Get(ctx context.Context, userID, dataType string) (json.RawMessage, error) {
	if err := validAccountDataType(dataType); err != nil {
		return nil, err
	}
	row, err := s.accountData.Get(ctx, userID, dataType)
	if errors.Is(err, persist.ErrNotFound) {
		return nil, matrixerr.Newf(matrixerr.CodeNotFound,
			"No account data of type %q has been stored for this user.", dataType)
	}
	if err != nil {
		return nil, fmt.Errorf("users: cannot read account data: %w", err)
	}
	return json.RawMessage(row.Content), nil
}

// Put stores one type of a user's account data. Writing the same type again replaces it:
// this is a current value, not a history, and sync reports the newest one.
func (s *AccountDataService) Put(ctx context.Context, userID, dataType string, content json.RawMessage) error {
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
	if _, err := s.accountData.Put(ctx, userID, dataType, string(content)); err != nil {
		return fmt.Errorf("users: cannot store account data: %w", err)
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
