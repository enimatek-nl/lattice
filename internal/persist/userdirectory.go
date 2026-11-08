package persist

import (
	"context"
	"encoding/json"
	"sort"

	"gorm.io/gorm"
)

// The user directory is asked across rooms: "whom may this searcher see, and under which names do
// they appear". Every per-room read this server has answers about one room and checks the caller's
// standing in it, which is right for `GET /members` and wrong here — a directory search would then
// cost one authorised read per room, and the rooms it must read include ones the searcher is not in
// at all (the rooms whose join rule lets anyone in).
//
// So the queries live here, and nothing else does: what a room's members are called, and which
// rooms make their members visible to strangers. Deciding who may be told what — the part with the
// rules in it — is the userdir service's job, and keeping it out of this file is what lets that
// decision be read in one place.
//
// The directory is computed from the member events this server already holds rather than maintained
// beside them. A maintained table would be faster and would also be a second answer to "who is in
// this room", which has to be kept in step with the first one on every join, leave, kick, profile
// change and room this server has never heard of. Room state is the authority and stays the only
// one; the cost is that a search reads state, and that cost is documented in
// `.agent-work/BLOCKERS.md` with the rest of the SQLite ceilings.

// RoomMemberName is one person's appearance in one room, as that room's member event for them
// wrote it. Matrix puts a user's name in the room's state, so two rooms can hold two names for the
// same person and only one of them is that person's profile.
type RoomMemberName struct {
	RoomID string
	// UserID is the member event's state key: who the event is about, which is not its sender.
	UserID      string
	DisplayName string
	AvatarURL   string
}

// UserDirectoryRepo reads the facts a user directory search is built from.
type UserDirectoryRepo interface {
	// WithTx returns a repository bound to an open transaction.
	WithTx(tx *gorm.DB) UserDirectoryRepo

	// JoinedRoomIDs returns the rooms whose current member event for userID says join. It reads
	// the member state that names the user, so the work is proportional to the user's rooms and
	// not to the server's.
	JoinedRoomIDs(ctx context.Context, userID string) ([]string, error)

	// OpenRoomIDs returns the rooms this server holds whose members are visible to somebody who is
	// not in them: the ones whose join rule is `public`, and the ones whose history visibility is
	// `world_readable`. Those two are the specification's own words for the minimum set a user
	// directory may consider, and this returns exactly them — a room that is merely published in
	// the room directory is not one of them, because publishing a room says strangers may look at
	// it, not that every person in it may be named to strangers.
	OpenRoomIDs(ctx context.Context) ([]string, error)

	// JoinedNamesIn returns one row per (room, user) whose current member event in a listed room
	// says join, carrying the display name and avatar that event wrote. A user joined in two of
	// the rooms appears twice, because the two rooms can call them two different things and the
	// caller has to know which name came from where to decide whom it may be shown to.
	//
	// roomIDs is read in chunks: SQLite has a ceiling on how many parameters one statement may
	// carry, and a server with a few thousand public rooms would meet it.
	JoinedNamesIn(ctx context.Context, roomIDs []string) ([]RoomMemberName, error)
}

type userDirectoryRepo struct{ db *gorm.DB }

// NewUserDirectoryRepo returns the user-directory read model.
func NewUserDirectoryRepo(db *gorm.DB) UserDirectoryRepo { return &userDirectoryRepo{db: db} }

// WithTx implements [UserDirectoryRepo].
func (r *userDirectoryRepo) WithTx(tx *gorm.DB) UserDirectoryRepo {
	return &userDirectoryRepo{db: tx}
}

// roomStateRows is the one shape these reads have: a room, and the event that currently speaks for
// a state key in it. The event is returned as stored bytes because what a member event *says* —
// its membership, its display name — lives in its content, and reading it out of the stored bytes
// is the same read [saysJoined] has always done.
type roomStateRows struct {
	RoomID   string
	StateKey string
	JSON     []byte
}

// JoinedRoomIDs implements [UserDirectoryRepo].
func (r *userDirectoryRepo) JoinedRoomIDs(ctx context.Context, userID string) ([]string, error) {
	var rows []roomStateRows
	err := r.db.WithContext(ctx).Table("room_state AS rs").
		Select("rs.room_id AS room_id, rs.state_key AS state_key, e.json AS json").
		Where("rs.type = ? AND rs.state_key = ?", "m.room.member", userID).
		Joins("JOIN events AS e ON e.event_id = rs.event_id").
		Order("rs.room_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		// The state key is the person the event is about; an empty one is an event about nobody, and
		// it is not a room anybody is in.
		if row.StateKey != "" && saysJoined(row.JSON) {
			out = append(out, row.RoomID)
		}
	}
	return out, nil
}

// OpenRoomIDs implements [UserDirectoryRepo].
func (r *userDirectoryRepo) OpenRoomIDs(ctx context.Context) ([]string, error) {
	// One read over the two state types that decide visibility, rather than two reads: a room has
	// at most one of each, so this is two rows per room at most.
	var rows []roomStateRows
	err := r.db.WithContext(ctx).Table("room_state AS rs").
		Select("rs.room_id AS room_id, e.json AS json").
		Where("rs.type IN ?", []string{"m.room.join_rules", "m.room.history_visibility"}).
		Joins("JOIN events AS e ON e.event_id = rs.event_id").
		Order("rs.room_id ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	open := make(map[string]bool)
	for _, row := range rows {
		// A room is named once and stays named: a room whose join rule is public is open even if
		// its history visibility says something stricter, and the reverse.
		if open[row.RoomID] {
			continue
		}
		if roomIsOpen(row.JSON) {
			open[row.RoomID] = true
		}
	}
	out := make([]string, 0, len(open))
	for roomID := range open {
		out = append(out, roomID)
	}
	// The scan above filled a map, which has no order; a directory that returns rooms in an order
	// the runtime chose would make every search's result order a coin toss.
	sort.Strings(out)
	return out, nil
}

// roomIsOpen reports whether the state event in raw makes its room's membership visible to a
// stranger. `public` and `world_readable` are the two words the specification uses for that, and
// this recognises exactly those two: an absent event is neither, because a room whose join rule was
// never written is not a room anybody decided to open.
func roomIsOpen(raw []byte) bool {
	var event struct {
		Type    string `json:"type"`
		Content struct {
			JoinRule          string `json:"join_rule"`
			HistoryVisibility string `json:"history_visibility"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return false
	}
	if event.Type == "m.room.join_rules" {
		return event.Content.JoinRule == "public"
	}
	return event.Content.HistoryVisibility == "world_readable"
}

// JoinedNamesIn implements [UserDirectoryRepo].
func (r *userDirectoryRepo) JoinedNamesIn(ctx context.Context, roomIDs []string) ([]RoomMemberName, error) {
	const chunk = 500
	out := make([]RoomMemberName, 0, len(roomIDs))
	for start := 0; start < len(roomIDs); start += chunk {
		end := min(start+chunk, len(roomIDs))
		var rows []roomStateRows
		err := r.db.WithContext(ctx).Table("room_state AS rs").
			Select("rs.room_id AS room_id, rs.state_key AS state_key, e.json AS json").
			Where("rs.type = ? AND rs.room_id IN ?", "m.room.member", roomIDs[start:end]).
			Joins("JOIN events AS e ON e.event_id = rs.event_id").
			Order("rs.room_id ASC, rs.state_key ASC").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.StateKey == "" {
				// A member event with an empty state key is not about anybody.
				continue
			}
			name, ok := memberName(row.JSON)
			if !ok {
				continue
			}
			name.RoomID = row.RoomID
			name.UserID = row.StateKey
			out = append(out, name)
		}
	}
	return out, nil
}

// memberName reads the display name and avatar a member event carries, and says whether the event
// still says its user is in the room. A name written by an invite, a leave or a ban is a record of
// what somebody said at the time and not a name the person is going by now, so those are not
// returned at all.
func memberName(raw []byte) (RoomMemberName, bool) {
	var event struct {
		Content struct {
			Membership  string `json:"membership"`
			DisplayName string `json:"displayname"`
			AvatarURL   string `json:"avatar_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return RoomMemberName{}, false
	}
	if event.Content.Membership != "join" {
		return RoomMemberName{}, false
	}
	return RoomMemberName{DisplayName: event.Content.DisplayName, AvatarURL: event.Content.AvatarURL}, true
}
