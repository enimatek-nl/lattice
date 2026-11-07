package rooms

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// A read receipt is a statement by one user about how far they have read. It is not an event:
// it has no ID, no authorization rules and no place in the room's graph. What it does have is a
// position in the same stream events take, because a client's question — "what has happened
// since I last looked?" — is asked about receipts too.

// The two receipt kinds this server understands by name. A custom kind is allowed as well (the
// specification permits a namespaced one), and it is stored and delivered unchanged.
const (
	receiptTypeRead         = "m.read"
	receiptTypeReadPrivate  = "m.read.private"
	receiptThreadMain       = "m.main"
	defaultReceiptScanLimit = 1000
)

// validReceiptType accepts the two defined kinds and any custom one that is shaped like a
// Matrix identifier — a dotted name. The empty string and a bare word are refused: a receipt
// type is how a client names which of its read positions it means, and a typo there would put
// the receipt where nothing will look for it.
func validReceiptType(receiptType string) error {
	switch receiptType {
	case receiptTypeRead, receiptTypeReadPrivate:
		return nil
	}
	if !strings.Contains(receiptType, ".") {
		return matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a receipt type: use %q, %q, or a custom name with a dot in it.",
			receiptType, receiptTypeRead, receiptTypeReadPrivate)
	}
	return nil
}

// PutReceipt records that a user has read up to an event of this room.
//
// The event is checked to exist in this room because a receipt is read back to the user as
// "you have seen this"; pointing it at an event the room never held would be a fact this server
// cannot stand behind. What is *not* checked is whether the requester may read that event: a
// client moves its read marker to the newest thing it can see, and refusing the move over the
// content of one event would strand the marker behind something it cannot pass.
func (s *Service) PutReceipt(ctx context.Context, roomID, receiptType, eventID, userID, threadID string) error {
	if err := s.requireHistoryAccess(ctx, roomID, userID); err != nil {
		return err
	}
	if err := validReceiptType(receiptType); err != nil {
		return err
	}
	if err := matrixid.EventIDValid(eventID); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not an event ID.", eventID)
	}
	event, err := s.events.ByID(ctx, eventID)
	if err != nil {
		if notFound(err) {
			return matrixerr.Newf(matrixerr.CodeNotFound, "Event %q is not known to this server.", eventID)
		}
		return err
	}
	if event.RoomID != roomID {
		return matrixerr.Newf(matrixerr.CodeNotFound,
			"Event %q is not an event of this room.", eventID)
	}
	threadScoped := threadID != ""
	if threadID == receiptThreadMain {
		// The main line of a room is a thread of sorts, and MSC2028's clients name it; the
		// distinction between "m.main" and no thread at all is kept.
		threadScoped = true
	}
	return s.db.Tx(ctx, func(tx *gorm.DB) error {
		service := s.WithTx(tx)
		position, err := service.events.NextStreamOrdering(ctx)
		if err != nil {
			return err
		}
		_, err = service.receipts.Put(ctx, &persist.Receipt{
			RoomID:         roomID,
			ReceiptType:    receiptType,
			UserID:         userID,
			EventID:        eventID,
			ThreadID:       threadID,
			ThreadScoped:   threadScoped,
			TS:             time.Now().UnixMilli(),
			StreamOrdering: position,
		})
		return err
	})
}

// ReceiptFor returns the user's own read position in a room, which is what an unread count is
// measured from. Absent means the user has never moved it.
func (s *Service) ReceiptFor(ctx context.Context, roomID, userID string) (*persist.Receipt, error) {
	rows, err := s.receipts.ListForRoom(ctx, roomID, 0, 0, defaultReceiptScanLimit)
	if err != nil {
		return nil, err
	}
	var newest *persist.Receipt
	for _, row := range rows {
		if row.UserID != userID || row.ReceiptType != receiptTypeRead {
			continue
		}
		if newest == nil || row.StreamOrdering > newest.StreamOrdering {
			newest = row
		}
	}
	return newest, nil
}
