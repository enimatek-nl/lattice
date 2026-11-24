package service

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The extensions of simplified sliding sync. MSC4186 names them but leaves their shapes to the
// documents that define each one. Those documents arrived after this code: MSC4538 (to device),
// MSC4543 (end-to-end keys), MSC4537 (user data) and MSC4508 (typing, plus the semantics every
// extension shares) supersede the MSC3885/3884/3959/3961 drafts and each carries a changelog of
// where the experimental Synapse v1.151 answer differed.
//
// Both answer forms are served, and the URL decides which one: the unstable
// org.matrix.simplified_msc3575 path keeps the shape of the implementations that chose it, the
// stable /v4/sync path answers the accepted MSCs. That is what their own unstable-prefix
// sections ask for. The data behind the two is one and the same.
const (
	slidingExtToDevice    = "to_device"
	slidingExtE2EE        = "e2ee"
	slidingExtAccountData = "account_data"
	slidingExtReceipts    = "receipts"
	slidingExtTyping      = "typing"
)

// slidingExtensionRequest is what the extension configs have in common: whether the section is
// wanted at all, and the two reserved keys that narrow which rooms it covers. A config naming
// a section without saying "enabled" is asking for it; a client that wanted nothing would not
// have named it.
type slidingExtensionRequest struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Since   *string  `json:"since,omitempty"`
	Limit   *int     `json:"limit,omitempty"`
	Lists   []string `json:"lists,omitempty"`
	Rooms   []string `json:"rooms,omitempty"`
}

// wants reports whether the section was asked for.
func (e slidingExtensionRequest) wants() bool {
	return e.Enabled == nil || *e.Enabled
}

// slidingExtensions builds the sections of the response that are not rooms, and says whether
// any of them has something to report. That second answer matters to the long poll: a section
// that always answers - the one-time-key counts are always sent, because a client decides
// whether to upload more keys from them - must not be read as "something happened", or a
// connection with the key section switched on would never wait.
func (s *Service) slidingExtensions(ctx context.Context, userID, deviceID string,
	conn *slidingConn, req *matrix.SlidingSyncRequest, plans map[string]*slidingRoomPlan,
	initial, legacyEndpoint bool) (map[string]any, bool, error) {
	if len(req.Extensions) == 0 {
		return nil, false, nil
	}
	from := conn.row.LastPos
	if initial {
		// A connection that has just begun has been told nothing, so the sections that
		// report changes since a point report everything.
		from = 0
	}
	scope := slidingExtensionScope(plans)
	out := map[string]any{}
	var moved bool
	for name, raw := range req.Extensions {
		var config slidingExtensionRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &config); err != nil {
				return nil, false, matrix.ErrInvalidParam("the %s extension is not shaped as an extension: %v", name, err)
			}
		}
		if !config.wants() {
			continue
		}
		switch name {
		case slidingExtToDevice:
			section, err := s.slidingToDeviceExtension(userID, deviceID, conn, config, from, legacyEndpoint)
			if err != nil {
				return nil, false, err
			}
			if messages, ok := section[slidingToDeviceMessagesKey(legacyEndpoint)].([]matrix.Object); ok && len(messages) > 0 {
				moved = true
			}
			out[name] = section
		case slidingExtE2EE:
			section, err := s.slidingE2EEExtension(ctx, userID, deviceID, conn, from, initial)
			if err != nil {
				return nil, false, err
			}
			if _, changed := section["device_lists"]; changed {
				moved = true
			}
			out[name] = section
		case slidingExtAccountData:
			section, err := s.slidingAccountDataExtension(userID, scope.rooms(config, false), from)
			if err != nil {
				return nil, false, err
			}
			// The section is answered even when it is empty, so emptiness is what has to be
			// read: a connection whose account data has not moved must still be able to wait.
			if global, ok := section["global"].([]matrix.Object); ok && len(global) > 0 {
				moved = true
			}
			if byRoom, ok := section["rooms"].(map[string][]matrix.Object); ok && len(byRoom) > 0 {
				moved = true
			}
			out[name] = section
		case slidingExtReceipts:
			section, err := s.slidingReceiptsExtension(scope.rooms(config, true), from, legacyEndpoint)
			if err != nil {
				return nil, false, err
			}
			if len(section) > 0 {
				moved = true
			}
			out[name] = section
		case slidingExtTyping:
			section, err := s.slidingTypingExtension(userID, scope.rooms(config, true), from, legacyEndpoint)
			if err != nil {
				return nil, false, err
			}
			if len(section) > 0 {
				moved = true
			}
			out[name] = section
		default:
			// An extension this server does not implement is left out of the answer rather
			// than failed: a client that asked for presence, say, still needs the rooms.
			continue
		}
	}
	if len(out) == 0 {
		return nil, moved, nil
	}
	return out, moved, nil
}

// slidingScope is the set of rooms the ephemeral sections are allowed to report: the rooms in
// the connection's window, plus its subscriptions. MSC4508 says so in as many words - "a room
// being in scope does _not_ require the room to have an entry in the `rooms` section of the
// _response_" - which is the difference between a typing indicator that arrives and one that
// never can: a room that has nothing else to say has no entry in the response, and a section that
// only covers reported rooms then has nothing to report either, forever.
type slidingScope struct {
	lists  map[string][]string // room id -> the lists whose range carries it
	joined map[string]bool
	order  []string // sorted, so two responses list rooms alike
}

func slidingExtensionScope(plans map[string]*slidingRoomPlan) slidingScope {
	scope := slidingScope{lists: map[string][]string{}, joined: map[string]bool{}}
	for roomID, plan := range plans {
		scope.lists[roomID] = append([]string{}, plan.lists...)
		scope.joined[roomID] = plan.entry.membership == "join"
		scope.order = append(scope.order, roomID)
	}
	sort.Strings(scope.order)
	return scope
}

// rooms narrows the scope with one extension's own `lists` and `rooms` keys. Neither key present
// means every room in scope, and a key naming nothing means none (MSC4508, which also retired the
// "*" every pre-MSC implementation accepted - that star is kept, because it is what the clients
// written against them still send).
//
// wantJoined keeps the rooms the user is currently in. Typing notifications and receipts are
// ephemeral events of a joined room, as in /v3/sync, and MSC4186 deliberately keeps rooms the user
// left or was kicked from in the lists - reporting those would leak read activity from a room the
// client can no longer read. Room account data is the exception on purpose: MSC4548 asks for it
// whatever the membership, because a user can set it for an invite or a knock.
func (scope slidingScope) rooms(config slidingExtensionRequest, wantJoined bool) []string {
	restricted := len(config.Lists) > 0 || len(config.Rooms) > 0
	var out []string
	for _, roomID := range scope.order {
		if wantJoined && !scope.joined[roomID] {
			continue
		}
		if !restricted || slidingWantsRoom(config.Rooms, roomID) || slidingWantsList(config.Lists, scope.lists[roomID]) {
			out = append(out, roomID)
		}
	}
	return out
}

// slidingWantsRoom reads the reserved "rooms" key: a list naming nothing means no subscribed
// room, a star means all of them.
func slidingWantsRoom(wanted []string, roomID string) bool {
	if len(wanted) == 0 {
		return false
	}
	for _, entry := range wanted {
		if entry == "*" || entry == roomID {
			return true
		}
	}
	return false
}

// slidingWantsList reads the reserved "lists" key the same way, against the lists a room
// matched.
func slidingWantsList(wanted, lists []string) bool {
	if len(wanted) == 0 {
		return false
	}
	for _, entry := range wanted {
		if entry == "*" {
			return true
		}
		for _, name := range lists {
			if name == entry {
				return true
			}
		}
	}
	return false
}

// slidingToDeviceMessagesKey names the field the to-device messages arrive under. MSC4538
// requires `messages`, counts returning them under `events` as the Synapse v1.151 shape (its
// changelog, point 3) and leaves that shape to the unstable URL, whose clients were written
// against it (its unstable-prefix section).
func slidingToDeviceMessagesKey(legacyEndpoint bool) string {
	if legacyEndpoint {
		return "events"
	}
	return "messages"
}

// slidingToDeviceExtension answers the to device section: the messages queued for this device
// and a position the client gives back when it has taken them.
func (s *Service) slidingToDeviceExtension(userID, deviceID string, conn *slidingConn,
	config slidingExtensionRequest, from uint64, legacyEndpoint bool) (map[string]any, error) {
	// MSC4538: "A limit that is not a positive integer MUST be rejected with a 400 and an error
	// code of M_BAD_JSON". Synapse answered a zero with an empty section, and because that
	// section carried a next_batch the client went on to acknowledge everything queued - the
	// messages were gone without any client having read them.
	if config.Limit != nil && *config.Limit <= 0 {
		return nil, matrix.NewError(matrix.ErrCodeBadJSON, http.StatusBadRequest,
			"the to_device extension limit must be a positive integer")
	}
	next := from
	if config.Since != nil {
		pos, err := auth.ParseStreamPos(*config.Since)
		if err != nil {
			return nil, matrix.ErrInvalidParam("the to_device extension names no usable position: %v", err)
		}
		next = pos
	}
	if deviceID == "" {
		// A token that belongs to no device has no queue to read.
		return map[string]any{
			"next_batch": strconv.FormatUint(next, 10),
			slidingToDeviceMessagesKey(legacyEndpoint): []matrix.Object{},
		}, nil
	}
	limit := toDeviceLimit // the same cap the classic sync delivers with
	if config.Limit != nil && *config.Limit < limit {
		limit = *config.Limit
	}
	rows, err := s.db.ToDeviceRepo.Pending(userID, deviceID, next, limit)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	// A ToDeviceMessage names type, sender and content and nothing else: MSC4538 says so in its
	// own table, and the /v3/sync to_device events it points at agree. A message id would be a
	// promise this endpoint cannot keep either - the MSC delivers at least once and the queue
	// keeps no ids to deliver them by.
	messages := make([]matrix.Object, 0, len(rows))
	for _, row := range rows {
		if row.StreamPos > next {
			next = row.StreamPos
		}
		messages = append(messages, matrix.Object{
			"type":    row.Type,
			"sender":  row.SenderID,
			"content": matrix.RawMessage(row.Content),
		})
	}
	return map[string]any{
		"next_batch": strconv.FormatUint(next, 10),
		slidingToDeviceMessagesKey(legacyEndpoint): messages,
	}, nil
}

// slidingE2EEExtension answers the key section: how many one-time keys the device has left,
// which fallback keys are unused, and whose device lists have changed.
func (s *Service) slidingE2EEExtension(ctx context.Context, userID, deviceID string,
	conn *slidingConn, from uint64, initial bool) (map[string]any, error) {
	section := map[string]any{}
	// MSC4543 marks both key fields Required, so they are answered even by a token that belongs
	// to no device: such a device holds no keys, which is an empty object and an empty list,
	// never an absence. A strict client reads the section as one structure, so a field that is
	// not there - or is null because the Go slice was nil - costs it the device lists too.
	counts := matrix.DeviceOneTimeCount{}
	fallback := []string{}
	if deviceID != "" {
		var err error
		counts, fallback, err = s.OneTimeKeyCounts(ctx, userID, deviceID)
		if err != nil {
			return nil, err
		}
	}
	// Always sent: a client decides whether to upload more keys from these numbers, and
	// a missing section cannot tell it "nothing changed" from "there are none".
	section["device_one_time_keys_count"] = counts
	section["device_unused_fallback_key_types"] = fallback
	if initial {
		// A new connection asks for whole key sets from POST /keys/query, as it does with
		// the classic sync; the delta is for the connections that have them already.
		return section, nil
	}
	lists, err := s.DeviceListChanges(ctx, userID, from, s.StreamPos(), deviceID)
	if err != nil {
		return nil, err
	}
	if lists != nil && (len(lists.Changed) > 0 || len(lists.Left) > 0) {
		section["device_lists"] = lists
	}
	return section, nil
}

// slidingAccountDataExtension answers the account data section: the user's own global data and
// the data of the rooms this response carries.
func (s *Service) slidingAccountDataExtension(userID string, rooms []string, from uint64) (map[string]any, error) {
	global, err := s.accountDataEvents(userID, from)
	if err != nil {
		return nil, err
	}
	section := map[string]any{"global": accountDataObjects(global)}
	byRoom := map[string][]matrix.Object{}
	for _, roomID := range rooms {
		events, err := s.roomAccountDataEvents(userID, roomID, from)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			continue
		}
		byRoom[roomID] = accountDataObjects(events)
	}
	if len(byRoom) > 0 {
		section["rooms"] = byRoom
	}
	return section, nil
}

// slidingEphemeralRoomValue decides what one room is worth inside the typing and receipts
// sections. MSC4551 says it plainly for receipts - the bare content of an m.receipt event,
// because the section can only carry receipts and the wrapper adds nothing - and MSC4508 says
// the same for typing. Both then hand the wrapped, event-shaped form to the unstable URL, where
// the clients that read it were written against Synapse.
//
// That is one decision with two answers, so it is made once, here.
func slidingEphemeralRoomValue(event matrix.Event, legacyEndpoint bool) any {
	if legacyEndpoint {
		return matrix.Object{"type": event.Type, "content": event.Content}
	}
	// The raw bytes of the event's content, which marshal back as the object they came from.
	return event.Content
}

// slidingReceiptsExtension answers the receipts section, one merged m.receipt per room.
func (s *Service) slidingReceiptsExtension(rooms []string, from uint64, legacyEndpoint bool) (map[string]any, error) {
	events, err := s.receiptEventsForRooms(rooms, from)
	if err != nil {
		return nil, err
	}
	byRoom := map[string]any{}
	for roomID, list := range events {
		if len(list) == 0 {
			continue
		}
		byRoom[roomID] = slidingEphemeralRoomValue(list[0], legacyEndpoint)
	}
	if len(byRoom) == 0 {
		return map[string]any{}, nil
	}
	return map[string]any{"rooms": byRoom}, nil
}

// slidingTypingExtension answers the typing section.
func (s *Service) slidingTypingExtension(userID string, rooms []string, from uint64, legacyEndpoint bool) (map[string]any, error) {
	byRoom := map[string]any{}
	for _, roomID := range rooms {
		events, err := s.typingEvents(roomID, userID, from)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			continue
		}
		byRoom[roomID] = slidingEphemeralRoomValue(events[0], legacyEndpoint)
	}
	if len(byRoom) == 0 {
		return map[string]any{}, nil
	}
	return map[string]any{"rooms": byRoom}, nil
}

// accountDataObjects renders account data events in the shape the extension sections use,
// which names the type and the content and nothing else.
func accountDataObjects(events []matrix.Event) []matrix.Object {
	out := make([]matrix.Object, 0, len(events))
	for _, event := range events {
		out = append(out, matrix.Object{"type": event.Type, "content": event.Content})
	}
	return out
}
