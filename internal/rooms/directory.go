package rooms

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// Directory paging limits. The default follows the Client-Server specification; the
// ceiling bounds one request, because each listed room costs a little state reading.
const (
	defaultDirectoryLimit = 100
	maxDirectoryLimit     = 500

	// directoryPowerLevel is the power level needed to publish or unpublish a room.
	// Publishing advertises the room to everyone, so it is a moderator decision rather
	// than an ordinary member one; the reference server uses the same number.
	directoryPowerLevel = 50
)

// DirectoryRequest is one page of this server's published rooms.
type DirectoryRequest struct {
	// Limit is the maximum number of rooms; zero means the default.
	Limit int
	// Since is the token from a previous page. Empty means the first page.
	Since string
	// Search matches a room's name or topic, case-insensitively.
	Search string
}

// ListedRoom is one entry in the directory.
type ListedRoom struct {
	RoomID           string   `json:"room_id"`
	NumJoinedMembers int      `json:"num_joined_members"`
	WorldReadable    bool     `json:"world_readable"`
	GuestCanJoin     bool     `json:"guest_can_join"`
	Name             string   `json:"name,omitempty"`
	Topic            string   `json:"topic,omitempty"`
	CanonicalAlias   string   `json:"canonical_alias,omitempty"`
	Aliases          []string `json:"aliases,omitempty"`
	RoomType         string   `json:"room_type,omitempty"`
}

// DirectoryPage is the response.
type DirectoryPage struct {
	Chunk      []ListedRoom `json:"chunk"`
	TotalRooms int          `json:"total_rooms"`
	// NextBatch is the token for the following page. It is omitted when this page was
	// the last one, which is how a client knows to stop.
	NextBatch string `json:"next_batch,omitempty"`
}

// PublicRooms lists the rooms this server publishes.
//
// Publishing is a decision, not a consequence of the join rule: a room can be open to
// anyone and still not want to be found. That is why the directory reads a flag on the
// room rather than inferring visibility from `m.room.join_rules`.
//
// Each entry carries the facts a room browser shows. The name and topic come from the
// room's cached directory text, which is rewritten whenever that state is written; the
// member count, the canonical alias and the room type are read from the room's state,
// because they are questions about the state rather than a copy of it. Counting members
// per room is work this server would rather not repeat on every page — a denormalised
// count is the way to fix that, and it is recorded as a scaling limitation rather than
// hidden.
func (s *Service) PublicRooms(ctx context.Context, req DirectoryRequest) (DirectoryPage, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = defaultDirectoryLimit
	}
	if limit > maxDirectoryLimit {
		limit = maxDirectoryLimit
	}
	offset, err := parseDirectoryToken(req.Since)
	if err != nil {
		return DirectoryPage{}, err
	}
	rows, total, err := s.rooms.ListPublished(ctx, req.Search, offset, limit)
	if err != nil {
		return DirectoryPage{}, fmt.Errorf("rooms: cannot list the published rooms: %w", err)
	}
	page := DirectoryPage{
		Chunk:      make([]ListedRoom, 0, len(rows)),
		TotalRooms: int(total),
	}
	for _, room := range rows {
		entry, err := s.listRoom(ctx, room)
		if err != nil {
			return DirectoryPage{}, err
		}
		page.Chunk = append(page.Chunk, entry)
	}
	// A short page means there is nothing past it. A full page may still be the last
	// one, so the token is offered whenever a following page could exist.
	if offset+len(rows) < int(total) {
		page.NextBatch = directoryToken(offset + len(rows))
	}
	return page, nil
}

// listRoom assembles one directory entry.
func (s *Service) listRoom(ctx context.Context, room *persist.Room) (ListedRoom, error) {
	entry := ListedRoom{
		RoomID:        room.RoomID,
		Name:          room.Name,
		Topic:         room.Topic,
		WorldReadable: false,
	}
	joined, err := s.countJoinedMembers(ctx, room.RoomID)
	if err != nil {
		return entry, err
	}
	entry.NumJoinedMembers = joined

	visibility, err := s.historyVisibility(ctx, room.RoomID)
	if err != nil {
		return entry, err
	}
	entry.WorldReadable = visibility == "world_readable"

	joinRule, err := s.joinRule(ctx, room.RoomID)
	if err != nil {
		return entry, err
	}
	// A guest may join when the room is open to anyone and has not said otherwise with
	// m.room.guest_join, whose absence means the default of allowing guests.
	entry.GuestCanJoin = joinRule == "public" && s.guestsAllowed(ctx, room.RoomID)

	if entry.CanonicalAlias, err = s.canonicalAliasOf(ctx, room.RoomID); err != nil {
		return entry, err
	}
	aliases, err := s.alias.ByRoom(ctx, room.RoomID)
	if err != nil {
		return entry, fmt.Errorf("rooms: cannot load the room's aliases: %w", err)
	}
	for _, alias := range aliases {
		entry.Aliases = append(entry.Aliases, alias.Alias)
	}
	entry.RoomType, err = s.roomType(ctx, room.RoomID)
	return entry, err
}

// countJoinedMembers counts the room members whose current membership is join.
func (s *Service) countJoinedMembers(ctx context.Context, roomID string) (int, error) {
	entries, err := s.state.ByType(ctx, roomID, eventauth.TypeMember)
	if err != nil {
		return 0, fmt.Errorf("rooms: cannot load the room's members: %w", err)
	}
	joined := 0
	for _, entry := range entries {
		event, err := s.eventJSON(ctx, entry.EventID)
		if err != nil {
			return 0, err
		}
		content, _ := event["content"].(map[string]any)
		if content["membership"] == eventauth.MembershipJoin {
			joined++
		}
	}
	return joined, nil
}

// joinRule reads the room's m.room.join_rules value.
func (s *Service) joinRule(ctx context.Context, roomID string) (string, error) {
	entry, err := s.state.Get(ctx, roomID, eventauth.TypeJoinRules, "")
	if err != nil {
		if notFound(err) {
			return "invite", nil
		}
		return "", fmt.Errorf("rooms: cannot load the room's join rule: %w", err)
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return "", err
	}
	content, _ := event["content"].(map[string]any)
	rule, _ := content["join_rule"].(string)
	if rule == "" {
		return "invite", nil
	}
	return rule, nil
}

// guestsAllowed reads m.room.guest_join. A room without that event allows guests, which
// is the room version default rather than a choice this server is making.
func (s *Service) guestsAllowed(ctx context.Context, roomID string) bool {
	entry, err := s.state.Get(ctx, roomID, "m.room.guest_join", "")
	if err != nil {
		return true
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return true
	}
	content, _ := event["content"].(map[string]any)
	allowed, ok := content["guest_can_join"].(bool)
	return !ok || allowed
}

// canonicalAliasOf reads the alias the room calls itself by.
func (s *Service) canonicalAliasOf(ctx context.Context, roomID string) (string, error) {
	entry, err := s.state.Get(ctx, roomID, canonicalAliasEventType, "")
	if err != nil {
		if notFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("rooms: cannot load the room's canonical alias: %w", err)
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return "", err
	}
	content, _ := event["content"].(map[string]any)
	alias, _ := content["alias"].(string)
	return alias, nil
}

// roomType reads the room type recorded in m.room.create.
func (s *Service) roomType(ctx context.Context, roomID string) (string, error) {
	entry, err := s.state.Get(ctx, roomID, eventauth.TypeCreate, "")
	if err != nil {
		if notFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("rooms: cannot load the room's create event: %w", err)
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return "", err
	}
	content, _ := event["content"].(map[string]any)
	roomType, _ := content["type"].(string)
	return roomType, nil
}

// SetRoomVisibility publishes or unpublishes a room in this server's directory.
//
// Publishing tells everyone this server's directory that the room exists and how to find
// it, so it is a moderator decision: the requester has to be a joined member with the
// power level the reference server requires for it.
func (s *Service) SetRoomVisibility(ctx context.Context, roomID, requester string, published bool) error {
	if err := s.requireMembership(ctx, roomID, requester, membershipJoined); err != nil {
		return err
	}
	level, err := s.powerLevel(ctx, roomID, requester)
	if err != nil {
		return err
	}
	if level < directoryPowerLevel {
		return matrixerr.New(matrixerr.CodeForbidden,
			"Publishing a room in the directory needs the room's moderator powers.")
	}
	if err := s.rooms.SetPublished(ctx, roomID, published); err != nil {
		if notFound(err) {
			return matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
		}
		return fmt.Errorf("rooms: cannot change the room's directory visibility: %w", err)
	}
	return nil
}

// RoomVisibility reports whether a room is published, for someone with the power to
// change it.
func (s *Service) RoomVisibility(ctx context.Context, roomID, requester string) (string, error) {
	if err := s.requireMembership(ctx, roomID, requester, membershipJoined); err != nil {
		return "", err
	}
	room, err := s.requireRoom(ctx, roomID)
	if err != nil {
		return "", err
	}
	if room.Published {
		return "public", nil
	}
	return "private", nil
}

// powerLevel reads the requester's power level in the room. A room without an
// m.room.power_levels event gives everyone the default of zero, which is what the room
// version rules assume.
func (s *Service) powerLevel(ctx context.Context, roomID, requester string) (int, error) {
	entry, err := s.state.Get(ctx, roomID, eventauth.TypePowerLevels, "")
	if err != nil {
		if notFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("rooms: cannot load the room's power levels: %w", err)
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return 0, err
	}
	content, _ := event["content"].(map[string]any)
	levels, _ := content["users"].(map[string]any)
	if value, ok := levels[requester]; ok {
		return asInt(value), nil
	}
	return asInt(content["users_default"]), nil
}

// refreshDirectoryText keeps the cached name and topic in step with the room's state. It
// is called wherever state is written, so a published room cannot be listed under a name
// it stopped using.
func (s *Service) refreshDirectoryText(ctx context.Context, roomID string, eventType string, content map[string]any) error {
	if eventType != "m.room.name" && eventType != "m.room.topic" {
		return nil
	}
	name, topic, err := s.directoryText(ctx, roomID)
	if err != nil {
		return err
	}
	if text, ok := content["name"].(string); eventType == "m.room.name" && ok {
		name = text
	}
	if text, ok := content["topic"].(string); eventType == "m.room.topic" && ok {
		topic = text
	}
	return s.rooms.SetDirectoryText(ctx, roomID, name, topic)
}

// directoryText reads the room's current name and topic state.
func (s *Service) directoryText(ctx context.Context, roomID string) (string, string, error) {
	var name, topic string
	entry, err := s.state.Get(ctx, roomID, "m.room.name", "")
	if err != nil {
		if !notFound(err) {
			return "", "", fmt.Errorf("rooms: cannot load the room's name: %w", err)
		}
	} else if event, err := s.eventJSON(ctx, entry.EventID); err == nil {
		content, _ := event["content"].(map[string]any)
		name, _ = content["name"].(string)
	}
	entry, err = s.state.Get(ctx, roomID, "m.room.topic", "")
	if err != nil {
		if !notFound(err) {
			return "", "", fmt.Errorf("rooms: cannot load the room's topic: %w", err)
		}
	} else if event, err := s.eventJSON(ctx, entry.EventID); err == nil {
		content, _ := event["content"].(map[string]any)
		topic, _ = content["topic"].(string)
	}
	return name, topic, nil
}

// directoryToken is the paging token. It counts rooms rather than naming a position in
// the event stream, because the directory is an enumeration of the published set and not
// a timeline: a client that asks for the next page wants the next rooms, and a token that
// named a stream position would silently skip rooms published in the meantime.
func directoryToken(offset int) string { return "p" + strconv.Itoa(offset) }

// parseDirectoryToken reads a token this server issued. Anything else is refused rather
// than treated as the first page, so a client carrying a stale or foreign token learns
// about it instead of silently receiving the same page twice.
func parseDirectoryToken(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	if !strings.HasPrefix(raw, "p") {
		return 0, matrixerr.New(matrixerr.CodeInvalidParam,
			"This server does not recognise that directory token.")
	}
	value, err := strconv.Atoi(raw[1:])
	if err != nil || value < 0 {
		return 0, matrixerr.New(matrixerr.CodeInvalidParam,
			"This server does not recognise that directory token.")
	}
	return value, nil
}

// anySlice reads a JSON array out of decoded event content.
func anySlice(value any) []any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	return list
}

// asInt reads a number out of decoded event JSON. The type depends on how the JSON was
// decoded: a plain decoder gives float64, one configured with UseNumber gives
// json.Number, and values this server built itself in Go give int. Treating anything
// else as zero would quietly hand the room's power to nobody.
func asInt(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int32:
		return int(number)
	case int64:
		return int(number)
	case float64:
		return int(number)
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return 0
	}
}

// RoomSummary answers the question "what room is this?" for one room, which is what a
// client needs to draw a room preview or a join prompt.
//
// It is the same information the directory publishes for a room, plus what only the
// room itself can say: its room version, its join rule, and — for a room whose join rule
// is restricted — the rooms whose members are allowed in. It is not a way to read a
// private room's contents: the access rule is the one used everywhere else in this
// package, so someone with no standing in a room sees nothing unless the room opened its
// history to the world.
type RoomSummary struct {
	RoomID           string   `json:"room_id"`
	NumJoinedMembers int      `json:"num_joined_members"`
	WorldReadable    bool     `json:"world_readable"`
	GuestCanJoin     bool     `json:"guest_can_join"`
	Name             string   `json:"name,omitempty"`
	Topic            string   `json:"topic,omitempty"`
	CanonicalAlias   string   `json:"canonical_alias,omitempty"`
	Aliases          []string `json:"aliases,omitempty"`
	AvatarURL        string   `json:"avatar_url,omitempty"`
	RoomType         string   `json:"room_type,omitempty"`
	RoomVersion      string   `json:"room_version,omitempty"`
	JoinRule         string   `json:"join_rule,omitempty"`
	// AllowedRoomIDs lists the rooms whose members may join when the join rule is
	// restricted or knock_restricted. It is omitted for any other join rule, because an
	// empty list would suggest a restriction with no rooms behind it.
	AllowedRoomIDs []string `json:"allowed_room_ids,omitempty"`
}

// RoomSummary returns the summary of a room, named by room ID or by a local alias.
func (s *Service) RoomSummary(ctx context.Context, roomIDOrAlias, requester string) (RoomSummary, error) {
	roomID := roomIDOrAlias
	if strings.HasPrefix(roomIDOrAlias, "#") {
		entry, err := s.alias.ByAlias(ctx, roomIDOrAlias)
		if err != nil {
			if notFound(err) {
				return RoomSummary{}, matrixerr.Newf(matrixerr.CodeNotFound,
					"This server has no alias %q.", roomIDOrAlias)
			}
			return RoomSummary{}, fmt.Errorf("rooms: cannot resolve the alias: %w", err)
		}
		roomID = entry.RoomID
	}
	if err := s.requireMembership(ctx, roomID, requester, membershipAny); err != nil {
		return RoomSummary{}, err
	}
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return RoomSummary{}, matrixerr.Newf(matrixerr.CodeNotFound,
				"Room %q is not known to this server.", roomID)
		}
		return RoomSummary{}, fmt.Errorf("rooms: cannot load the room: %w", err)
	}
	listed, err := s.listRoom(ctx, room)
	if err != nil {
		return RoomSummary{}, err
	}
	summary := RoomSummary{
		RoomID:           listed.RoomID,
		NumJoinedMembers: listed.NumJoinedMembers,
		WorldReadable:    listed.WorldReadable,
		GuestCanJoin:     listed.GuestCanJoin,
		Name:             listed.Name,
		Topic:            listed.Topic,
		CanonicalAlias:   listed.CanonicalAlias,
		Aliases:          listed.Aliases,
		RoomType:         listed.RoomType,
		RoomVersion:      room.RoomVersion,
	}
	summary.JoinRule, summary.AllowedRoomIDs, err = s.joinRuleAndAllowed(ctx, roomID)
	if err != nil {
		return RoomSummary{}, err
	}
	summary.AvatarURL, err = s.avatarURL(ctx, roomID)
	return summary, err
}

// joinRuleAndAllowed reads the room's join rule and, when it restricts joining, the rooms
// the restriction names.
func (s *Service) joinRuleAndAllowed(ctx context.Context, roomID string) (string, []string, error) {
	entry, err := s.state.Get(ctx, roomID, eventauth.TypeJoinRules, "")
	if err != nil {
		if notFound(err) {
			return "invite", nil, nil
		}
		return "", nil, fmt.Errorf("rooms: cannot load the room's join rule: %w", err)
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return "", nil, err
	}
	content, _ := event["content"].(map[string]any)
	rule, _ := content["join_rule"].(string)
	if rule == "" {
		rule = "invite"
	}
	if rule != "restricted" && rule != "knock_restricted" {
		return rule, nil, nil
	}
	list := []string{}
	for _, item := range anySlice(content["allow"]) {
		allow, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := allow["type"].(string)
		if kind != "" && kind != "m.room_membership" {
			continue
		}
		if named, ok := allow["room_id"].(string); ok && named != "" {
			list = append(list, named)
		}
	}
	if len(list) == 0 {
		return rule, nil, nil
	}
	return rule, list, nil
}

// avatarURL reads the room's m.room.avatar URL.
func (s *Service) avatarURL(ctx context.Context, roomID string) (string, error) {
	entry, err := s.state.Get(ctx, roomID, "m.room.avatar", "")
	if err != nil {
		if notFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("rooms: cannot load the room's avatar: %w", err)
	}
	event, err := s.eventJSON(ctx, entry.EventID)
	if err != nil {
		return "", err
	}
	content, _ := event["content"].(map[string]any)
	url, _ := content["url"].(string)
	return url, nil
}
