package service

import (
	"context"
	"log"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// Being part of a room is not the same as being allowed to read all of it, and a server
// that hands over history to whoever asks undoes the room's decision for every server
// that ever asks. The room says how far a reader sees through m.room.history_visibility,
// and the answer a client gets is the answer a peer gets from GET /event,
// get_missing_events and backfill: the event arrives, with its signature and its hashes
// intact, and its content gone. Signatures survive a redaction on purpose - they are made
// over the unredacted form precisely so that a reader can tell that the fields it is
// missing were removed by the room rather than tampered with in transit.
//
// Three reads are deliberately *not* redacted: state_ids, state and event_auth. A peer
// cannot judge an event without the rules and the memberships it cites as authority, and
// a membership whose content is gone cannot answer "was this user in the room when this
// event was sent" - which is the very question the redaction rests on. Redacting those
// reads protects nothing; it produces refusals of honest events, and a server that gets
// them cannot tell the difference.

const (
	// maxServedBackfillEvents caps how much history one backfill request may ask for.
	// The specification's own default and maximum are both 100, so a larger `limit` is
	// treated as 100 rather than honoured.
	maxServedBackfillEvents = 100
	// maxBackfillWalkRounds bounds how far back one backfill walk goes. It is a graph
	// walk over prev_events, and a room with a long history or a cycle in it must not be
	// able to make a request spin.
	maxBackfillWalkRounds = 100
)

// servedEvent renders one stored event for a peer: the PDU its room version defines, with
// the content gone when history visibility says the peer may not read it.
//
// The visibility is read from the event's own auth events rather than from the room as it
// stands, because history visibility is a statement about the moment an event was sent.
// A room that moved from `shared` to `joined` does not retroactively hide the history it
// shared, and a room that moved the other way does not expose what it had already closed.
func (s *Service) servedEvent(ctx context.Context, event *persist.Event, version, peer string) (matrix.Object, error) {
	pdu, err := transportPDU(event.JSON, version)
	if err != nil {
		log.Printf("federation: the event %s cannot be prepared for %s: %v", event.EventID, peer, err)
		return nil, err
	}
	if s.maySeeContent(ctx, event.RoomID, peer, storedIDsOf(event.AuthEvents)) {
		return pdu, nil
	}
	return eventcrypto.Redact(pdu, version), nil
}

// maySeeContent reports whether a peer may read the content of an event. The membership
// half of the decision is read from the auth events the event cited, because that is the
// membership that was in force when it was sent; the visibility half is read from the room
// as it stands, because this server keeps no state snapshot per event and a room that has
// never changed its visibility - which is almost every room - has the one answer either
// way. What is *not* done is guessing a stricter answer when the question is unclear:
// hiding history the room never closed is the mistake a reader cannot notice.
func (s *Service) maySeeContent(ctx context.Context, roomID, peer string, authIDs []string) bool {
	joinedHere, invitedHere := false, false
	rows, err := s.db.EventRepo.ListByIDs(authIDs)
	if err != nil {
		log.Printf("federation: reading the state behind an event of %s failed: %v", roomID, err)
	}
	for _, row := range rows {
		switch row.Type {
		case matrix.TypeMember:
			// "Is the asking server in the room" is answered by its users: a server is
			// part of a room because some user of it is, and an invitation to one is the
			// room telling that server it may read what an invited reader may read.
			if matrix.DomainOf(row.StateKey) != peer {
				continue
			}
			member := matrix.MemberContent{}
			if err := matrix.DecodeContent(matrix.RawMessage(row.Content), &member); err != nil {
				continue
			}
			switch member.Membership {
			case matrix.MembershipJoin:
				joinedHere = true
			case matrix.MembershipInvite:
				invitedHere = true
			}
		}
	}
	// A room that never named a visibility, and a room that named one this server does
	// not know, both mean `shared`: that is the default the specification gives, and
	// guessing a stricter answer would hide history the room never decided to hide.
	visibility := s.HistoryVisibility(ctx, roomID)
	switch visibility {
	case matrix.HistoryWorldReadable:
		// 1. world_readable says everybody may read it, member or not.
		return true
	case matrix.HistoryJoined:
		// 2. A user of the asking server joined at that moment: read it.
		return joinedHere
	case matrix.HistoryInvited:
		// 3. Invited is the point at which the room opened its history to that server;
		// a server already joined has passed that point.
		return joinedHere || invitedHere
	default:
		// 4. `shared`, and anything unrecognised: a server that is in the room now reads
		// the room's whole history, which is what "shared" has always meant between
		// servers, and a server that joined after the event was sent reads it too.
		if joinedHere {
			return true
		}
		in, err := s.roomSharesServer(ctx, roomID, peer)
		if err != nil {
			// A question this server cannot answer is answered on the careful side, and
			// said out loud: redacting a peer's read is visible to that peer, while
			// publishing history the room closed is not.
			log.Printf("federation: whether %s may read an event of %s is unknown: %v", peer, roomID, err)
			return false
		}
		return in
	}
}

// ServeBackfill answers GET /_matrix/federation/v1/backfill/{roomId}: older history,
// oldest first, for a server that is part of the room. `since` names an event to walk
// back from; without it the walk starts at this room's leaves, which is what a server
// asking for some history of a room it just joined is asking for.
//
// Backfill is how a room that was apart agrees on what happened while it was apart, and
// it is bounded on purpose: `limit` is the asking server's bound, and the walk's own
// bound is this server's.
func (s *Service) ServeBackfill(ctx context.Context, roomID, peer string, limit int, since string) (matrix.Object, error) {
	room, err := s.roomForPeer(ctx, roomID, peer)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxServedBackfillEvents {
		limit = maxServedBackfillEvents
	}
	version := RoomVersionOf(room)

	// The walk starts at the event the peer named, or at this room's leaves. Either way
	// the starting events are not part of the answer: the peer has them, which is why it
	// could name them.
	frontier := make([]string, 0, 2)
	if since != "" {
		event, err := s.db.EventRepo.Get(since)
		if err != nil {
			if persist.IsNotFound(err) {
				return nil, matrix.ErrNotFound("this server holds no event %s", since)
			}
			return nil, matrix.ErrInternal(err)
		}
		if event.RoomID != roomID {
			return nil, matrix.ErrNotFound("the event %s is not in room %s", since, roomID)
		}
		frontier = append(frontier, since)
	} else {
		leaves, err := s.db.EventRepo.ForwardExtremities(roomID, 20)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		frontier = append(frontier, leaves...)
	}

	seen := map[string]bool{}
	for _, id := range frontier {
		seen[id] = true
	}
	found := map[string]*persist.Event{}
	for round := 0; round < maxBackfillWalkRounds && len(frontier) > 0 && len(found) < limit; round++ {
		next := make([]string, 0, len(frontier))
		for _, id := range frontier {
			event, err := s.db.EventRepo.Get(id)
			if err != nil {
				continue
			}
			for _, prev := range storedIDsOf(event.PrevEvents) {
				if seen[prev] {
					continue
				}
				seen[prev] = true
				older, err := s.db.EventRepo.Get(prev)
				if err != nil || older.RoomID != roomID {
					continue
				}
				found[prev] = older
				next = append(next, prev)
			}
			if len(found) >= limit {
				break
			}
		}
		frontier = next
	}

	pdus := matrix.Array{}
	for _, event := range byDepth(found) {
		if len(pdus) >= limit {
			break
		}
		pdu, err := s.servedEvent(ctx, event, version, peer)
		if err != nil {
			continue
		}
		pdus = append(pdus, pdu)
	}
	return matrix.Object{
		"origin":           s.ServerName(),
		"origin_server_ts": s.NowMS(),
		"pdus":             pdus,
	}, nil
}

// ServeTimestampToEvent answers POST /_matrix/federation/v1/timestamp_to_event/{roomId}
// (MSC3030): the id of the event of this room nearest a moment, searched forwards from it
// or backwards. Only the id is named, so there is no content here to redact - what the
// peer does with the id it asks about next, and that read is subject to the room's rules.
func (s *Service) ServeTimestampToEvent(ctx context.Context, roomID, peer string, body matrix.Object) (matrix.Object, error) {
	if _, err := s.roomForPeer(ctx, roomID, peer); err != nil {
		return nil, err
	}
	ts := numberField(body, "ts")
	if ts <= 0 {
		return nil, matrix.ErrInvalidParam(`"ts" must be a unix timestamp in milliseconds`)
	}
	dir := "f"
	if named, ok := body["dir"].(string); ok && named != "" {
		dir = named
	}
	if dir != "f" && dir != "b" {
		return nil, matrix.ErrInvalidParam(`"dir" must be "f" or "b"`)
	}
	// `end` is the far bound of the window the peer is searching. An event outside it is
	// not the answer, and this server says so rather than handing back the nearest thing
	// it happens to hold.
	end := numberField(body, "end")
	rows, err := s.db.EventRepo.HistoryByTimestamp(roomID, ts, dir, 10)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	for _, row := range rows {
		if end > 0 && ((dir == "f" && row.OriginServerTS > end) || (dir == "b" && row.OriginServerTS < end)) {
			continue // outside the window the peer named
		}
		return matrix.Object{
			"origin":           s.ServerName(),
			"origin_server_ts": row.OriginServerTS,
			"event_id":         row.EventID,
		}, nil
	}
	return nil, matrix.ErrNotFound("this server holds no event of %s around %d", roomID, ts)
}
