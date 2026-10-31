package matrix

// SyncResponse is the body of GET /_matrix/client/v3/sync.
type SyncResponse struct {
	NextBatch          string              `json:"next_batch"`
	Rooms              Rooms               `json:"rooms"`
	Presence           *Ephemeral          `json:"presence,omitempty"`
	AccountData        *Ephemeral          `json:"account_data,omitempty"`
	ToDevice           *Ephemeral          `json:"to_device,omitempty"`
	DeviceLists        *DeviceLists        `json:"device_lists,omitempty"`
	DeviceOneTimeCount *DeviceOneTimeCount `json:"device_one_time_keys_count,omitempty"`
	UnusedFallbackKeys []string            `json:"device_unused_fallback_key_types,omitempty"`
	Since              string              `json:"since,omitempty"`
	DeletedRooms       []string            `json:"rooms_offline,omitempty"`
}

// Rooms groups the three room sections of a sync response.
type Rooms struct {
	Join   map[string]*JoinedRoom  `json:"join,omitempty"`
	Invite map[string]*InvitedRoom `json:"invite,omitempty"`
	Leave  map[string]*LeftRoom    `json:"leave,omitempty"`
	Knock  map[string]*KnockedRoom `json:"knock,omitempty"`
}

// Ephemeral wraps a list of ephemeral events.
type Ephemeral struct {
	Events []Event `json:"events"`
}

// State is a set of state events, optionally with a prev_batch token.
type State struct {
	Events    []Event `json:"events"`
	PrevBatch string  `json:"prev_batch,omitempty"`
}

// Timeline is a window of recent events.
type Timeline struct {
	Events    []Event `json:"events"`
	Limited   bool    `json:"limited"`
	PrevBatch string  `json:"prev_batch,omitempty"`
}

// UnreadNotifications carries highlight and notification counts.
type UnreadNotifications struct {
	NotificationCount int64 `json:"notification_count,omitempty"`
	HighlightCount    int64 `json:"highlight_count,omitempty"`
}

// JoinedRoom is the /sync "join" section of a single room.
type JoinedRoom struct {
	Timeline            Timeline            `json:"timeline"`
	State               State               `json:"state"`
	InviteCount         int64               `json:"invite_count,omitempty"`
	UnreadNotifications UnreadNotifications `json:"unread_notifications"`
	UnreadThreadUnread  UnreadNotifications `json:"unread_thread_notifications,omitempty"`
	AccountData         *Ephemeral          `json:"account_data,omitempty"`
	Ephemeral           *Ephemeral          `json:"ephemeral,omitempty"`
	OrgMatrixE2E        *Ephemeral          `json:"org.matrix.msc3814.e2ee,omitempty"`
	PrevBatch           string              `json:"prev_batch,omitempty"`
	Unread              *int64              `json:"unread_notifications_count,omitempty"`

	// The room summary of spec revision v1.3 lives directly on the joined room.
	Heroes             []string `json:"m.heroes,omitempty"`
	JoinedMemberCount  *int64   `json:"m.joined_member_count,omitempty"`
	InvitedMemberCount *int64   `json:"m.invited_member_count,omitempty"`
}

// InvitedRoom is the /sync "invite" section of a single room.
type InvitedRoom struct {
	InviteState State `json:"invite_state"`
}

// KnockedRoom is the /sync "knock" section of a single room.
type KnockedRoom struct {
	KnockState State `json:"knock_state"`
}

// LeftRoom is the /sync "leave" section of a single room.
type LeftRoom struct {
	Timeline    Timeline   `json:"timeline"`
	State       State      `json:"state"`
	AccountData *Ephemeral `json:"account_data,omitempty"`
}

// DeviceLists reports members whose device lists changed. Both lists are always
// present when the section is included, which is what clients expect.
type DeviceLists struct {
	Changed []string `json:"changed"`
	Left    []string `json:"left"`
}

// DeviceOneTimeCount reports how many one-time keys the syncing device still
// has available, keyed by algorithm. Clients watch these counts to decide when
// to upload another batch of keys.
type DeviceOneTimeCount map[string]int64
