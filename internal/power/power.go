// Package power holds the power level arithmetic of the specification: which
// level a user has in a room, which level an action asks for, and which level it
// takes to change somebody's level.
//
// The functions are pure - they take a decoded m.room.power_levels event and
// answer - so the rules of the "Power level restrictions" section can be tested
// without a database, and the same arithmetic can decide permissions for events
// that arrive over federation.
package power

import (
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The defaults of the "Power level restrictions" section. A power level is a
// signed integer; a user may only perform an action whose required level is at
// or below their own.
const (
	// Admin is the level the creator of a room holds, and the level a room
	// upgrade raises the levels to.
	Admin = 100
	// DefaultUser is the level of a member the room has not named.
	DefaultUser = 0
	// DefaultInvite, DefaultKick, DefaultBan and DefaultRedact are the levels the
	// membership actions ask for. Inviting is open to everyone, the rest is an
	// administrator's job.
	DefaultInvite = 0
	DefaultKick   = 50
	DefaultBan    = 50
	DefaultRedact = 50
	// DefaultState is the level required to change a state event that the room
	// has not given its own level.
	DefaultState = 50
	// DefaultMessage is the level required to send a message.
	DefaultMessage = 0
)

// Defaults returns a power levels event as the specification defines it for a
// room that never set one.
func Defaults() matrix.PowerLevelsContent {
	return matrix.PowerLevelsContent{
		Invite:       DefaultInvite,
		Kick:         DefaultKick,
		Ban:          DefaultBan,
		Redact:       DefaultRedact,
		SendDefault:  DefaultMessage,
		StateDefault: DefaultState,
		UsersDefault: DefaultUser,
		// The per-event levels are empty, but they are written out: a client that
		// renders "who may send which event" reads this object, and a missing key is
		// a blank table where the answer is "the room default for every one of them".
		Events: map[string]int64{},
	}
}

// ApplyDefaults restores the levels a decoded event leaves at zero.
//
// The zero value cannot tell an absent field from a field explicitly set to 0,
// and for kick, ban and redact the specification's default is 50 rather than 0:
// a room that writes no levels at all must not let every member ban people. An
// explicit 0 therefore reads as the default, which is what homeservers do.
func ApplyDefaults(levels matrix.PowerLevelsContent) matrix.PowerLevelsContent {
	if levels.Kick == 0 {
		levels.Kick = DefaultKick
	}
	if levels.Ban == 0 {
		levels.Ban = DefaultBan
	}
	if levels.Redact == 0 {
		levels.Redact = DefaultRedact
	}
	if levels.StateDefault == 0 {
		levels.StateDefault = DefaultState
	}
	return levels
}

// Level returns the effective power level of a user: the level the room gave
// them, or the administrator level if they created the room, or the room's
// default level for everyone else.
func Level(levels matrix.PowerLevelsContent, userID, creator string) int64 {
	if level, ok := levels.Users[userID]; ok {
		return level
	}
	if creator != "" && userID == creator {
		return Admin
	}
	return levels.UsersDefault
}

// EventRequired returns the level required to send an event of a type: the
// per-type level the room set, or the room's state_default for a state event, or
// the room's events_default for a message.
//
// The levels passed in have to be the room's levels with the specification's
// defaults restored, which is what [Defaults] and [ApplyDefaults] produce. A room
// that wrote its own state_default of 0 means it, and a caller that restores the
// defaults over a decoded event has already turned an absent key into 50.
func EventRequired(levels matrix.PowerLevelsContent, typ string, isState bool) int64 {
	if level, ok := levels.Events[typ]; ok {
		return level
	}
	if isState {
		return levels.StateDefault
	}
	return levels.SendDefault
}

// ActionRequired returns the level the power levels event asks for a room action
// - invite, kick, ban or redact - falling back to fallback for actions the event
// has no field for.
func ActionRequired(levels matrix.PowerLevelsContent, action string, fallback int64) int64 {
	switch action {
	case "invite":
		return levels.Invite
	case "kick":
		return levels.Kick
	case "ban":
		return levels.Ban
	case "redact":
		return levels.Redact
	}
	return fallback
}

// ChangeRequired returns the level a user needs to give somebody else the level
// want when their current level is current. The specification says a user may
// only change power levels up to and including their own, so the higher of the
// two levels wins: lowering an administrator to 10 takes an administrator, just
// as raising them to 100 does.
func ChangeRequired(current, want int64) int64 {
	if want > current {
		return want
	}
	return current
}
