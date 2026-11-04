package matrix

import "encoding/json"

// This file holds the wire shapes of simplified sliding sync (MSC4186): a sync in which the
// client names the rooms it wants and how much of each it wants, and the server answers with
// only what has changed on that connection.
//
// The one rule that shapes every type here is that an omitted field is a *statement*: when a
// room is sent as a delta, a field left out means "unchanged", and when a room is sent for the
// first time it means "not set". So the optional fields are pointers or raw JSON, and the code
// that fills them decides which of the two it means rather than letting the encoder decide.

// SlidingSyncRequest is the body of POST /_matrix/client/v4/sync.
type SlidingSyncRequest struct {
	// ConnID names this connection to the server. The empty string, and an absent field,
	// are the same connection: the MSC lets a client run exactly one loop under a name it
	// did not bother to choose.
	ConnID string `json:"conn_id,omitempty"`
	// Pos is the position the previous response on this connection answered with. Its
	// absence is what makes this request an initial one.
	Pos string `json:"pos,omitempty"`
	// Timeout is how long the server may wait for something to report, in milliseconds.
	// A pointer distinguishes "0, answer now" from "the client said nothing".
	Timeout *int `json:"timeout,omitempty"`
	// SetPresence is the /v3/sync parameter of the same name, moved into the body.
	SetPresence string `json:"set_presence,omitempty"`
	// Lists maps a client-chosen name to a window over the user's room list.
	Lists map[string]SlidingSyncList `json:"lists,omitempty"`
	// RoomSubscriptions maps a room id to the config a room is reported under when the
	// client asks for that room by name.
	RoomSubscriptions map[string]SlidingSyncSubscription `json:"room_subscriptions,omitempty"`
	// Extensions carries the sections that are not rooms - to device messages, the
	// end-to-end key sections, typing, receipts, account data. The server decodes each one
	// by name and ignores what it does not know how to read.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

// SlidingSyncList is one entry of the request's "lists": a window over the rooms the user is
// in, plus the filters that decide what is in that window and the config rooms in it are
// reported under.
type SlidingSyncList struct {
	// TimelineLimit caps how many of the room's most recent events are sent. Required,
	// and a server may cap it further.
	TimelineLimit int `json:"timeline_limit"`
	// RequiredState says which of the room's current state to send.
	RequiredState SlidingRequiredState `json:"required_state"`
	// Range indexes into the filtered list, inclusive and from 0. Its absence means the
	// whole list.
	Range []int `json:"range,omitempty"`
	// Filters narrows the list before the range is applied.
	Filters *SlidingRoomFilter `json:"filters,omitempty"`
}

// SlidingSyncSubscription is the config a room the client named is reported under.
type SlidingSyncSubscription struct {
	TimelineLimit int                  `json:"timeline_limit"`
	RequiredState SlidingRequiredState `json:"required_state"`
}

// SlidingRequiredState describes the slice of a room's current state to return.
type SlidingRequiredState struct {
	// Include is the set to return; an element with no type or no state key is a wildcard.
	Include []SlidingStateElement `json:"include,omitempty"`
	// Exclude filters out of that set. Lazy loaded memberships are not filtered by it.
	Exclude []SlidingStateElement `json:"exclude,omitempty"`
	// LazyMembers sends the memberships a client needs to render what it is being shown,
	// instead of the room's whole membership list.
	LazyMembers bool `json:"lazy_members,omitempty"`
}

// The state-key tokens a client that grew up with the sliding sync proxy writes where a state
// key belongs. MSC4186 defines neither - it asks for lazy loading by the lazy_members flag and
// for one's own membership by naming it - but the proxy-era clients are in the wild, and no
// room can hold a state key beginning with "$", so reading the two costs nothing.
const (
	// SlidingStateKeyMe means the requesting user's own membership event.
	SlidingStateKeyMe = "$ME"
	// SlidingStateKeyLazy means "the memberships of this room, lazily".
	SlidingStateKeyLazy = "$LAZY"
)

// SlidingStateElement is one type/state key pair. An omitted half matches everything on that
// axis, which is why both are pointers.
type SlidingStateElement struct {
	Type     *string `json:"type,omitempty"`
	StateKey *string `json:"state_key,omitempty"`
}

// SlidingRoomFilter narrows a list. Each filter is a question about the room, and a room in
// the list has to answer every one of them the way the filter asks.
type SlidingRoomFilter struct {
	// IsDM asks whether the room appears in the user's m.direct account data.
	IsDM *bool `json:"is_dm,omitempty"`
	// Spaces keeps rooms directly named by an m.space.child of a joined space the client
	// names. Subspaces are not walked: it is the client's job to name them.
	Spaces []string `json:"spaces,omitempty"`
	// IsEncrypted asks whether the room has an m.room.encryption event.
	IsEncrypted *bool `json:"is_encrypted,omitempty"`
	// IsInvited asks whether the user is currently invited.
	IsInvited *bool `json:"is_invited,omitempty"`
	// RoomTypes keeps rooms whose m.room.create names one of these types; a null entry
	// means "a room with no type".
	RoomTypes []*string `json:"room_types,omitempty"`
	// NotRoomTypes excludes them, and wins over RoomTypes.
	NotRoomTypes []*string `json:"not_room_types,omitempty"`
	// Tags keeps rooms carrying one of these tags.
	Tags []string `json:"tags,omitempty"`
	// NotTags excludes rooms carrying one, and wins over Tags.
	NotTags []string `json:"not_tags,omitempty"`
}

// SlidingSyncResponse is the answer to a sliding sync request.
type SlidingSyncResponse struct {
	// Pos is the token for the next request on this connection. It also works as a batch
	// token in /messages and /relations.
	Pos string `json:"pos"`
	// Lists reports each requested list; the count is the number of rooms matching its
	// filters, whatever the range asked for.
	Lists map[string]SlidingListResult `json:"lists,omitempty"`
	// Rooms holds the rooms that are new to this connection or have changed since it last
	// saw them.
	Rooms map[string]SlidingRoom `json:"rooms,omitempty"`
	// Extensions holds the sections that are not rooms, keyed by the name the client asked
	// for them under.
	Extensions map[string]any `json:"extensions,omitempty"`
}

// SlidingListResult is what a list reports about itself: how many rooms it holds. A client
// uses that count to decide how far to grow its window.
type SlidingListResult struct {
	Count int `json:"count"`
}

// SlidingRoom is one room in the response.
//
// Which fields appear depends on the user's membership: a room they are in (or have been in)
// is described by the joined half of this struct, a room they are merely invited to, knocked
// on, or have rejected an invite to only by its stripped state.
type SlidingRoom struct {
	// BumpStamp sorts rooms by their last real activity, and is opaque to the client: this
	// server uses the stream position of the newest event of the kinds the MSC counts.
	BumpStamp *int64 `json:"bump_stamp,omitempty"`
	// Membership is the user's membership of the room, omitted when they have none.
	Membership string `json:"membership,omitempty"`
	// Lists names the lists this room matched; absent when the room is here only because
	// the client subscribed to it.
	Lists []string `json:"lists,omitempty"`

	// Name and Avatar are raw JSON because they have three states a string cannot hold:
	// unchanged (leave out), set (a JSON string), and removed (JSON null).
	Name   json.RawMessage `json:"name,omitempty"`
	Avatar json.RawMessage `json:"avatar,omitempty"`
	// Heroes are the members a client can name a room from. They are sent only when the
	// room has no name of its own.
	Heroes []Hero `json:"heroes,omitempty"`
	// IsDM reports the room's direct flag; a nil pointer means the field is unchanged.
	IsDM *bool `json:"is_dm,omitempty"`

	// Initial says this is the first time the connection has been shown this room, so an
	// omitted field means "not set" rather than "unchanged".
	Initial bool `json:"initial,omitempty"`
	// ExpandedTimeline says the timeline carries events older than the last time the room
	// was sent, because the client asked for a wider window.
	ExpandedTimeline bool `json:"expanded_timeline,omitempty"`

	// RequiredState carries the room's changed state. An entry may be a StateStub: state
	// that used to be there and is not.
	RequiredState []any `json:"required_state,omitempty"`
	// Timeline is the room's recent events, oldest last.
	Timeline []Event `json:"timeline,omitempty"`
	// PrevBatch pages backwards from the oldest event sent.
	PrevBatch string `json:"prev_batch,omitempty"`
	// Limited says there are events this response did not carry.
	Limited bool `json:"limited,omitempty"`
	// NumLive counts the events that are not history - the ones that happened since the
	// client's last request - which a client uses to decide whether to make a noise.
	NumLive *int `json:"num_live,omitempty"`

	JoinedCount  *int64 `json:"joined_count,omitempty"`
	InvitedCount *int64 `json:"invited_count,omitempty"`

	// StrippedState is all a room the user is not in can be described by: the state as it
	// was written into the invite or knock.
	StrippedState []StrippedStateEvent `json:"stripped_state,omitempty"`
}

// Hero is one member a room's name can be built from, with what the client needs to show them.
type Hero struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"displayname,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
}

// StateStub names a piece of state that has gone away. It has no content on purpose: the
// absence of that field is what tells a client to delete the entry it holds.
type StateStub struct {
	Type     string `json:"type"`
	StateKey string `json:"state_key"`
}
