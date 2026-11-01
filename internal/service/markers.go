package service

import (
	"context"
	"strconv"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Known receipt types.
const (
	ReceiptRead        = "m.read"
	ReceiptReadPrivate = "m.read.private"
	ReceiptFullyRead   = "m.fully_read"
)

// PostReceipt records a read receipt for an event.
func (s *Service) PostReceipt(ctx context.Context, roomID, userID, receiptType, eventID string, data matrix.Object) error {
	switch receiptType {
	case ReceiptRead, ReceiptReadPrivate, ReceiptFullyRead:
	default:
		return matrix.ErrInvalidParam("unsupported receipt type %q", receiptType)
	}
	event, err := s.db.EventRepo.GetInRoom(roomID, eventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
		}
		return matrix.ErrInternal(err)
	}
	if err := s.RequireJoined(ctx, roomID, userID); err != nil {
		return err
	}
	ts := s.NowMS()
	fresh := true
	if existing, err := s.db.ReceiptRepo.LatestForUser(userID, roomID, receiptType); err == nil &&
		existing.StreamPos > event.StreamPos {
		// A receipt never moves backwards: the newer position and its timestamp
		// are kept as they are.
		ts, fresh = existing.SentTS, false
	}
	pos := s.nextStreamPos()
	if fresh {
		if err := s.db.ReceiptRepo.Set(&persist.Receipt{
			RoomID:      roomID,
			UserID:      userID,
			ReceiptType: receiptType,
			EventID:     eventID,
			StreamPos:   pos,
			SentTS:      ts,
			Data:        string(matrix.Encode(data)),
		}); err != nil {
			return matrix.ErrInternal(err)
		}
	}
	if receiptType == ReceiptFullyRead {
		if err := s.SetReadMarker(ctx, roomID, userID, eventID, ""); err != nil {
			return err
		}
	}
	s.Notify()
	return nil
}

// SetReadMarker writes the fully read marker and optionally the read receipt of
// a user.
func (s *Service) SetReadMarker(ctx context.Context, roomID, userID, fullyRead, readReceipt string) error {
	if fullyRead == "" && readReceipt == "" {
		return matrix.ErrInvalidParam("at least one of fully_read or read_receipt must be set")
	}
	for _, eventID := range []string{fullyRead, readReceipt} {
		if eventID == "" {
			continue
		}
		if _, err := s.db.EventRepo.GetInRoom(roomID, eventID); err != nil {
			if persist.IsNotFound(err) {
				return matrix.ErrNotFound("no event %s in room %s", eventID, roomID)
			}
			return matrix.ErrInternal(err)
		}
	}
	marker, err := s.db.ReadMarkerRepo.Get(userID, roomID)
	if err != nil && !persist.IsNotFound(err) {
		return matrix.ErrInternal(err)
	}
	// A missing marker yields a zero row, so the key has to be filled in.
	marker.UserID = userID
	marker.RoomID = roomID
	if fullyRead != "" {
		marker.FullyReadEventID = fullyRead
	}
	if readReceipt != "" {
		marker.ReadReceiptEventID = readReceipt
		if err := s.PostReceipt(ctx, roomID, userID, ReceiptRead, readReceipt, nil); err != nil {
			return err
		}
	}
	if err := s.db.ReadMarkerRepo.Set(marker); err != nil {
		return matrix.ErrInternal(err)
	}
	if fullyRead != "" {
		// The fully read marker doubles as m.fully_read room account data, which
		// is how clients learn about it through sync.
		content := matrix.Encode(matrix.Object{"event_id": fullyRead})
		if err := s.SetAccountData(ctx, userID, userID, roomID, matrix.TypeFullyRead, content); err != nil {
			return err
		}
	}
	s.Notify()
	return nil
}

// RoomReceipts lists the read receipts of a room, which is what
// GET /v1/rooms/{roomId}/receipts reports. The payload is keyed by room, event
// and receipt type, and m.read.private receipts stay hidden from everybody but
// the user that posted them.
func (s *Service) RoomReceipts(ctx context.Context, idOrAlias, userID string) (matrix.Object, error) {
	room, err := s.MustRoom(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}
	if err := s.RequireReadable(ctx, room.ID, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.ReceiptRepo.ForRoom(room.ID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	byEvent := matrix.Object{}
	for _, row := range rows {
		switch row.ReceiptType {
		case ReceiptRead, ReceiptReadPrivate:
		default:
			// A fully read marker is not a receipt and does not belong here.
			continue
		}
		if row.ReceiptType == ReceiptReadPrivate && row.UserID != userID {
			continue
		}
		data := matrix.MustParseObject([]byte(row.Data))
		if _, ok := data["ts"]; !ok {
			data["ts"] = row.SentTS
		}
		perEvent, ok := byEvent[row.EventID].(matrix.Object)
		if !ok {
			perEvent = matrix.Object{}
			byEvent[row.EventID] = perEvent
		}
		perType, ok := perEvent[row.ReceiptType].(matrix.Object)
		if !ok {
			perType = matrix.Object{}
			perEvent[row.ReceiptType] = perType
		}
		perType[row.UserID] = matrix.Object{"data": data}
	}
	return matrix.Object{room.ID: byEvent}, nil
}

// notificationActions renders the actions of a stored notification. The action
// list of the rule that fired is reused verbatim; older rows that only kept the
// count tweak fall back to a plain notify.
func notificationActions(row *persist.Notification) matrix.Array {
	if row.Actions != "" {
		if decoded := toAnySlice(row.Actions); decoded != nil {
			return decoded
		}
	}
	if row.CountTweak != "" {
		return matrix.Array{"notify", matrix.Object{"set_tweak": "count"}}
	}
	return matrix.Array{"notify"}
}

// SetTyping records the typing state of a user in a room for a limited time.
func (s *Service) SetTyping(ctx context.Context, roomID, userID string, typing bool, timeout time.Duration) error {
	if err := s.RequireJoined(ctx, roomID, userID); err != nil {
		return err
	}
	now := s.NowMS()
	expires := int64(0)
	if typing {
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		expires = now + timeout.Milliseconds()
	}
	if err := s.db.TypingRepo.Set(&persist.Typing{
		RoomID:    roomID,
		UserID:    userID,
		Typing:    typing,
		SentTS:    now,
		ExpiresTS: expires,
		StreamPos: s.nextStreamPos(),
	}); err != nil {
		return matrix.ErrInternal(err)
	}
	s.Notify()
	return nil
}

// SetPresence records the presence state of a user.
func (s *Service) SetPresence(ctx context.Context, userID, presence, statusMsg string, currentlyActive *bool) error {
	switch presence {
	case "online", "offline", "unavailable":
	default:
		return matrix.ErrInvalidParam("presence must be one of online, offline or unavailable")
	}
	active := presence == "online"
	if currentlyActive != nil {
		active = *currentlyActive
	}
	record := &persist.Presence{
		UserID:          userID,
		Presence:        presence,
		StatusMsg:       statusMsg,
		LastActiveAgo:   0,
		CurrentlyActive: active,
		SentTS:          s.NowMS(),
	}
	if err := s.db.PresenceRepo.Set(record); err != nil {
		return matrix.ErrInternal(err)
	}
	s.Notify()
	return nil
}

// Presence returns the presence state of a user.
func (s *Service) Presence(ctx context.Context, targetUserID, requester string) (matrix.Object, error) {
	if !s.LocalUser(targetUserID) {
		return nil, matrix.ErrNotFound("user %s not found", targetUserID)
	}
	record, err := s.db.PresenceRepo.Get(targetUserID)
	if err != nil {
		if persist.IsNotFound(err) {
			return matrix.Object{
				"presence":         "offline",
				"last_active_ago":  0,
				"currently_active": false,
			}, nil
		}
		return nil, matrix.ErrInternal(err)
	}
	out := matrix.Object{
		"presence":         record.Presence,
		"last_active_ago":  s.NowMS() - record.SentTS,
		"currently_active": record.CurrentlyActive,
	}
	if record.StatusMsg != "" {
		out["status_msg"] = record.StatusMsg
	}
	return out, nil
}

// NotificationEntry is one row of the /notifications list.
type NotificationEntry struct {
	RoomID  string       `json:"room_id"`
	Event   matrix.Event `json:"event"`
	TS      int64        `json:"ts"`
	Actions matrix.Array `json:"actions,omitempty"`
	// Read reports whether the requester has caught up with the notification:
	// their read receipt or fully read marker in that room has reached it. The
	// specification makes this field mandatory, so it is always serialised.
	Read       bool   `json:"read"`
	ThumbImage string `json:"thumb_image,omitempty"`
}

// Notifications lists the notifications of a user, oldest first, paginated by
// row id. Lattice keeps the store in arrival order and pages forward from a
// token, which is what the "pagination token to continue from" in the
// specification describes; the ordering is pinned by
// TestNotificationsOrder in the compatibility suite.
func (s *Service) Notifications(ctx context.Context, userID, from, only string, limit int) ([]NotificationEntry, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if only != "" && only != persist.NotificationOnlyHighlight {
		return nil, "", matrix.ErrInvalidParam("only must be %q, got %q",
			persist.NotificationOnlyHighlight, only)
	}
	fromPos := int64(0)
	if from != "" {
		parsed, err := strconv.ParseInt(from, 10, 64)
		if err != nil {
			return nil, "", matrix.ErrInvalidParam("invalid from token %q", from)
		}
		fromPos = parsed
	}
	rows, next, err := s.db.NotificationRepo.List(persist.NotificationQuery{
		UserID: userID,
		From:   fromPos,
		Limit:  limit,
		Only:   only,
	})
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}
	// One lookup for every event of the page, and one per room for how far the
	// requester has read: a page of notifications must not turn into a query per
	// row.
	ids := make([]string, 0, len(rows))
	roomIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.EventID)
		roomIDs = append(roomIDs, row.RoomID)
	}
	events, err := s.db.EventRepo.GetByIDs(ids)
	if err != nil {
		return nil, "", matrix.ErrInternal(err)
	}
	readUpTo := s.readPositions(ctx, userID, roomIDs)

	out := make([]NotificationEntry, 0, len(rows))
	for _, row := range rows {
		event, ok := events[row.EventID]
		if !ok {
			continue
		}
		out = append(out, NotificationEntry{
			RoomID:  row.RoomID,
			Event:   event.Matrix(),
			TS:      row.TS,
			Actions: notificationActions(row),
			Read:    row.StreamPos <= readUpTo[row.RoomID],
		})
	}
	token := ""
	if next > 0 && len(rows) == limit {
		token = strconv.FormatInt(next, 10)
	}
	return out, token, nil
}

// readPositions returns, per room, the stream position the requester has read
// up to. Three things record that: an m.read receipt, an m.read.private receipt
// and the fully read marker, each of which points at an event, so the answer is
// the furthest of those events. Rooms the requester never read are absent, which
// reads as position zero.
func (s *Service) readPositions(ctx context.Context, userID string, roomIDs []string) map[string]uint64 {
	positions := map[string]uint64{}
	anchors := map[string][]string{}
	seen := map[string]bool{}
	for _, roomID := range roomIDs {
		if roomID == "" || seen[roomID] {
			continue
		}
		seen[roomID] = true
		for _, receiptType := range []string{ReceiptRead, ReceiptReadPrivate} {
			if receipt, err := s.db.ReceiptRepo.LatestForUser(userID, roomID, receiptType); err == nil &&
				receipt.EventID != "" {
				anchors[roomID] = append(anchors[roomID], receipt.EventID)
			}
		}
		if marker, err := s.db.ReadMarkerRepo.Get(userID, roomID); err == nil && marker.FullyReadEventID != "" {
			anchors[roomID] = append(anchors[roomID], marker.FullyReadEventID)
		}
	}
	ids := make([]string, 0, len(anchors))
	for _, list := range anchors {
		ids = append(ids, list...)
	}
	if len(ids) == 0 {
		return positions
	}
	// The stored stream position of a receipt row is the position of the receipt
	// update, not of the event it points at, so the anchor events are looked up
	// here - in one query for the whole page.
	events, err := s.db.EventRepo.GetByIDs(ids)
	if err != nil {
		return positions
	}
	for roomID, list := range anchors {
		for _, eventID := range list {
			if event, ok := events[eventID]; ok && event.StreamPos > positions[roomID] {
				positions[roomID] = event.StreamPos
			}
		}
	}
	return positions
}
