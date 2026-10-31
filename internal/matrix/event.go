package matrix

import "encoding/json"

// Well known event types used by the client-server API.
const (
	TypeCreate           = "m.room.create"
	TypeMember           = "m.room.member"
	TypeName             = "m.room.name"
	TypeTopic            = "m.room.topic"
	TypeAvatar           = "m.room.avatar"
	TypePowerLevels      = "m.room.power_levels"
	TypeJoinRules        = "m.room.join_rules"
	TypeHistoryVis       = "m.room.history_visibility"
	TypeGuestAccess      = "m.room.guest_access"
	TypeCanonicalAlias   = "m.room.canonical_alias"
	TypeRedaction        = "m.room.redaction"
	TypeMessage          = "m.room.message"
	TypeEncrypted        = "m.room.encrypted"
	TypeEncryption       = "m.room.encryption"
	TypeReaction         = "m.reaction"
	TypeSticker          = "m.sticker"
	TypeTyping           = "m.typing"
	TypeReceipt          = "m.receipt"
	TypePresence         = "m.presence"
	TypeTag              = "m.tag"
	TypeDirect           = "m.direct"
	TypeSpaceChild       = "m.space.child"
	TypeSpaceParent      = "m.space.parent"
	TypeCallInvite       = "m.call.invite"
	TypePollStart        = "m.poll.start"
	TypeRoomKey          = "m.room_key"
	TypeRoomKeyRequest   = "m.room_key_request"
	TypeForwardedRoomKey = "m.forwarded_room_key"
	TypeKeyVerification  = "m.key.verification.request"
	TypeDummy            = "m.dummy"
	TypeFullyRead        = "m.fully_read"
	TypeTagAccountData   = "m.tag"
	TypeIgnoredUserList  = "m.ignored_user_list"
	TypeSpaceOrder       = "m.space_order"
)

// Membership values for m.room.member events.
const (
	MembershipJoin   = "join"
	MembershipInvite = "invite"
	MembershipLeave  = "leave"
	MembershipBan    = "ban"
	MembershipKnock  = "knock"
)

// Relation types carried by the m.relates_to relationship.
const (
	RelAnnotation = "m.annotation"
	RelThread     = "m.thread"
	RelReplace    = "m.replace"
	RelReply      = "m.in_reply_to"
)

// History visibility values.
const (
	HistoryWorldReadable     = "world_readable"
	HistoryShared            = "shared"
	HistoryInvited           = "invited"
	HistoryJoined            = "joined"
	JoinRulesPublic          = "public"
	JoinRulesKnock           = "knock"
	JoinRulesInvite          = "invite"
	JoinRulesRestricted      = "restricted"
	JoinRulesKnockRestricted = "knock_restricted"
)

// Event is the wire representation of a Matrix event (a PDU subset that is
// good enough for a client-server compatible homeserver).
type Event struct {
	RoomID         string     `json:"room_id,omitempty"`
	Type           string     `json:"type"`
	StateKey       *string    `json:"state_key,omitempty"`
	Sender         string     `json:"sender"`
	OriginServerTS int64      `json:"origin_server_ts"`
	ID             string     `json:"event_id"`
	Content        RawMessage `json:"content"`
	Unsigned       Object     `json:"unsigned,omitempty"`

	// Fields that clients echo but mostly ignore.
	PrevEvents []string `json:"prev_events,omitempty"`
	AuthEvents []string `json:"auth_events,omitempty"`
	Depth      int64    `json:"depth,omitempty"`
	Origin     string   `json:"origin,omitempty"`
	Redacts    string   `json:"redacts,omitempty"`
}

// IsState reports whether the event is a state event.
func (e *Event) IsState() bool { return e.StateKey != nil }

// MemberContent is the content of an m.room.member event.
type MemberContent struct {
	Membership       string `json:"membership"`
	DisplayName      string `json:"displayname,omitempty"`
	AvatarURL        string `json:"avatar_url,omitempty"`
	Reason           string `json:"reason,omitempty"`
	ThirdPartyInvite Object `json:"third_party_invite,omitempty"`
}

// CreateContent is the content of an m.room.create event.
type CreateContent struct {
	Creator             string       `json:"creator"`
	RoomVersion         string       `json:"room_version,omitempty"`
	Predecessor         *Predecessor `json:"predecessor,omitempty"`
	Type                string       `json:"type,omitempty"`
	Federate            *bool        `json:"m.federate,omitempty"`
	RoomVersionFallback bool         `json:"-"`
}

// Predecessor links an upgraded room to its predecessor.
type Predecessor struct {
	RoomID  string `json:"room_id"`
	EventID string `json:"event_id"`
}

// NameContent is the content of an m.room.name event.
type NameContent struct {
	Name string `json:"name,omitempty"`
}

// TopicContent is the content of an m.room.topic event.
type TopicContent struct {
	Topic string `json:"topic,omitempty"`
}

// JoinRulesContent is the content of an m.room.join_rules event.
type JoinRulesContent struct {
	JoinRule      string      `json:"join_rule"`
	Allow         []AllowRule `json:"allow,omitempty"`
	RequireAppsvc bool        `json:"m.restricted_allow_appservice_implicit,omitempty"`
}

// AllowRule describes a room based restriction for restricted join rules.
type AllowRule struct {
	Type   string `json:"type,omitempty"`
	RoomID string `json:"room_id,omitempty"`
}

// HistoryVisibilityContent is the content of an m.room.history_visibility event.
type HistoryVisibilityContent struct {
	HistoryVisibility string `json:"history_visibility"`
}

// GuestAccessContent is the content of an m.room.guest_access event.
type GuestAccessContent struct {
	GuestAccess string `json:"guest_access"`
}

// CanonicalAliasContent is the content of an m.room.canonical_alias event.
type CanonicalAliasContent struct {
	Alias      string   `json:"alias,omitempty"`
	AltAliases []string `json:"alt_aliases,omitempty"`
}

// PowerLevelsContent is the content of an m.room.power_levels event.
type PowerLevelsContent struct {
	Invite        int64            `json:"invite,omitempty"`
	Kick          int64            `json:"kick,omitempty"`
	Ban           int64            `json:"ban,omitempty"`
	Redact        int64            `json:"redact,omitempty"`
	SendDefault   int64            `json:"events_default,omitempty"`
	UsersDefault  int64            `json:"users_default,omitempty"`
	Events        map[string]int64 `json:"events,omitempty"`
	Users         map[string]int64 `json:"users,omitempty"`
	Notifications map[string]int64 `json:"notifications,omitempty"`
}

// RedactionContent is the content of an m.room.redaction event.
type RedactionContent struct {
	Reason string `json:"reason,omitempty"`
	// Redacts is filled in for the redaction event itself.
	Redacts string `json:"redacts,omitempty"`
}

// MessageContent is the content of an m.room.message event.
type MessageContent struct {
	MsgType string `json:"msgtype,omitempty"`
	Body    string `json:"body,omitempty"`
}

// DecodeMemberContent decodes an m.room.member content block.
func DecodeMemberContent(raw RawMessage) (MemberContent, error) {
	var c MemberContent
	err := json.Unmarshal(raw, &c)
	return c, err
}

// DecodeContent decodes any content block into the given target type.
func DecodeContent(raw RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

// Encode marshals a content block, tolerating nil input by returning an empty
// JSON object.
func Encode(v any) RawMessage {
	if v == nil {
		return RawMessage("{}")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return RawMessage("{}")
	}
	return raw
}
