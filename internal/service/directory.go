package service

import (
	"context"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// CreateAlias maps an alias to a room, rejecting duplicates.
func (s *Service) CreateAlias(ctx context.Context, alias string, roomID, creator string) (*persist.RoomAlias, error) {
	normalised, err := s.normaliseAlias(alias)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.RoomRepo.Get(roomID); err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no such room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	existing, err := s.db.AliasRepo.Get(normalised.String())
	if err == nil {
		// The specification answers with M_ALIAS_EXISTS for an alias that is
		// already taken, even when it points at the same room.
		return nil, matrix.ErrConflict(matrix.ErrCodeAliasExists,
			"alias %s already points at room %s", normalised, existing.RoomID)
	} else if !persist.IsNotFound(err) {
		return nil, matrix.ErrInternal(err)
	}
	if s.MembershipOf(ctx, roomID, creator) != matrix.MembershipJoin && !s.IsAdmin(ctx, creator) {
		return nil, matrix.ErrForbidden("you must be joined to %s to create an alias for it", roomID)
	}
	row := &persist.RoomAlias{
		Alias:   normalised.String(),
		RoomID:  roomID,
		Creator: creator,
		Server:  s.serverName,
	}
	if err := s.db.AliasRepo.Create(row); err != nil {
		if persist.IsDuplicate(err) {
			return nil, matrix.ErrConflict(matrix.ErrCodeAliasExists,
				"alias %s already exists", normalised)
		}
		return nil, matrix.ErrInternal(err)
	}
	return row, nil
}

// DeleteAlias removes an alias. The requester must be the alias creator, a
// room member with permission, or an administrator.
func (s *Service) DeleteAlias(ctx context.Context, alias string, requester string) error {
	row, err := s.db.AliasRepo.Get(alias)
	if err != nil {
		if persist.IsNotFound(err) {
			return matrix.ErrNotFound("no such alias %s", alias)
		}
		return matrix.ErrInternal(err)
	}
	if row.Creator != requester && !s.Allow(ctx, row.RoomID, requester, "redact", 50) &&
		!s.IsAdmin(ctx, requester) {
		return matrix.ErrForbidden("you do not have permission to delete this alias")
	}
	if err := s.db.AliasRepo.Delete(alias); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// IsAdmin reports whether a user is a server administrator.
func (s *Service) IsAdmin(ctx context.Context, userID string) bool {
	user, err := s.db.UserRepo.GetByID(userID)
	if err != nil {
		return false
	}
	return user.Admin
}

// RoomAliases lists the local aliases of a room.
func (s *Service) RoomAliases(ctx context.Context, roomID string) ([]string, error) {
	rows, err := s.db.AliasRepo.ListForRoom(roomID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	aliases := make([]string, 0, len(rows))
	for _, row := range rows {
		aliases = append(aliases, row.Alias)
	}
	return aliases, nil
}

// SetDirectoryVisibility publishes or unpublishes a room.
func (s *Service) SetDirectoryVisibility(ctx context.Context, roomID, visibility, requester string) error {
	if visibility != "public" && visibility != "private" {
		return matrix.ErrInvalidParam("visibility must be public or private")
	}
	if !s.IsJoined(ctx, roomID, requester) && !s.IsAdmin(ctx, requester) {
		return matrix.ErrForbidden("you must be joined to change the visibility of a room")
	}
	if err := s.db.VisibilityRepo.Set(roomID, visibility); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// DirectoryVisibility returns the directory visibility of a room.
func (s *Service) DirectoryVisibility(ctx context.Context, roomID string) (string, error) {
	if _, err := s.db.RoomRepo.Get(roomID); err != nil {
		if persist.IsNotFound(err) {
			return "", matrix.ErrNotFound("no such room %s", roomID)
		}
		return "", matrix.ErrInternal(err)
	}
	visibility, err := s.db.VisibilityRepo.Get(roomID)
	if err != nil {
		return "", matrix.ErrInternal(err)
	}
	return visibility, nil
}

// PublicRoom is one entry of the public room directory.
type PublicRoom struct {
	RoomID           string   `json:"room_id"`
	Name             string   `json:"name,omitempty"`
	Topic            string   `json:"topic,omitempty"`
	Aliases          []string `json:"aliases,omitempty"`
	CanonicalAlias   string   `json:"canonical_alias,omitempty"`
	NumJoinedMembers int      `json:"num_joined_members"`
	WorldReadable    bool     `json:"world_readable"`
	GuestCanJoin     bool     `json:"guest_can_join"`
	AvatarURL        string   `json:"avatar_url,omitempty"`
	RoomType         string   `json:"room_type,omitempty"`
}

// PublicRoomsResult is a page of the public room directory.
type PublicRoomsResult struct {
	Chunk      []PublicRoom `json:"chunk"`
	PrevBatch  string       `json:"prev_batch,omitempty"`
	NextBatch  string       `json:"next_batch,omitempty"`
	TotalRooms int64        `json:"total_rooms"`
}

// PublicRooms lists published rooms. Pagination tokens are row offsets of the
// visibility table, which are stable for the lifetime of a directory session.
func (s *Service) PublicRooms(ctx context.Context, filter string, limit int, since string) (*PublicRoomsResult, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	offset := 0
	if since != "" {
		parsed, err := strconv.Atoi(since)
		if err != nil || parsed < 0 {
			return nil, matrix.ErrInvalidParam("invalid since token %q", since)
		}
		offset = parsed
	}
	rooms, total, err := s.db.VisibilityRepo.Published(limit, offset, filter)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	chunk := make([]PublicRoom, 0, len(rooms))
	for _, room := range rooms {
		aliases, err := s.RoomAliases(ctx, room.ID)
		if err != nil {
			return nil, err
		}
		joined, err := s.db.MembershipRepo.Count(room.ID, matrix.MembershipJoin)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		entry := PublicRoom{
			RoomID:           room.ID,
			Name:             s.roomName(ctx, room.ID),
			Topic:            s.roomTopic(ctx, room.ID),
			Aliases:          aliases,
			CanonicalAlias:   s.roomAlias(ctx, room.ID),
			NumJoinedMembers: int(joined),
			WorldReadable:    s.HistoryVisibility(ctx, room.ID) == matrix.HistoryWorldReadable,
			GuestCanJoin:     s.guestsCanConnect(ctx, room.ID),
			AvatarURL:        s.roomAvatarURL(ctx, room.ID),
		}
		chunk = append(chunk, entry)
	}
	result := &PublicRoomsResult{
		Chunk:      chunk,
		TotalRooms: total,
		PrevBatch:  strconv.Itoa(max(offset-limit, 0)),
	}
	if int64(offset+len(chunk)) < total {
		result.NextBatch = strconv.Itoa(offset + len(chunk))
	}
	return result, nil
}

// guestsCanConnect reports whether guest accounts may join a room.
func (s *Service) guestsCanConnect(ctx context.Context, roomID string) bool {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeGuestAccess, "")
	if err != nil {
		return false
	}
	var content matrix.GuestAccessContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return false
	}
	return content.GuestAccess == "can_connect"
}

// GuestsCanRead reports whether an unauthenticated request may read a room: a
// guest needs both world readable history and permission to connect.
func (s *Service) GuestsCanRead(ctx context.Context, roomID string) bool {
	return s.HistoryVisibility(ctx, roomID) == matrix.HistoryWorldReadable && s.guestsCanConnect(ctx, roomID)
}

// RoomServers returns the servers that may serve a room: this homeserver plus
// the domains of its joined members.
func (s *Service) RoomServers(ctx context.Context, roomID string) ([]string, error) {
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	servers := []string{s.serverName}
	seen := map[string]bool{s.serverName: true}
	for _, member := range members {
		domain := matrix.DomainOf(member.UserID)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		servers = append(servers, domain)
	}
	return servers, nil
}

// RoomSummary builds the room summary payload for a room ID or alias.
func (s *Service) RoomSummary(ctx context.Context, idOrAlias, requester string) (matrix.Object, error) {
	room, err := s.MustRoom(ctx, idOrAlias)
	if err != nil {
		return nil, err
	}
	members, err := s.db.MembershipRepo.Members(room.ID, matrix.MembershipJoin)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	heroes := make([]string, 0, 5)
	invited := 0
	for _, member := range members {
		if member.UserID != requester && len(heroes) < 5 {
			heroes = append(heroes, member.UserID)
		}
	}
	if invitedRows, err := s.db.MembershipRepo.Members(room.ID, matrix.MembershipInvite); err == nil {
		invited = len(invitedRows)
	}
	aliases, err := s.RoomAliases(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	visibility, err := s.DirectoryVisibility(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	joinRule, err := s.db.StateRepo.JoinRule(room.ID)
	if err != nil {
		return nil, err
	}
	summary := matrix.Object{
		// The room summary uses the namespaced keys from spec revision v1.3.
		"m.heroes":               heroes,
		"m.joined_member_count":  len(members),
		"m.invited_member_count": invited,
		"empty":                  len(members) <= 1 && invited == 0,
		// Extra fields that help clients without a separate state request.
		"room_id":            room.ID,
		"name":               s.roomName(ctx, room.ID),
		"topic":              s.roomTopic(ctx, room.ID),
		"num_joined_members": len(members),
		"world_readable":     s.HistoryVisibility(ctx, room.ID) == matrix.HistoryWorldReadable,
		"guest_can_join":     s.guestsCanConnect(ctx, room.ID),
		"aliases":            aliases,
		"canonical_alias":    s.roomAlias(ctx, room.ID),
		"join_rules":         joinRule,
		"room_version":       room.RoomVersion,
	}
	if avatar := s.roomAvatarURL(ctx, room.ID); avatar != "" {
		summary["avatar_url"] = avatar
	}
	if visibility == "public" {
		summary["membership"] = s.MembershipOf(ctx, room.ID, requester)
	}
	return summary, nil
}

// UpgradeRoom creates the successor of a room and marks the old room as
// retired.
func (s *Service) UpgradeRoom(ctx context.Context, roomID, requester, newVersion, reason string) (string, error) {
	room, err := s.MustRoom(ctx, roomID)
	if err != nil {
		return "", err
	}
	if !s.Allow(ctx, roomID, requester, "ban", 50) && room.Creator != requester {
		return "", matrix.ErrForbidden("you do not have permission to upgrade this room")
	}
	if newVersion != "" && !versionSupported(newVersion) {
		return "", matrix.NewError(matrix.ErrCodeIncompatibleRoomVer, 400,
			"this room cannot be upgraded to version "+newVersion)
	}
	if newVersion == "" {
		newVersion = room.RoomVersion
	}

	predecessor := matrix.Predecessor{RoomID: room.ID, EventID: s.lastCreateEventID(roomID)}
	creation := matrix.Object{"predecessor": matrix.MustParseObject(matrix.Encode(predecessor))}
	params := CreateRoomParams{
		Creator:           requester,
		RoomVersion:       newVersion,
		Name:              s.roomName(ctx, room.ID),
		Topic:             s.roomTopic(ctx, room.ID),
		CreationContent:   creation,
		HistoryVisibility: s.HistoryVisibility(ctx, room.ID),
		JoinRule:          mustJoinRule(ctx, s, room.ID),
	}
	// Copy the room's current state into the replacement room.
	states, err := s.db.StateRepo.List(room.ID, nil, "")
	if err != nil {
		return "", matrix.ErrInternal(err)
	}
	for _, state := range states {
		if state.Type == matrix.TypeCreate || state.Type == matrix.TypeMember ||
			strings.HasPrefix(state.Type, "m.room.tombstone") {
			continue
		}
		params.InitialStates = append(params.InitialStates, matrix.Event{
			Type:     state.Type,
			StateKey: StateKeyOf(state.StateKey),
			Content:  matrix.RawMessage(state.Content),
		})
	}
	newRoom, err := s.CreateRoom(ctx, params)
	if err != nil {
		return "", err
	}
	tombstone := matrix.Object{"body": "This room has been replaced", "predecessor": creation["predecessor"]}
	if reason != "" {
		tombstone["reason"] = reason
	}
	if err := s.appendState(ctx, room.ID, requester, "m.room.tombstone", tombstone); err != nil {
		return "", err
	}
	return newRoom.ID, nil
}

// mustJoinRule returns the join rule of a room, tolerating read failures.
func mustJoinRule(ctx context.Context, s *Service, roomID string) string {
	rule, err := s.db.StateRepo.JoinRule(roomID)
	if err != nil {
		return matrix.JoinRulesInvite
	}
	return rule
}

// lastCreateEventID returns the m.room.create event id of a room.
func (s *Service) lastCreateEventID(roomID string) string {
	state, err := s.db.StateRepo.Get(roomID, matrix.TypeCreate, "")
	if err != nil {
		return ""
	}
	return state.EventID
}
