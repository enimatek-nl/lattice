package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// MarkDirectRoom records a room in the m.direct account data of its creator,
// keyed by invitee, so clients list it as a direct message room.
func (s *Service) MarkDirectRoom(ctx context.Context, creator, roomID string, invitees []string) error {
	if len(invitees) == 0 {
		return nil
	}
	raw, err := s.AccountData(ctx, creator, creator, "", matrix.TypeDirect)
	if err != nil {
		// An account that never tagged a room has no m.direct data yet, which is
		// not a reason to fail the room creation.
		var merr *matrix.Error
		if !errors.As(err, &merr) || merr.Code != matrix.ErrCodeNotFound {
			return err
		}
		raw = nil
	}
	direct := matrix.Object{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &direct); err != nil {
			direct = matrix.Object{}
		}
	}
	for _, invitee := range invitees {
		if invitee == "" || invitee == creator {
			continue
		}
		rooms := stringSlice(direct[invitee])
		found := false
		for _, existing := range rooms {
			if existing == roomID {
				found = true
				break
			}
		}
		if !found {
			rooms = append(rooms, roomID)
		}
		direct[invitee] = rooms
	}
	encoded, err := json.Marshal(direct)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	return s.SetAccountData(ctx, creator, creator, "", "m.direct", encoded)
}

// stringSlice coerces a decoded JSON value into a slice of strings.
func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// DirectRoom is one entry of the room listings MSC2666 adds: a room id plus
// the membership the named user holds in it.
type DirectRoom struct {
	RoomID     string `json:"room_id"`
	Membership string `json:"membership"`
}

// MutualRooms lists the rooms two users have joined in common, most recently
// active first. Rooms either of them tagged as a direct message are left out,
// which is the point of the endpoint: a client uses the result to offer a new
// group chat without exposing private DMs.
func (s *Service) MutualRooms(ctx context.Context, requester, target string) ([]DirectRoom, error) {
	if !s.UserExists(ctx, target) {
		return nil, matrix.ErrNotFound("user %s does not exist", target)
	}
	ids, err := s.db.MembershipRepo.MutualRooms(requester, target)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	private := s.directRoomSet(ctx, requester)
	for roomID := range s.directRoomSet(ctx, target) {
		private[roomID] = true
	}
	out := make([]DirectRoom, 0, len(ids))
	for _, roomID := range ids {
		if private[roomID] {
			continue
		}
		out = append(out, DirectRoom{RoomID: roomID, Membership: matrix.MembershipJoin})
	}
	s.sortByActivity(out)
	return out, nil
}

// DirectRooms lists the rooms a user shares through its m.direct account data,
// most recently active first. Only the user itself may ask for the list, plus
// server administrators.
func (s *Service) DirectRooms(ctx context.Context, target, requester string) ([]DirectRoom, error) {
	if target != requester && !s.IsAdmin(ctx, requester) {
		return nil, matrix.ErrForbidden("only %s can list its direct rooms", target)
	}
	if !s.UserExists(ctx, target) {
		return nil, matrix.ErrNotFound("user %s does not exist", target)
	}
	out := make([]DirectRoom, 0, 4)
	seen := map[string]bool{}
	for _, roomID := range s.directRoomList(ctx, target) {
		if seen[roomID] {
			continue
		}
		seen[roomID] = true
		membership := s.MembershipOf(ctx, roomID, target)
		if membership != matrix.MembershipJoin && membership != matrix.MembershipInvite {
			// A room the user left or never joined is no longer part of the
			// direct message list.
			continue
		}
		out = append(out, DirectRoom{RoomID: roomID, Membership: membership})
	}
	s.sortByActivity(out)
	return out, nil
}

// sortByActivity orders rooms by the stream position of their newest event,
// newest first, which is how clients expect these lists.
func (s *Service) sortByActivity(rooms []DirectRoom) {
	positions := make(map[string]uint64, len(rooms))
	for _, entry := range rooms {
		pos, err := s.db.EventRepo.LatestRoomStreamPos(entry.RoomID)
		if err != nil {
			continue
		}
		positions[entry.RoomID] = pos
	}
	sort.SliceStable(rooms, func(i, j int) bool {
		return positions[rooms[i].RoomID] > positions[rooms[j].RoomID]
	})
}

// directRoomSet returns the rooms a user tagged as direct messages, keyed by
// room id for cheap lookups.
func (s *Service) directRoomSet(ctx context.Context, userID string) map[string]bool {
	set := map[string]bool{}
	for _, roomID := range s.directRoomList(ctx, userID) {
		set[roomID] = true
	}
	return set
}

// directRoomList returns the room ids of the m.direct account data of a user,
// which groups rooms per recipient.
func (s *Service) directRoomList(ctx context.Context, userID string) []string {
	raw, err := s.AccountData(ctx, userID, userID, "", matrix.TypeDirect)
	if err != nil || len(raw) == 0 {
		return nil
	}
	grouped := matrix.MustParseObject(raw)
	var rooms []string
	for _, value := range grouped {
		rooms = append(rooms, stringSlice(value)...)
	}
	return rooms
}
