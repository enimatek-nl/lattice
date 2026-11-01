package unit

import (
	"testing"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/power"
)

// TestPowerDefaults pins the levels of a room that never set an
// m.room.power_levels event. The numbers come from the "Power level
// restrictions" section: everyone may talk and invite, only administrators
// (level 50) may kick, ban, redact or change state.
func TestPowerDefaults(t *testing.T) {
	levels := power.Defaults()
	tests := []struct {
		name string
		got  int64
		want int64
	}{
		{name: "invite", got: levels.Invite, want: 0},
		{name: "kick", got: levels.Kick, want: 50},
		{name: "ban", got: levels.Ban, want: 50},
		{name: "redact", got: levels.Redact, want: 50},
		{name: "events_default", got: levels.SendDefault, want: 0},
		{name: "users_default", got: levels.UsersDefault, want: 0},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("default %s = %d, want %d", tt.name, tt.got, tt.want)
		}
	}
	if power.Admin != 100 {
		t.Errorf("the creator's level = %d, want 100", power.Admin)
	}
}

// TestApplyDefaults pins what happens to levels a decoded event leaves at zero.
// A decoded event cannot tell an absent field from an explicit 0, so kick, ban
// and redact fall back to 50: reading an absent field as 0 would let every
// member of a plain room ban everybody else.
func TestApplyDefaults(t *testing.T) {
	tests := []struct {
		name    string
		in      matrix.PowerLevelsContent
		comment string
		kick    int64
		ban     int64
		redact  int64
		invite  int64
	}{
		{
			name: "an empty event gets the specification defaults",
			in:   matrix.PowerLevelsContent{},
			kick: 50, ban: 50, redact: 50, invite: 0,
		},
		{
			name: "an explicit level is kept",
			in:   matrix.PowerLevelsContent{Kick: 10, Ban: 0, Redact: 0, Invite: 0},
			kick: 10, ban: 50, redact: 50, invite: 0,
		},
		{
			name: "an explicit zero is allowed where the default is zero",
			in:   matrix.PowerLevelsContent{Invite: 0, Kick: 0, Ban: 25, Redact: 0},
			kick: 50, ban: 25, redact: 50, invite: 0,
			comment: "kick 0 and redact 0 read as the default, which is the safe reading",
		},
	}
	for _, tt := range tests {
		got := power.ApplyDefaults(tt.in)
		if got.Kick != tt.kick || got.Ban != tt.ban || got.Redact != tt.redact || got.Invite != tt.invite {
			t.Errorf("%s: got kick=%d ban=%d redact=%d invite=%d, want %d/%d/%d/%d",
				tt.name, got.Kick, got.Ban, got.Redact, got.Invite, tt.kick, tt.ban, tt.redact, tt.invite)
		}
	}
}

// TestPowerLevel pins which level a user has. The creator of a room holds 100
// even when the room lists no levels at all: without that the creator of a room
// could not rename it, and a room could not be administered after a upgrade that
// wrote no users map.
func TestPowerLevel(t *testing.T) {
	levels := matrix.PowerLevelsContent{
		Users:        map[string]int64{"@admin:example.com": 100, "@mod:example.com": 50, "@muted:example.com": -1},
		UsersDefault: 0,
	}
	tests := []struct {
		user    string
		creator string
		want    int64
		comment string
	}{
		{user: "@admin:example.com", creator: "@someone:example.com", want: 100},
		{user: "@mod:example.com", creator: "@someone:example.com", want: 50},
		{user: "@muted:example.com", creator: "@someone:example.com", want: -1,
			comment: "a negative level is a real level: it silences a member below the default"},
		{user: "@plain:example.com", creator: "@someone:example.com", want: 0,
			comment: "an unnamed member gets the room default"},
		{user: "@creator:example.com", creator: "@creator:example.com", want: 100,
			comment: "the creator is an administrator even when the users map does not say so"},
		{user: "@creator:example.com", creator: "", want: 0,
			comment: "without a known creator the level falls back to the default"},
	}
	for _, tt := range tests {
		if got := power.Level(levels, tt.user, tt.creator); got != tt.want {
			t.Errorf("Level(%s, creator %q) = %d, want %d", tt.user, tt.creator, got, tt.want)
		}
	}
	// A room that raises its default level governs everybody, named or not.
	raised := matrix.PowerLevelsContent{UsersDefault: 10}
	if got := power.Level(raised, "@plain:example.com", ""); got != 10 {
		t.Errorf("users_default 10: Level = %d, want 10", got)
	}
	// A named user can sit below the raised default: that is how a room mutes
	// one person without silencing the room.
	muted := matrix.PowerLevelsContent{UsersDefault: 10, Users: map[string]int64{"@noise:example.com": 0}}
	if got := power.Level(muted, "@noise:example.com", ""); got != 0 {
		t.Errorf("a member below the raised default: Level = %d, want 0", got)
	}
}

// TestEventRequired pins the level an event asks for. State events default to
// 50, messages to the room's events_default, and a per-type entry wins over
// both.
func TestEventRequired(t *testing.T) {
	// A room's levels as a decoded event would arrive, with the specification's
	// defaults restored over it: a room that wrote no state_default asks 50 for a
	// state event, and a room that wrote one means it.
	levels := power.ApplyDefaults(matrix.PowerLevelsContent{
		SendDefault: 5,
		Events:      map[string]int64{"m.room.name": 100, "m.room.message": 0, "m.sticker": 10},
	})
	tests := []struct {
		typ     string
		isState bool
		want    int64
		comment string
	}{
		{typ: "m.room.name", isState: true, want: 100, comment: "the room raised the level of one state type"},
		{typ: "m.room.topic", isState: true, want: 50, comment: "unnamed state needs an administrator"},
		{typ: "m.room.message", isState: false, want: 0, comment: "an explicit 0 is not the same as absent"},
		{typ: "m.sticker", isState: false, want: 10},
		{typ: "m.reaction", isState: false, want: 5, comment: "unnamed messages follow events_default"},
	}
	for _, tt := range tests {
		if got := power.EventRequired(levels, tt.typ, tt.isState); got != tt.want {
			t.Errorf("EventRequired(%s, state=%v) = %d, want %d", tt.typ, tt.isState, got, tt.want)
		}
	}
	// A room with no levels at all: state is an administrator's job, messages are
	// open.
	empty := power.Defaults()
	if got := power.EventRequired(empty, "m.room.name", true); got != 50 {
		t.Errorf("state in a default room = %d, want 50", got)
	}
	if got := power.EventRequired(empty, "m.room.message", false); got != 0 {
		t.Errorf("messages in a default room = %d, want 0", got)
	}
}

// TestActionRequired pins the invite, kick, ban and redact levels, and what an
// unknown action falls back to.
func TestActionRequired(t *testing.T) {
	levels := matrix.PowerLevelsContent{Invite: 0, Kick: 25, Ban: 50, Redact: 5}
	tests := []struct {
		action   string
		fallback int64
		want     int64
		comment  string
	}{
		{action: "invite", fallback: 50, want: 0},
		{action: "kick", fallback: 50, want: 25},
		{action: "ban", fallback: 0, want: 50},
		{action: "redact", fallback: 0, want: 5},
		{action: "knock", fallback: 7, want: 7, comment: "an action the event has no field for keeps the caller's fallback"},
		{action: "", fallback: 3, want: 3},
	}
	for _, tt := range tests {
		if got := power.ActionRequired(levels, tt.action, tt.fallback); got != tt.want {
			t.Errorf("ActionRequired(%q) = %d, want %d", tt.action, got, tt.want)
		}
	}
}

// TestChangeRequired pins the level it takes to change somebody's power level.
// The specification says a user may only change levels up to and including their
// own, and this covers the demotion case: dropping an administrator to 10 takes
// an administrator, otherwise a moderator could strip the creator by quietly
// lowering them.
func TestChangeRequired(t *testing.T) {
	tests := []struct {
		current, want, needed int64
		comment               string
	}{
		{current: 0, want: 50, needed: 50, comment: "raising somebody takes the level you are handing out"},
		{current: 100, want: 10, needed: 100, comment: "demoting an administrator takes an administrator"},
		{current: 50, want: 50, needed: 50},
		{current: -1, want: 0, needed: 0},
		{current: 0, want: -1, needed: 0, comment: "unmuting somebody takes their old level, which is the higher one"},
		{current: 100, want: 100, needed: 100},
	}
	for _, tt := range tests {
		if got := power.ChangeRequired(tt.current, tt.want); got != tt.needed {
			t.Errorf("ChangeRequired(%d, %d) = %d, want %d", tt.current, tt.want, got, tt.needed)
		}
	}
}
