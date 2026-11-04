package service

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The extensions of simplified sliding sync. MSC4186 names them but leaves their shapes to the
// documents that define each one, so this server answers in the shape the implementations
// clients actually run - to device (MSC3885), end-to-end keys (MSC3884), account data
// (MSC3959), receipts (MSC3960) and typing (MSC3961) - and the data is the same the classic
// /sync already assembles for those things.
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
	conn *slidingConn, req *matrix.SlidingSyncRequest, response *matrix.SlidingSyncResponse,
	initial bool) (map[string]any, bool, error) {
	if len(req.Extensions) == 0 {
		return nil, false, nil
	}
	from := conn.row.LastPos
	if initial {
		// A connection that has just begun has been told nothing, so the sections that
		// report changes since a point report everything.
		from = 0
	}
	rooms := slidingExtensionRooms(req, response)
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
			section, err := s.slidingToDeviceExtension(userID, deviceID, conn, config, from)
			if err != nil {
				return nil, false, err
			}
			if events, ok := section["events"].([]matrix.Event); ok && len(events) > 0 {
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
			section, err := s.slidingAccountDataExtension(userID, rooms, from)
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
			section, err := s.slidingReceiptsExtension(rooms, from)
			if err != nil {
				return nil, false, err
			}
			if len(section) > 0 {
				moved = true
			}
			out[name] = section
		case slidingExtTyping:
			section, err := s.slidingTypingExtension(userID, rooms, from)
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

// slidingExtensionRooms decides which rooms the ephemeral sections cover. They follow the
// rooms in this response: a section does not report a room the client has not been given, and
// the reserved lists and rooms keys narrow that further.
func slidingExtensionRooms(req *matrix.SlidingSyncRequest, response *matrix.SlidingSyncResponse) []string {
	if response == nil || len(response.Rooms) == 0 {
		return nil
	}
	var restricted *slidingExtensionRequest
	for name, raw := range req.Extensions {
		if name != slidingExtAccountData && name != slidingExtReceipts && name != slidingExtTyping {
			continue
		}
		var config slidingExtensionRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &config); err != nil {
				continue
			}
		}
		if len(config.Lists) > 0 || len(config.Rooms) > 0 {
			restricted = &config
			break
		}
	}
	out := make([]string, 0, len(response.Rooms))
	for roomID, room := range response.Rooms {
		if restricted == nil {
			out = append(out, roomID)
			continue
		}
		if slidingWantsRoom(restricted.Rooms, roomID) || slidingWantsList(restricted.Lists, room.Lists) {
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

// slidingToDeviceExtension answers the to device section: the messages queued for this device
// and a position the client gives back when it has taken them.
func (s *Service) slidingToDeviceExtension(userID, deviceID string, conn *slidingConn,
	config slidingExtensionRequest, from uint64) (map[string]any, error) {
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
		return map[string]any{"next_batch": strconv.FormatUint(next, 10), "events": []matrix.Event{}}, nil
	}
	limit := toDeviceLimit // the same cap the classic sync delivers with
	if config.Limit != nil && *config.Limit > 0 && *config.Limit < limit {
		limit = *config.Limit
	}
	rows, err := s.db.ToDeviceRepo.Pending(userID, deviceID, next, limit)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	events := make([]matrix.Event, 0, len(rows))
	for _, row := range rows {
		if row.StreamPos > next {
			next = row.StreamPos
		}
		events = append(events, matrix.Event{
			Type:           row.Type,
			Sender:         row.SenderID,
			OriginServerTS: row.TS,
			Content:        matrix.RawMessage(row.Content),
		})
	}
	return map[string]any{"next_batch": strconv.FormatUint(next, 10), "events": events}, nil
}

// slidingE2EEExtension answers the key section: how many one-time keys the device has left,
// which fallback keys are unused, and whose device lists have changed.
func (s *Service) slidingE2EEExtension(ctx context.Context, userID, deviceID string,
	conn *slidingConn, from uint64, initial bool) (map[string]any, error) {
	section := map[string]any{}
	if deviceID != "" {
		counts, fallback, err := s.OneTimeKeyCounts(ctx, userID, deviceID)
		if err != nil {
			return nil, err
		}
		// Always sent: a client decides whether to upload more keys from these numbers, and
		// a missing section cannot tell it "nothing changed" from "there are none".
		section["device_one_time_keys_count"] = counts
		section["device_unused_fallback_key_types"] = fallback
	}
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

// slidingReceiptsExtension answers the receipts section, one merged m.receipt per room.
func (s *Service) slidingReceiptsExtension(rooms []string, from uint64) (map[string]any, error) {
	events, err := s.receiptEventsForRooms(rooms, from)
	if err != nil {
		return nil, err
	}
	byRoom := map[string]matrix.Object{}
	for roomID, list := range events {
		if len(list) == 0 {
			continue
		}
		byRoom[roomID] = matrix.Object{"type": list[0].Type, "content": list[0].Content}
	}
	if len(byRoom) == 0 {
		return map[string]any{}, nil
	}
	return map[string]any{"rooms": byRoom}, nil
}

// slidingTypingExtension answers the typing section.
func (s *Service) slidingTypingExtension(userID string, rooms []string, from uint64) (map[string]any, error) {
	byRoom := map[string]matrix.Object{}
	for _, roomID := range rooms {
		events, err := s.typingEvents(roomID, userID, from)
		if err != nil {
			return nil, err
		}
		if len(events) == 0 {
			continue
		}
		byRoom[roomID] = matrix.Object{"type": events[0].Type, "content": events[0].Content}
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
