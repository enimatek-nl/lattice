package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"gorm.io/gorm"
)

// EventDraft describes an event that should be appended to a room.
type EventDraft struct {
	RoomID   string
	Type     string
	StateKey *string
	Sender   string
	Content  matrix.RawMessage
	// TxnID makes the append idempotent for a client transaction. It is only
	// consulted for non-state events sent through SendRoomEvent.
	TxnID string
	// Kind distinguishes the transaction namespaces of the txn log.
	Kind string
}

// StateKeyOf returns a pointer to a state key, ready for use in an
// [EventDraft].
func StateKeyOf(key string) *string { return &key }

// AppendResult carries the outcome of appending an event.
type AppendResult struct {
	Event     *persist.Event
	PrevBatch uint64
}

// AppendEvent persists an event, updates the current state of the room when the
// event is a state event, keeps the membership table in step with
// m.room.member events and wakes long polling sync calls.
//
// All writes happen in one transaction so a room never observes an event
// without its state change.
func (s *Service) AppendEvent(ctx context.Context, draft *EventDraft) (*persist.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if draft.Type == "" {
		return nil, matrix.ErrInvalidRequest("event type is required")
	}
	if draft.Sender == "" {
		return nil, matrix.ErrForbidden("sender is required")
	}
	content := draft.Content
	if len(content) == 0 {
		content = matrix.RawMessage("{}")
	}

	pos := s.nextStreamPos()
	var created *persist.Event

	// The room version decides the shape of every event in the room, and it is needed
	// after the transaction too: a peer receives the event in the form its room version
	// defines, not the form this server stores for clients.
	version := ""
	err := s.db.Tx(func(tx *gorm.DB) error {
		rooms := persist.NewRoomRepo(tx)
		room, err := rooms.Get(draft.RoomID)
		if err != nil {
			if persist.IsNotFound(err) {
				return matrix.ErrNotFound("no such room %s", draft.RoomID)
			}
			return matrix.ErrInternal(err)
		}

		// The event is first built and signed in the form the room version defines,
		// because its id depends on that form: in room version 3 and later the id is a
		// hash over the event including the events it cites and the hash that covers
		// its content. Doing this inside the transaction is what keeps the cited events
		// the ones that were current when the event was made.
		originServerTS := s.NowMS()
		version = RoomVersionOf(room)
		packet, err := s.BuildPDU(ctx, tx, draft, version, originServerTS)
		if err != nil {
			return err
		}
		if err := s.SignAndIdentify(ctx, packet); err != nil {
			return err
		}

		events := persist.NewEventRepo(tx)
		event := &persist.Event{
			EventID:        packet.eventID,
			RoomID:         draft.RoomID,
			StreamPos:      pos,
			Type:           draft.Type,
			Sender:         draft.Sender,
			OriginServerTS: originServerTS,
			Content:        string(content),
			Depth:          depthOf(packet),
			JSON:           packet.encoded,
			PrevEvents:     encodedIDs(packet.prevIDs),
			AuthEvents:     encodedIDs(packet.authIDs),
		}
		if draft.StateKey != nil {
			event.StateKey = *draft.StateKey
			event.IsState = true
		}
		event.RelatesTo, event.RelType = relationOf(content)
		if err := events.Create(event); err != nil {
			return fmt.Errorf("insert event: %w", err)
		}

		if draft.StateKey != nil {
			state := persist.NewStateRepo(tx)
			if err := state.Upsert(&persist.RoomState{
				RoomID:    draft.RoomID,
				Type:      draft.Type,
				StateKey:  *draft.StateKey,
				EventID:   event.EventID,
				StreamPos: pos,
				Sender:    draft.Sender,
				Content:   event.Content,
			}); err != nil {
				return fmt.Errorf("update room state: %w", err)
			}
			if err := s.applySideEffects(tx, draft, event, pos); err != nil {
				return err
			}
		}
		// Redaction is the one message event with a side effect: it strips the
		// event it points at, whichever way the redaction was appended.
		if draft.Type == matrix.TypeRedaction {
			if err := applyRedaction(tx, event); err != nil {
				return err
			}
		}
		// The new event takes the place of the leaves it cited: a graph whose leaves
		// are never retired grows a fork for every concurrent send.
		if err := events.AdvanceForwardExtremities(draft.RoomID, packet.prevIDs, &persist.ForwardExtremity{
			EventID:        event.EventID,
			Depth:          event.Depth,
			OriginServerTS: event.OriginServerTS,
		}); err != nil {
			return fmt.Errorf("advance the graph leaves: %w", err)
		}

		if draft.TxnID != "" {
			txn := persist.NewTxnRepo(tx)
			if err := txn.Set(&persist.Txn{
				UserID:    draft.Sender,
				Kind:      txnKind(draft.Kind),
				RoomID:    draft.RoomID,
				TxnID:     draft.TxnID,
				EventID:   event.EventID,
				StreamPos: pos,
				SentTS:    event.OriginServerTS,
			}); err != nil {
				return fmt.Errorf("record transaction: %w", err)
			}
		}
		created = event
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.Notify()
	// The event goes to the peers that share the room through the outbox, not from this
	// call: a peer that is unreachable must not make a local message fail, and the
	// specification's transaction rules make the unit of delivery a queued batch rather
	// than an event.
	s.queueEventForPeers(ctx, created, version)
	if err := s.notifyParticipants(ctx, draft.RoomID, created); err != nil {
		log.Printf("push rule evaluation for %s failed: %v", created.EventID, err)
	}
	return created, nil
}

// applySideEffects keeps derived tables in sync with special state event
// types inside the enclosing transaction.
func (s *Service) applySideEffects(tx *gorm.DB, draft *EventDraft, event *persist.Event, pos uint64) error {
	switch draft.Type {
	case matrix.TypeMember:
		content, err := matrix.DecodeMemberContent(matrix.RawMessage(event.Content))
		if err != nil {
			return fmt.Errorf("decode member content: %w", err)
		}
		members := persist.NewMembershipRepo(tx)
		if err := members.Set(&persist.RoomMembership{
			RoomID:     draft.RoomID,
			UserID:     *draft.StateKey,
			Membership: content.Membership,
			EventID:    event.EventID,
			SentTS:     event.OriginServerTS,
		}); err != nil {
			return fmt.Errorf("update membership: %w", err)
		}
	case matrix.TypeJoinRules:
		var content matrix.JoinRulesContent
		if err := matrix.DecodeContent(matrix.RawMessage(event.Content), &content); err != nil {
			return fmt.Errorf("decode join rules: %w", err)
		}
		rooms := persist.NewRoomRepo(tx)
		room, err := rooms.Get(draft.RoomID)
		if err != nil {
			return err
		}
		room.JoinRule = content.JoinRule
		if err := rooms.Save(room); err != nil {
			return fmt.Errorf("update join rule: %w", err)
		}
	case matrix.TypeCreate:
		// The creator's membership is recorded when the room is created; no
		// extra work here.
	}
	return nil
}

// applyRedaction strips the event an m.room.redaction points at, inside the
// transaction that appended the redaction. Clients may send redaction events
// through PUT /send, so the effect cannot live in the /redact handler.
func applyRedaction(tx *gorm.DB, redaction *persist.Event) error {
	var content matrix.RedactionContent
	if err := matrix.DecodeContent(matrix.RawMessage(redaction.Content), &content); err != nil {
		return fmt.Errorf("decode redaction content: %w", err)
	}
	if content.Redacts == "" {
		return nil
	}
	events := persist.NewEventRepo(tx)
	target, err := events.GetInRoom(redaction.RoomID, content.Redacts)
	if err != nil {
		if persist.IsNotFound(err) {
			return matrix.ErrNotFound("no event %s in room %s", content.Redacts, redaction.RoomID)
		}
		return err
	}
	if target.Type == matrix.TypeCreate || target.Type == matrix.TypeMember ||
		target.Type == matrix.TypeRedaction {
		return matrix.ErrForbidden("cannot redact %s events", target.Type)
	}
	if target.Redacted {
		return nil
	}
	evidence := redaction.Matrix()
	evidence.RoomID = ""
	evidence.Unsigned = nil
	if _, err := events.MarkRedacted(target.EventID, content.Reason, string(matrix.Encode(evidence))); err != nil {
		return fmt.Errorf("redact %s: %w", target.EventID, err)
	}
	return nil
}

// txnKind defaults an empty transaction kind.
func txnKind(kind string) string {
	if kind == "" {
		return "room_event"
	}
	return kind
}

// replayTxn returns the event a client transaction id already produced. ok
// is false when the id is unknown; a stored transaction whose event has gone
// missing counts as unknown too, so the request is processed normally. Only a
// real repository failure returns an error.
func (s *Service) replayTxn(sender, kind, roomID, txnID string) (*persist.Event, bool, error) {
	if txnID == "" {
		return nil, false, nil
	}
	existing, err := s.db.TxnRepo.Get(sender, txnKind(kind), roomID, txnID)
	if persist.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, matrix.ErrInternal(err)
	}
	stored, err := s.db.EventRepo.Get(existing.EventID)
	if err != nil {
		return nil, false, nil
	}
	return stored, true, nil
}

// relationOf extracts the (event id, relation type) pair of an m.relates_to
// content block. Thread and annotation relations are stored directly; legacy
// in-reply-to relations are normalised onto the same columns.
func relationOf(content matrix.RawMessage) (eventID, relType string) {
	var parsed struct {
		RelatesTo *struct {
			EventID   string `json:"event_id"`
			RelType   string `json:"rel_type"`
			InReplyTo *struct {
				EventID string `json:"event_id"`
			} `json:"m.in_reply_to"`
		} `json:"m.relates_to"`
	}
	if err := json.Unmarshal(content, &parsed); err != nil || parsed.RelatesTo == nil {
		return "", ""
	}
	if parsed.RelatesTo.RelType != "" {
		return parsed.RelatesTo.EventID, parsed.RelatesTo.RelType
	}
	if parsed.RelatesTo.InReplyTo != nil {
		return parsed.RelatesTo.InReplyTo.EventID, "m.in_reply_to"
	}
	return "", ""
}

// SendRoomEvent appends a message-like event, honouring the client
// transaction id so retries return the original event.
func (s *Service) SendRoomEvent(ctx context.Context, roomID, sender, typ string, content matrix.RawMessage, txnID string) (*persist.Event, error) {
	if event, replayed, err := s.replayTxn(sender, "room_event", roomID, txnID); err != nil {
		return nil, err
	} else if replayed {
		return event, nil
	}
	if err := s.RequireSend(ctx, roomID, sender); err != nil {
		return nil, err
	}
	return s.AppendEvent(ctx, &EventDraft{
		RoomID:  roomID,
		Type:    typ,
		Sender:  sender,
		Content: content,
		TxnID:   txnID,
		Kind:    "room_event",
	})
}

// SetRoomState appends a state event, honouring the optional transaction id
// used for idempotent PUT /state requests.
func (s *Service) SetRoomState(ctx context.Context, roomID, sender, typ, stateKey string, content matrix.RawMessage, txnID string) (*persist.Event, error) {
	if event, replayed, err := s.replayTxn(sender, "state", roomID, txnID); err != nil {
		return nil, err
	} else if replayed {
		return event, nil
	}
	if err := s.RequireSend(ctx, roomID, sender); err != nil {
		return nil, err
	}
	if err := s.RequireState(ctx, roomID, sender, typ); err != nil {
		return nil, err
	}
	if err := s.validateStateEvent(ctx, roomID, sender, typ, stateKey, content); err != nil {
		return nil, err
	}
	// Writing the state the room already has is not a second statement about the room,
	// and answering it with a new event would let a client that saved twice - or retried
	// after losing the answer - push the room's history forward with copies of one
	// decision. Membership is left out on purpose: re-joining has to be a new event even
	// when its content matches, because what changed is the graph, not the content.
	if existing, same := s.unchangedState(roomID, typ, stateKey, sender, content); same {
		return existing, nil
	}
	return s.AppendEvent(ctx, &EventDraft{
		RoomID:   roomID,
		Type:     typ,
		StateKey: StateKeyOf(stateKey),
		Sender:   sender,
		Content:  content,
		TxnID:    txnID,
		Kind:     "state",
	})
}

// unchangedState reports whether the state a client is about to write is the state the
// room already holds: same type, same state key, same sender, and content that is equal
// once canonicalised, so a reordered object or a different number spelling is still the
// same statement. The event it names is returned so the caller can answer with it.
func (s *Service) unchangedState(roomID, typ, stateKey, sender string, content matrix.RawMessage) (*persist.Event, bool) {
	if typ == matrix.TypeMember || typ == matrix.TypeCreate {
		return nil, false
	}
	row, err := s.db.StateRepo.Get(roomID, typ, stateKey)
	if err != nil || row.Sender != sender {
		return nil, false
	}
	var wanted, held any
	if err := json.Unmarshal(content, &wanted); err != nil {
		return nil, false
	}
	if err := json.Unmarshal([]byte(row.Content), &held); err != nil {
		return nil, false
	}
	wantedJSON, err := matrix.CanonicalJSON(wanted)
	if err != nil {
		return nil, false
	}
	heldJSON, err := matrix.CanonicalJSON(held)
	if err != nil || !bytes.Equal(wantedJSON, heldJSON) {
		return nil, false
	}
	existing, err := s.db.EventRepo.GetInRoom(roomID, row.EventID)
	if err != nil {
		return nil, false
	}
	return existing, true
}

// RedactEvent appends an m.room.redaction event and strips the target event.
func (s *Service) RedactEvent(ctx context.Context, roomID, sender, targetEventID, reason, txnID string) (*persist.Event, error) {
	if event, replayed, err := s.replayTxn(sender, "redaction", roomID, txnID); err != nil {
		return nil, err
	} else if replayed {
		return event, nil
	}
	if err := s.RequireSend(ctx, roomID, sender); err != nil {
		return nil, err
	}
	target, err := s.db.EventRepo.GetInRoom(roomID, targetEventID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no such event %s", targetEventID)
		}
		return nil, matrix.ErrInternal(err)
	}
	if target.Type == matrix.TypeCreate || target.Type == matrix.TypeMember ||
		target.Type == matrix.TypeRedaction {
		return nil, matrix.ErrForbidden("cannot redact %s events", target.Type)
	}
	if target.Redacted {
		// The event carries no content left to strip, so the request has no
		// target any more.
		return nil, matrix.ErrNotFound("event %s is already redacted", targetEventID)
	}
	if target.Sender != sender && !s.Allow(ctx, roomID, sender, "redact", 50) {
		return nil, matrix.ErrForbidden("insufficient power level to redact this event")
	}

	content := matrix.Encode(matrix.RedactionContent{Reason: reason, Redacts: targetEventID})
	event, err := s.AppendEvent(ctx, &EventDraft{
		RoomID:  roomID,
		Type:    matrix.TypeRedaction,
		Sender:  sender,
		Content: content,
		TxnID:   txnID,
		Kind:    "redaction",
	})
	if err != nil {
		return nil, err
	}
	// The redaction itself did the stripping: AppendEvent applies the effect of
	// an m.room.redaction inside the transaction that stored it.
	return event, nil
}
