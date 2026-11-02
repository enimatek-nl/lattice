package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"gorm.io/gorm"
)

// The room is a directed acyclic graph: every event names the events it builds on,
// and an event's identity in room version 3 and later is a hash over that naming.
// Two consequences shape this file. First, prev_events and auth_events are decided
// once, at the moment an event is created, and can never be recomputed: the same
// event cited against a graph that has moved on is a different event with a
// different id. Second, a homeserver that cannot say which events are the leaves of
// the graph cannot create an event that other servers will attach to, which is why
// the leaves are stored rather than derived.

// maxPrevEvents is how many graph leaves a new event cites. The specification caps
// prev_events at 20 and asks for the deepest leaves first; a room with more leaves
// than that is a room in the middle of a state fork, and citing the deepest ones is
// what lets the graph converge again.
const maxPrevEvents = 20

// maxAuthEvents is the specification's cap on auth_events. The selection rules below
// never produce more than this for the event types Matrix defines, so exceeding it
// means a bug, and the event is refused rather than silently truncated: an event
// whose auth chain is missing an event cannot be checked by the receiver, and the
// receiver would reject it anyway with a less clear reason.
const maxAuthEvents = 10

// RoomVersionOf returns the room version in effect for a room. A room row written
// before the version was recorded is treated as the version this server creates,
// which is the only version its events could have been valid under.
func RoomVersionOf(room *persist.Room) string {
	if room == nil {
		return DefaultRoomVersion
	}
	version := strings.TrimSpace(room.RoomVersion)
	if version == "" {
		return DefaultRoomVersion
	}
	return version
}

// pdu is an event in the form the room version defines: the object that gets hashed,
// signed, identified and - once federation speaks - sent to peers.
type pdu struct {
	object  matrix.Object
	version string
	prevIDs []string
	authIDs []string
	eventID string
	encoded string
}

// BuildPDU assembles the room-version form of a draft event: it picks the graph
// leaves the event builds on, works out the depth those leaves imply, and selects the
// events that authorise it.
//
// All three reads happen inside the caller's transaction, so the prev_events of an
// event describe the graph as it was when the event was created rather than as it
// looks after a concurrent append. That is not a nicety: two events created in the
// same room at the same time are expected to cite each other's leaves, and an event
// that cites a leaf another transaction already replaced produces a fork nobody
// can resolve.
func (s *Service) BuildPDU(ctx context.Context, tx *gorm.DB, draft *EventDraft, version string, originServerTS int64) (*pdu, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events := persist.NewEventRepo(tx)
	state := persist.NewStateRepo(tx)

	// One leaf more than the cap is asked for, so a room that has outgrown it can be
	// reported rather than quietly trimmed.
	leaves, err := events.ForwardExtremities(draft.RoomID, maxPrevEvents+1)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	prevIDs := leaves
	if len(prevIDs) > maxPrevEvents {
		log.Printf("room %s has %d forward extremities, more than the %d an event can cite: "+
			"the deepest are cited and the rest wait for a fork to be resolved",
			draft.RoomID, len(prevIDs), maxPrevEvents)
		prevIDs = prevIDs[:maxPrevEvents]
	}
	depth, err := depthAfter(events, draft.RoomID, prevIDs)
	if err != nil {
		return nil, err
	}
	authIDs, err := selectAuthEvents(state, draft, version)
	if err != nil {
		return nil, err
	}

	content := matrix.Object{}
	if len(draft.Content) > 0 {
		content = matrix.MustParseObject(draft.Content)
	}
	object := matrix.Object{
		"type":             draft.Type,
		"room_id":          draft.RoomID,
		"sender":           draft.Sender,
		"origin_server_ts": originServerTS,
		"content":          content,
		"depth":            depth,
		"prev_events":      pduIDList(prevIDs),
		"auth_events":      pduIDList(authIDs),
	}
	if !usesDerivedEventIDs(version) {
		// Room versions 1 and 2 name the server that vouches for an event inside the
		// event itself. Later versions do not: the signature is what vouches, and it is
		// made by the server whose name is in it.
		object["origin"] = s.serverName
	}
	if draft.StateKey != nil {
		object["state_key"] = *draft.StateKey
	}
	if draft.Type == matrix.TypeRedaction {
		// Before room version 11 the redaction target is a top level property of the
		// event; from room version 11 it lives in the content, which is where the
		// client put it and where a redaction leaves it.
		if redacts, ok := content["redacts"].(string); ok && redacts != "" {
			if !eventcrypto.UseRoom11Algorithms(version) {
				object["redacts"] = redacts
			}
		}
	}
	if !usesDerivedEventIDs(version) {
		// Room versions 1 and 2 carry an id the origin server made up, and that id is
		// part of what gets hashed, so it has to be chosen before signing.
		object["event_id"] = matrix.NewEventID()
	}
	return &pdu{object: object, version: version, prevIDs: prevIDs, authIDs: authIDs}, nil
}

// SignAndIdentify adds the content hash and this server's signature to a PDU, and
// works out the event's id.
//
// The order is the specification's, and each step depends on the one before it: the
// hash covers the whole event, the signature is made over the redacted form with
// that hash inside it, and in room version 3 and later the id is the hash of the
// redacted form *including* the content hash. An id computed before the hash was
// added, or a signature made before the hash was in the event, produces an event
// every other server refuses.
func (s *Service) SignAndIdentify(ctx context.Context, p *pdu) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key, err := s.SigningKey(ctx)
	if err != nil {
		return err
	}
	signed, err := eventcrypto.HashAndSign(p.object, p.version, key.Private, s.serverName, key.ID)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	eventID, err := eventcrypto.EventID(signed, p.version)
	if err != nil {
		return matrix.ErrInvalidRequest("%v", err)
	}
	signed["event_id"] = eventID
	encoded, err := matrix.CanonicalJSON(signed)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	p.object = signed
	p.eventID = eventID
	p.encoded = string(encoded)
	return nil
}

// usesDerivedEventIDs reports whether a room version derives event ids from the
// event itself. Versions 1 and 2 take the id from a field of the event, chosen by the
// origin server, so those events need an id before they can be hashed. An unknown
// version is treated like the oldest one, because inventing ids is always valid while
// deriving them under the wrong rules is not.
func usesDerivedEventIDs(version string) bool {
	parsed, err := strconv.Atoi(strings.TrimSpace(version))
	if err != nil {
		return false
	}
	return parsed >= 3
}

// depthAfter computes the depth a new event carries: one more than the deepest of
// the events it cites. The specification caps depth at the largest integer a room
// version can name, and asks that a room already at the limit stays there rather
// than wrapping - an event whose depth went negative would be ordered before the
// room's creation by every state resolution algorithm that compares depths.
func depthAfter(events persist.EventRepo, roomID string, prevIDs []string) (int64, error) {
	if len(prevIDs) == 0 {
		return 1, nil
	}
	found, err := events.GetByIDs(prevIDs)
	if err != nil {
		return 0, matrix.ErrInternal(err)
	}
	deepest := int64(0)
	for _, eventID := range prevIDs {
		event, ok := found[eventID]
		if !ok {
			// A leaf that is no longer in the store means the room's graph was
			// pruned under this event. Depth is a hint for ordering, not a fact about
			// content, so the honest answer is to keep going from what is known.
			continue
		}
		if event.Depth > deepest {
			deepest = event.Depth
		}
	}
	if deepest >= math.MaxInt64-1 {
		return math.MaxInt64 - 1, nil
	}
	return deepest + 1, nil
}

// selectAuthEvents picks the events that authorise a new event, in the order the
// specification lists them.
//
// These are not "the events that happen to be in the room": they are the ones the
// authorisation rules read - who created the room, what the power levels are, the
// sender's own membership, the target's membership for a membership event, the join
// rule for a join, invite or knock, and the third-party invite or authorising member
// where one of those is claimed. A receiver re-runs the same rules against the same
// events, so an event that cites less than this cannot be checked and an event that
// cites something else has changed which rules apply to it.
func selectAuthEvents(state persist.StateRepo, draft *EventDraft, version string) ([]string, error) {
	var ids []string
	// A membership event by a user about themselves names that user twice: once as the
	// sender's membership and once as the target's, which is the same stored event. A
	// room version from 3 onwards rejects an event whose auth events repeat an id, so the
	// list is kept free of duplicates rather than left for the receiver to fault.
	seen := map[string]bool{}
	add := func(typ, stateKey string) error {
		row, err := state.Get(draft.RoomID, typ, stateKey)
		if err != nil {
			if persist.IsNotFound(err) {
				return nil
			}
			return matrix.ErrInternal(err)
		}
		if row.EventID != "" && !seen[row.EventID] {
			seen[row.EventID] = true
			ids = append(ids, row.EventID)
		}
		return nil
	}

	// Every room version this server speaks cites the create event: it is what makes
	// the room the same room, and the authorisation rules read its creator and its
	// m.federate property.
	if err := add(matrix.TypeCreate, ""); err != nil {
		return nil, err
	}
	if err := add(matrix.TypePowerLevels, ""); err != nil {
		return nil, err
	}
	if err := add(matrix.TypeMember, draft.Sender); err != nil {
		return nil, err
	}
	if draft.Type == matrix.TypeMember {
		stateKey := ""
		if draft.StateKey != nil {
			stateKey = *draft.StateKey
		}
		if err := add(matrix.TypeMember, stateKey); err != nil {
			return nil, err
		}
		content := matrix.MemberContent{}
		if len(draft.Content) > 0 {
			if err := matrix.DecodeContent(draft.Content, &content); err != nil {
				return nil, matrix.ErrInvalidRequest("the membership content cannot be read: %v", err)
			}
		}
		switch content.Membership {
		case matrix.MembershipJoin, matrix.MembershipInvite, matrix.MembershipKnock:
			if err := add(matrix.TypeJoinRules, ""); err != nil {
				return nil, err
			}
		}
		if content.Membership == matrix.MembershipInvite {
			if token := thirdPartyToken(content.ThirdPartyInvite); token != "" {
				if err := add(matrix.TypeThirdPartyInvite, token); err != nil {
					return nil, err
				}
			}
		}
		if content.Membership == matrix.MembershipJoin {
			if authorisedBy := joinAuthorisedBy(draft.Content); authorisedBy != "" && authorisedBy != draft.Sender {
				if err := add(matrix.TypeMember, authorisedBy); err != nil {
					return nil, err
				}
			}
		}
	}
	if len(ids) > maxAuthEvents {
		return nil, matrix.ErrInternal(fmt.Errorf(
			"the auth event selection produced %d events, which more than %d means a bug in the selection",
			len(ids), maxAuthEvents))
	}
	return ids, nil
}

// thirdPartyToken reads the token a third-party invite claims, if the content carries
// one.
func thirdPartyToken(invite matrix.Object) string {
	if len(invite) == 0 {
		return ""
	}
	signed, ok := invite["signed"].(matrix.Object)
	if !ok {
		if asMap, mapOK := invite["signed"].(map[string]any); mapOK {
			signed = asMap
		} else {
			return ""
		}
	}
	token, _ := signed["token"].(string)
	return token
}

// joinAuthorisedBy reads the member that authorised a restricted join. The field is
// newer than the MemberContent struct this server has used since the beginning, so it
// is read from the raw content rather than added to that struct's contract.
func joinAuthorisedBy(content matrix.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var claim struct {
		JoinAuthorisedViaUsersServer string `json:"join_authorised_via_users_server"`
	}
	if err := json.Unmarshal(content, &claim); err != nil {
		return ""
	}
	return claim.JoinAuthorisedViaUsersServer
}

// depthOf reads the depth the built PDU carries, so the storage column and the signed
// object cannot drift apart.
func depthOf(p *pdu) int64 {
	depth, _ := p.object["depth"].(int64)
	return depth
}

// pduIDList renders event ids as the array a PDU names them in, never as null:
// prev_events and auth_events are required properties of every event, and an empty
// one is an empty array.
func pduIDList(ids []string) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

// encodedIDs renders the id list for storage.
func encodedIDs(ids []string) string {
	if len(ids) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(raw)
}
