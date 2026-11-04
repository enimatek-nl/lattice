package matrix

import (
	"encoding/json"
	"fmt"
)

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
	TypeTombstone        = "m.room.tombstone"
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
	TypeThirdPartyInvite = "m.room.third_party_invite"
	TypeServerACL        = "m.room.server_acl"
	TypePolicyServerACL  = "m.room.policy_server_acl"
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
	// Hashes and Signatures are how an event proves who put it in the room and that
	// nothing in it changed on the way. A client normally ignores them; a bridge, a
	// key-sharing dialog, or a user auditing a room cannot, and a server that never
	// passes them on makes those checks impossible.
	Hashes     Object `json:"hashes,omitempty"`
	Signatures Object `json:"signatures,omitempty"`
	// Stored is the event as its sender signed it, when the server has that copy. A
	// client is served from it rather than from the fields above, because a signature
	// is made over one particular set of properties: an event rebuilt from columns
	// carries the same story in different words and fails its own hash.
	Stored RawMessage `json:"-"`
}

// MarshalJSON serves an event in the form it was signed in when the server holds that
// copy, and in the form of its fields otherwise.
//
// The distinction is not cosmetic. The hash and signature of an event cover the
// properties of the event as its origin server wrote them, so a homeserver that answers
// with a reconstruction hands out an event that does not verify against its own hash -
// and a client that checks, or a bridge that relays, has to decide which copy to
// believe. What is served is therefore the stored event, with the id added (an event in
// room version 3 and later does not carry its own id; a client still needs one) and with
// the unsigned properties this server chose to tell the client.
func (e Event) MarshalJSON() ([]byte, error) {
	if len(e.Stored) == 0 {
		type served Event
		return json.Marshal(served(e))
	}
	decoded, err := DecodeCanonical(e.Stored)
	if err != nil {
		return nil, fmt.Errorf("the stored event cannot be decoded: %w", err)
	}
	object, ok := decoded.(Object)
	if !ok {
		return nil, fmt.Errorf("the stored event is not an object")
	}
	if e.ID != "" {
		object["event_id"] = e.ID
	}
	if e.RoomID != "" {
		object["room_id"] = e.RoomID
	}
	if len(e.Unsigned) > 0 {
		object["unsigned"] = e.Unsigned
	} else {
		delete(object, "unsigned")
	}
	return json.Marshal(object)
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
	// Extra carries the keys a client put in the body of a join, knock or invite
	// that this server does not name itself. They belong to the client: a bridge
	// that says what it is in its own member event, or a relay that tags why it
	// joined, has nowhere else to say it. They are stored and served back.
	Extra Object `json:"-"`
}

// MarshalJSON writes the named keys and then the client's own keys, which cannot
// replace membership: an m.room.member event whose membership is not one of the
// five defined values is not a member event, whatever else it says.
func (m MemberContent) MarshalJSON() ([]byte, error) {
	type named MemberContent
	body, err := json.Marshal(named(m))
	if err != nil {
		return nil, err
	}
	if len(m.Extra) == 0 {
		return body, nil
	}
	var content Object
	if err := json.Unmarshal(body, &content); err != nil {
		return nil, err
	}
	for key, value := range m.Extra {
		if key == "membership" {
			continue
		}
		content[key] = value
	}
	return json.Marshal(content)
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
	RoomID string `json:"room_id"`
	// EventID is the last event of the old room at the moment of the upgrade. Room version
	// 12 rooms leave it out: their event ids are hashes this server computed, and a peer that
	// did not see the old room cannot check one - so the room is what carries the history,
	// not a claim about its last event.
	EventID string `json:"event_id,omitempty"`
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
//
// It is decoded by hand rather than by encoding/json because the allow list is a place where
// servers put things that are not rules at all: a bare string, an object with no room, the whole
// key replaced by a scalar. Those are not a client's error to make - the room is stating its
// rules - and refusing the event would let one bad sentence lock everyone out of the room. Each
// unusable entry is dropped, which is also what the tests of the other implementations expect: a
// room whose allow list carries nothing usable is a room that needs an invite.
type JoinRulesContent struct {
	JoinRule      string      `json:"join_rule"`
	Allow         []AllowRule `json:"allow,omitempty"`
	RequireAppsvc bool        `json:"m.restricted_allow_appservice_implicit,omitempty"`
}

// AllowRule describes a room based restriction for restricted join rules.
//
// Type names the kind of restriction. The specification spells the room-shaped one "m.space", and
// the implementations that grew the feature first spelled it "m.room_membership"; both mean the
// same thing - this join is authorised by membership of that room - so both are accepted and the
// distinction is not kept.
type AllowRule struct {
	Type   string `json:"type,omitempty"`
	RoomID string `json:"room_id,omitempty"`
}

// JoinRuleRoomMembership is the allow rule type that means "joined to this room".
const (
	AllowRuleSpace          = "m.space"
	AllowRuleRoomMembership = "m.room_membership"
)

// UnmarshalJSON reads a join rules event, keeping the rules that can be acted on.
func (c *JoinRulesContent) UnmarshalJSON(data []byte) error {
	var raw Object
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.JoinRule, _ = raw["join_rule"].(string)
	if flag, ok := raw["m.restricted_allow_appservice_implicit"].(bool); ok {
		c.RequireAppsvc = flag
	}
	entries, ok := raw["allow"].([]any)
	if !ok {
		return nil
	}
	for _, entry := range entries {
		rule, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		roomID, _ := rule["room_id"].(string)
		if !ValidRoomID(roomID) {
			continue
		}
		kind, _ := rule["type"].(string)
		switch kind {
		case AllowRuleSpace, AllowRuleRoomMembership:
		default:
			continue
		}
		c.Allow = append(c.Allow, AllowRule{Type: AllowRuleRoomMembership, RoomID: roomID})
	}
	return nil
}

// GrantsAccessTo reports whether the allow list admits a user given the rooms they are joined to.
func (c JoinRulesContent) GrantsAccessTo(joined func(roomID string) bool) bool {
	for _, rule := range c.Allow {
		if rule.Type == AllowRuleRoomMembership && joined(rule.RoomID) {
			return true
		}
	}
	return false
}

// HistoryVisibilityContent is the content of an m.room.history_visibility event.
type HistoryVisibilityContent struct {
	HistoryVisibility string `json:"history_visibility"`
}

// GuestAccessContent is the content of an m.room.guest_access event.
type GuestAccessContent struct {
	GuestAccess string `json:"guest_access"`
}

// The guest access levels an m.room.guest_access event can name. "can_connect" is the
// pre-spec spelling of "can_join" and is still read, because a room created by an early
// Synapse says that and nothing else.
const (
	GuestAccessCanJoin   = "can_join"
	GuestAccessForbidden = "forbidden"
	guestAccessLegacy    = "can_connect"
)

// GuestAccessAllows reports whether a guest access value lets a guest in.
func GuestAccessAllows(value string) bool {
	return value == GuestAccessCanJoin || value == guestAccessLegacy
}

// CanonicalAliasContent is the content of an m.room.canonical_alias event.
type CanonicalAliasContent struct {
	Alias      string   `json:"alias,omitempty"`
	AltAliases []string `json:"alt_aliases,omitempty"`
}

// PowerLevelsContent is the content of an m.room.power_levels event.
type PowerLevelsContent struct {
	// The levels are written out rather than left implicit. The specification lets a
	// reader supply defaults, but a level of 0 disappears from JSON when it is
	// optional, and a client that shows "who can invite" from the stored event reads
	// a hole where an answer was.
	Invite       int64 `json:"invite"`
	Kick         int64 `json:"kick"`
	Ban          int64 `json:"ban"`
	Redact       int64 `json:"redact"`
	SendDefault  int64 `json:"events_default"`
	StateDefault int64 `json:"state_default"`
	UsersDefault int64 `json:"users_default"`
	// Events names per-event levels. An empty object is written rather than
	// dropped: it says "no event has a level of its own", which is an answer, while
	// a missing key leaves a client showing an empty table.
	Events        map[string]int64 `json:"events"`
	Users         map[string]int64 `json:"users,omitempty"`
	Notifications map[string]int64 `json:"notifications,omitempty"`
}

// TombstoneContent is the content of an m.room.tombstone event: the marker that
// a room has been replaced, and where to go instead.
type TombstoneContent struct {
	Body            string `json:"body"`
	ReplacementRoom string `json:"replacement_room"`
	Reason          string `json:"reason,omitempty"`
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
