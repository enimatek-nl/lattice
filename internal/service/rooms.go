package service

import (
	"context"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// DefaultRoomVersion is the room version this homeserver creates and
// advertises as the default.
const DefaultRoomVersion = "11"

// SupportedRoomVersions lists the room versions this server understands.
var SupportedRoomVersions = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}

// Creation presets understood by createRoom.
const (
	PresetPrivateChat        = "private_chat"
	PresetTrustedPrivateChat = "trusted_private_chat"
	PresetPublicChat         = "public_chat"
)

// CreateRoomParams captures the supported fields of a createRoom request.
type CreateRoomParams struct {
	Creator                   string
	Alias                     string
	Name                      string
	Topic                     string
	Preset                    string
	Visibility                string
	JoinRule                  string
	HistoryVisibility         string
	GuestAccess               string
	RoomVersion               string
	CreationContent           matrix.Object
	PowerLevelContentOverride matrix.Object
	InitialStates             []matrix.Event
}

// CreateRoom creates a room and its initial state, returning the stored room.
func (s *Service) CreateRoom(ctx context.Context, params CreateRoomParams) (*persist.Room, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	roomID := matrix.NewRoomID(s.serverName)
	version := params.RoomVersion
	if version == "" {
		version = DefaultRoomVersion
	}
	if !versionSupported(version) {
		return nil, matrix.NewError(matrix.ErrCodeUnsupportedRoomVer, 400,
			"room version "+version+" is not supported by this homeserver")
	}
	params.RoomVersion = version

	preset := s.presetDefaults(params)

	room := &persist.Room{
		ID:          roomID,
		Creator:     params.Creator,
		RoomVersion: version,
		JoinRule:    preset.JoinRule,
	}
	if err := s.db.RoomRepo.Create(room); err != nil {
		if persist.IsDuplicate(err) {
			return nil, matrix.ErrConflict(matrix.ErrCodeUnknown, "room already exists")
		}
		return nil, matrix.ErrInternal(err)
	}

	if params.Alias != "" {
		if _, err := s.CreateAlias(ctx, params.Alias, roomID, params.Creator); err != nil {
			return nil, err
		}
	}

	if err := s.writeInitialState(ctx, roomID, params, preset); err != nil {
		return nil, err
	}

	visibility := params.Visibility
	if visibility == "" {
		visibility = "private"
		if preset.JoinRule == matrix.JoinRulesPublic {
			visibility = "public"
		}
	}
	if err := s.db.VisibilityRepo.Set(roomID, visibility); err != nil {
		return nil, matrix.ErrInternal(err)
	}
	return room, nil
}

// Preset holds the defaults derived from a creation preset plus explicit
// overrides.
type Preset struct {
	JoinRule          string
	HistoryVisibility string
	GuestAccess       string
}

// presetDefaults resolves join rule, history visibility and guest access from
// the preset and any explicit overrides.
func (s *Service) presetDefaults(params CreateRoomParams) Preset {
	preset := Preset{
		JoinRule:          matrix.JoinRulesInvite,
		HistoryVisibility: matrix.HistoryShared,
		GuestAccess:       "can_connect",
	}
	switch params.Preset {
	case PresetPublicChat:
		preset.JoinRule = matrix.JoinRulesPublic
		preset.HistoryVisibility = matrix.HistoryShared
		preset.GuestAccess = "can_connect"
	case PresetPrivateChat, PresetTrustedPrivateChat:
		preset.JoinRule = matrix.JoinRulesInvite
		preset.HistoryVisibility = matrix.HistoryShared
		preset.GuestAccess = "can_connect"
	}
	if params.JoinRule != "" {
		preset.JoinRule = params.JoinRule
	}
	if params.HistoryVisibility != "" {
		preset.HistoryVisibility = params.HistoryVisibility
	}
	if params.GuestAccess != "" {
		preset.GuestAccess = params.GuestAccess
	}
	return preset
}

// writeInitialState appends the state events every new room starts with. The
// order matches the specification: create, membership, power levels, join
// rules, then the informational events.
func (s *Service) writeInitialState(ctx context.Context, roomID string, params CreateRoomParams, preset Preset) error {
	creation := matrix.Object{}
	for k, v := range params.CreationContent {
		creation[k] = v
	}
	creation["creator"] = params.Creator
	creation["room_version"] = params.RoomVersion
	if _, ok := creation["m.federate"]; !ok {
		creation["m.federate"] = false
	}
	if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeCreate, creation); err != nil {
		return err
	}

	member := matrix.MemberContent{
		Membership:  matrix.MembershipJoin,
		DisplayName: s.displayName(ctx, params.Creator),
		AvatarURL:   s.avatarURL(ctx, params.Creator),
	}
	if err := s.appendMember(ctx, roomID, params.Creator, params.Creator, member); err != nil {
		return err
	}

	levels := matrix.PowerLevelsContent{
		Invite:        PowerDefaultInvite,
		Kick:          PowerDefaultKick,
		Ban:           PowerDefaultBan,
		Redact:        PowerDefaultRedact,
		SendDefault:   PowerDefaultMessage,
		UsersDefault:  PowerDefaultUser,
		Events:        map[string]int64{},
		Users:         map[string]int64{params.Creator: PowerAdmin},
		Notifications: map[string]int64{"room_char": 50},
	}
	override := matrix.MustParseObject(matrix.Encode(levels))
	for k, v := range params.PowerLevelContentOverride {
		override[k] = v
	}
	if err := s.appendState(ctx, roomID, params.Creator, matrix.TypePowerLevels, override); err != nil {
		return err
	}

	if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeJoinRules,
		matrix.Object{"join_rule": preset.JoinRule}); err != nil {
		return err
	}
	if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeHistoryVis,
		matrix.Object{"history_visibility": preset.HistoryVisibility}); err != nil {
		return err
	}
	if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeGuestAccess,
		matrix.Object{"guest_access": preset.GuestAccess}); err != nil {
		return err
	}
	if params.Name != "" {
		if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeName,
			matrix.Object{"name": params.Name}); err != nil {
			return err
		}
	}
	if params.Topic != "" {
		if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeTopic,
			matrix.Object{"topic": params.Topic}); err != nil {
			return err
		}
	}
	if params.Alias != "" {
		if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeCanonicalAlias,
			matrix.Object{"alias": params.Alias}); err != nil {
			return err
		}
	}

	for _, ev := range params.InitialStates {
		if ev.Type == matrix.TypeCreate {
			continue
		}
		content := matrix.MustParseObject(ev.Content)
		key := ""
		if ev.StateKey != nil {
			key = *ev.StateKey
		}
		if err := s.appendStateKey(ctx, roomID, params.Creator, ev.Type, key, content); err != nil {
			return err
		}
	}
	return nil
}

// appendState appends a state event with an empty state key.
func (s *Service) appendState(ctx context.Context, roomID, sender, typ string, content matrix.Object) error {
	return s.appendStateKey(ctx, roomID, sender, typ, "", content)
}

// appendStateKey appends a state event with an explicit state key.
func (s *Service) appendStateKey(ctx context.Context, roomID, sender, typ, stateKey string, content matrix.Object) error {
	_, err := s.AppendEvent(ctx, &EventDraft{
		RoomID:   roomID,
		Type:     typ,
		StateKey: StateKeyOf(stateKey),
		Sender:   sender,
		Content:  matrix.Encode(content),
	})
	return err
}

// appendMember appends an m.room.member event for target.
func (s *Service) appendMember(ctx context.Context, roomID, sender, target string, content matrix.MemberContent) error {
	_, err := s.AppendEvent(ctx, &EventDraft{
		RoomID:   roomID,
		Type:     matrix.TypeMember,
		StateKey: StateKeyOf(target),
		Sender:   sender,
		Content:  matrix.Encode(content),
	})
	return err
}

// versionSupported reports whether the server can create a room version.
func versionSupported(version string) bool {
	for _, v := range SupportedRoomVersions {
		if v == version {
			return true
		}
	}
	return false
}

// roomName returns the m.room.name of a room, if set.
func (s *Service) roomName(ctx context.Context, roomID string) string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeName, "")
	if err != nil {
		return ""
	}
	var content matrix.NameContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return ""
	}
	return content.Name
}

// roomTopic returns the m.room.topic of a room, if set.
func (s *Service) roomTopic(ctx context.Context, roomID string) string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeTopic, "")
	if err != nil {
		return ""
	}
	var content matrix.TopicContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return ""
	}
	return content.Topic
}

// roomAlias returns the canonical alias of a room, if set.
func (s *Service) roomAlias(ctx context.Context, roomID string) string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeCanonicalAlias, "")
	if err != nil {
		return ""
	}
	var content matrix.CanonicalAliasContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return ""
	}
	if content.Alias != "" {
		return content.Alias
	}
	rows, err := s.db.AliasRepo.ListForRoom(roomID)
	if err != nil || len(rows) == 0 {
		return ""
	}
	return rows[0].Alias
}

// roomAvatarURL returns the m.room.avatar of a room, if set.
func (s *Service) roomAvatarURL(ctx context.Context, roomID string) string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeAvatar, "")
	if err != nil {
		return ""
	}
	var content matrix.Object
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return ""
	}
	url, _ := content["url"].(string)
	return url
}

// normaliseAlias validates an alias and rewrites it to this server's domain
// when the client supplied a bare localpart.
func (s *Service) normaliseAlias(alias string) (matrix.Alias, error) {
	if alias == "" {
		return "", matrix.ErrInvalidRequest("an alias is required")
	}
	if !strings.HasPrefix(alias, "#") {
		alias = "#" + alias
	}
	local := matrix.Alias(alias).Localpart()
	domain := matrix.Alias(alias).Domain()
	if domain == "" {
		alias = "#" + local + ":" + s.serverName
		domain = s.serverName
	}
	if domain != s.serverName {
		return "", matrix.ErrForbidden("cannot create aliases on other servers")
	}
	if !matrix.ValidLocalpart(local) {
		return "", matrix.ErrInvalidParam("invalid alias localpart %q", local)
	}
	if !matrix.Alias(alias).Valid() {
		return "", matrix.ErrInvalidParam("invalid alias %q", alias)
	}
	return matrix.Alias(alias), nil
}
