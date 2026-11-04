package service

import (
	"context"
	"strings"

	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/power"
)

// DefaultRoomVersion is the room version this homeserver creates and
// advertises as the default.
//
// The specification recommends room version 12 as the default, and this server stays on
// 11 on purpose: room version 12 rooms are named by a hash of their create event, so an
// invite for one is only meaningful if the invite carries that create event in its
// stripped state (MSC4311, spec v1.16), and a server that defaults to 12 must then reject
// the invites of clients that do not send it. Defaulting to a version whose invites the
// wider federation half-sends would break rooms rather than protect them. Switching the
// default is a decision for a release, not for a step.
const DefaultRoomVersion = "11"

// SupportedRoomVersions lists the room versions this server understands.
var SupportedRoomVersions = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}

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
	// Invite lists the users the createRoom call invites. It is not used to invite them -
	// that happens after the room exists - but room version 12 reads it to decide who the
	// room's creators are, and that has to be settled before the create event is signed.
	Invite []string
}

// CreateRoom creates a room and its initial state, returning the stored room.
func (s *Service) CreateRoom(ctx context.Context, params CreateRoomParams) (*persist.Room, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	version := params.RoomVersion
	if version == "" {
		version = DefaultRoomVersion
	}
	if !s.versionSupported(version) {
		return nil, matrix.NewError(matrix.ErrCodeUnsupportedRoomVer, 400,
			"room version "+version+" is not supported by this homeserver")
	}
	params.RoomVersion = version

	preset := s.presetDefaults(params)

	// The create event content is built before the room exists, because in a room version
	// that hashes room ids the event decides what the room is called.
	creation, err := s.creationContent(params)
	if err != nil {
		return nil, err
	}

	roomID := matrix.NewRoomID(s.serverName)
	var create *pdu
	if eventcrypto.UsesHashedRoomIDs(version) {
		create, err = s.buildCreatePDU(ctx, params.Creator, creation, version)
		if err != nil {
			return nil, err
		}
		roomID, err = matrix.RoomIDFromEventID(create.eventID)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
	}

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
		// The alias is claimed after the room's state exists, because the rule "only a
		// member of a room may make an alias for it" is false for the creator until the
		// creator's own join event has been written. The conflict is still checked here,
		// so a taken alias does not leave a room behind with nothing pointing at it.
		if taken, err := s.db.AliasRepo.Get(params.Alias); err == nil {
			return nil, matrix.ErrConflict(matrix.ErrCodeAliasExists,
				"alias %s already points at room %s", params.Alias, taken.RoomID)
		} else if !persist.IsNotFound(err) {
			return nil, matrix.ErrInternal(err)
		}
	}

	if err := s.writeInitialState(ctx, roomID, params, preset, creation, create); err != nil {
		return nil, err
	}

	if params.Alias != "" {
		if _, err := s.CreateAlias(ctx, params.Alias, roomID, params.Creator); err != nil {
			return nil, err
		}
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

// creationContent assembles the content of a room's m.room.create event from what the
// caller asked for: the caller's own creation_content, plus the properties this server
// always writes.
//
// additional_creators is the property room version 12 adds, and it is validated here
// rather than left for the authorisation rule to refuse later: a client that mistyped a
// user id in a createRoom request wants a 400 naming the field, not a room that was
// created anyway and then refuses everything its second creator tries to do. The users a
// createRoom call invites become creators as well, which is what lets the people a room
// was opened for administer it from the first moment - and it is why a v12 room's create
// event can be handed to them as proof of what the room is.
func (s *Service) creationContent(params CreateRoomParams) (matrix.Object, error) {
	creation := matrix.Object{}
	for k, v := range params.CreationContent {
		creation[k] = v
	}
	creation["creator"] = params.Creator
	creation["room_version"] = params.RoomVersion
	if _, ok := creation["m.federate"]; !ok {
		// A room is federated unless its creator says otherwise: the specification's
		// create event makes m.federate default to true, and a server that defaults it to
		// false quietly un-federates every room it creates - the creator then has to know
		// to ask for federation, which is the opposite of how the feature is specified.
		creation["m.federate"] = true
	}

	privileged := eventcrypto.UsesHashedRoomIDs(params.RoomVersion)
	if raw, asked := creation["additional_creators"]; asked {
		if !privileged {
			delete(creation, "additional_creators")
			return creation, matrix.ErrInvalidRequest(
				"additional_creators is a property of room version 12 rooms, not %s rooms", params.RoomVersion)
		}
		creators, err := validAdditionalCreators(raw)
		if err != nil {
			return creation, err
		}
		creation["additional_creators"] = creators
	}
	if privileged && params.Preset == PresetTrustedPrivateChat && len(params.Invite) > 0 {
		// A trusted private chat is one the invited people are trusted in: before room
		// version 12 they were handed the administrator level for it, and in this version they
		// are made creators of the room instead, which is the stronger and the more honest
		// statement - they helped make it, so nothing in the room can take that back.
		// Other presets do not say that about their invitees, so an ordinary invite does not
		// quietly promote anybody.
		creators, _ := creation["additional_creators"].([]string)
		for _, user := range params.Invite {
			if !matrix.IsUserID(user) {
				return creation, matrix.ErrInvalidRequest("%s is not a user id", user)
			}
			if !containsString(creators, user) {
				creators = append(creators, user)
			}
		}
		creation["additional_creators"] = creators
	}
	return creation, nil
}

// validAdditionalCreators checks an additional_creators array the way room version 12's
// authorisation rule does: an array, of strings, each spelled as a user id.
func validAdditionalCreators(raw any) ([]string, error) {
	var entries []any
	switch typed := raw.(type) {
	case []any:
		entries = typed
	case []string:
		entries = make([]any, len(typed))
		for i, v := range typed {
			entries[i] = v
		}
	default:
		return nil, matrix.ErrInvalidRequest("additional_creators must be an array of user ids")
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		user, ok := entry.(string)
		if !ok {
			return nil, matrix.ErrInvalidRequest("every additional creator must be a user id, and %v is not a string", entry)
		}
		if !matrix.ValidUserID(user) {
			return nil, matrix.ErrInvalidRequest("%q is not a user id", user)
		}
		out = append(out, user)
	}
	return out, nil
}

// Preset holds the defaults derived from a creation preset plus explicit
// overrides. An empty guest access means the room gets no m.room.guest_access event
// at all, which is how a room says "nobody decided about guests".
type Preset struct {
	JoinRule          string
	HistoryVisibility string
	GuestAccess       string
}

// presetDefaults resolves join rule, history visibility and guest access from
// the preset and any explicit overrides.
//
// A public room gets no guest access event. Guests are not welcome in one by default,
// and writing an event to say so - with a value the specification does not use - is how
// this server's rooms came to differ from every other server's public rooms in what
// their state looks like.
func (s *Service) presetDefaults(params CreateRoomParams) Preset {
	preset := Preset{
		JoinRule:          matrix.JoinRulesInvite,
		HistoryVisibility: matrix.HistoryShared,
	}
	switch params.Preset {
	case PresetPublicChat:
		preset.JoinRule = matrix.JoinRulesPublic
		preset.HistoryVisibility = matrix.HistoryShared
	case PresetPrivateChat, PresetTrustedPrivateChat:
		preset.JoinRule = matrix.JoinRulesInvite
		preset.HistoryVisibility = matrix.HistoryShared
		preset.GuestAccess = matrix.GuestAccessCanJoin
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
//
// The create event content and, for a room version that hashes room ids, the finished
// create event come from the caller: such an event had to be signed before the room it
// names existed, so it was built in [Service.CreateRoom] and is stored here rather than
// rebuilt.
func (s *Service) writeInitialState(ctx context.Context, roomID string, params CreateRoomParams, preset Preset, creation matrix.Object, prebuiltCreate *pdu) error {
	if err := s.appendCreate(ctx, roomID, params.Creator, creation, prebuiltCreate); err != nil {
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
		StateDefault:  PowerDefaultState,
		UsersDefault:  PowerDefaultUser,
		Events:        map[string]int64{},
		Users:         map[string]int64{},
		Notifications: map[string]int64{"room_char": 50},
	}
	privileged := eventcrypto.UsesHashedRoomIDs(params.RoomVersion)
	if !privileged {
		// Before room version 12 a creator is an administrator like any other, so the room
		// says so in its own power levels event.
		levels.Users[params.Creator] = PowerAdmin
	}
	if privileged {
		// A room version 12 creator holds a level no power levels event can name, so naming
		// them here would be a level the authorisation rules refuse to read (rule 10.4
		// rejects the event outright). Ending the room belongs to the creators alone.
		levels.Events[matrix.TypeTombstone] = PowerTombstone
	}
	override := matrix.MustParseObject(matrix.Encode(levels))
	for k, v := range params.PowerLevelContentOverride {
		override[k] = v
	}
	if privileged {
		// The room says out loud that its levels name nobody. An absent users map and an
		// empty one mean the same thing here, but the point of a room version 12 room is that
		// its creators were deliberately left out of the event, and a reader - another server,
		// or whoever is auditing why a room refused an event - can only see that omission if
		// the field it is an omission from is there.
		if _, named := override["users"]; !named {
			override["users"] = map[string]int64{}
		}
	}
	if privileged {
		// A caller that overrides the levels may hand in a map that names a creator, which
		// is the one thing a room version 12 power levels event is not allowed to do. The
		// client that did it asked for a room that cannot hold its own first state event,
		// so the room is not created and the request is refused.
		if named := powerLevelsNamesUsers(override, power.CreatorsOf(creation, params.Creator, true)); len(named) > 0 {
			return matrix.ErrInvalidRequest(
				"room version 12 power levels cannot name its creators, and %s is one", strings.Join(named, ", "))
		}
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
	if preset.GuestAccess != "" {
		if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeGuestAccess,
			matrix.Object{"guest_access": preset.GuestAccess}); err != nil {
			return err
		}
	}
	if params.Alias != "" {
		// The event names the alias in full. A bare localpart is not an identifier:
		// a client, a public room list or another server cannot resolve it, and the
		// room would answer to a name nobody can use.
		alias, err := s.normaliseAlias(params.Alias)
		if err != nil {
			return err
		}
		if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeCanonicalAlias,
			matrix.Object{"alias": alias.String()}); err != nil {
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

	// The named parameters of the request come last, so they win over the state a
	// caller also handed in through initial_state. The specification puts the
	// explicit keys above the list rather than under it, and a client that sends
	// both - a room template with a topic the caller then edits - means the edit.
	if params.Name != "" {
		if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeName,
			matrix.Object{"name": params.Name}); err != nil {
			return err
		}
	}
	if params.Topic != "" {
		if err := s.appendState(ctx, roomID, params.Creator, matrix.TypeTopic,
			richTopic(params.Topic)); err != nil {
			return err
		}
	}
	return nil
}

// richTopic writes an m.room.topic content block the way the rich topics
// extension asks for: the plain topic, plus the same text as a formatted
// representation a client can render without guessing that a bare string is
// plain text. Both are written from one string, so they cannot drift apart.
func richTopic(topic string) matrix.Object {
	return matrix.Object{
		"topic": topic,
		"m.topic": matrix.Object{
			"m.text": []matrix.Object{{
				"mimetype": "text/plain",
				"body":     topic,
			}},
		},
	}
}

// appendState appends a state event with an empty state key.
func (s *Service) appendState(ctx context.Context, roomID, sender, typ string, content matrix.Object) error {
	return s.appendStateKey(ctx, roomID, sender, typ, "", content)
}

// appendCreate appends a room's create event, which a room version that hashes room ids
// has already had built, hashed and named by [Service.buildCreatePDU].
func (s *Service) appendCreate(ctx context.Context, roomID, sender string, content matrix.Object, prebuilt *pdu) error {
	_, err := s.AppendEvent(ctx, &EventDraft{
		RoomID:   roomID,
		Type:     matrix.TypeCreate,
		StateKey: StateKeyOf(""),
		Sender:   sender,
		Content:  matrix.Encode(content),
		prebuilt: prebuilt,
	})
	return err
}

// powerLevelsNamesUsers returns the users a power levels content object lists in its
// users map that the room names as creators.
func powerLevelsNamesUsers(content matrix.Object, creators []string) []string {
	users, ok := content["users"]
	if !ok {
		return nil
	}
	var named []string
	switch typed := users.(type) {
	case map[string]int64:
		for _, creator := range creators {
			if _, found := typed[creator]; found {
				named = append(named, creator)
			}
		}
	case map[string]any:
		for _, creator := range creators {
			if _, found := typed[creator]; found {
				named = append(named, creator)
			}
		}
	}
	return named
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
func (s *Service) versionSupported(version string) bool {
	for _, v := range s.roomVersions {
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
	if !matrix.ValidAliasLocalpart(local) {
		return "", matrix.ErrInvalidParam("invalid alias localpart %q", local)
	}
	if !matrix.Alias(alias).Valid() {
		return "", matrix.ErrInvalidParam("invalid alias %q", alias)
	}
	return matrix.Alias(alias), nil
}
