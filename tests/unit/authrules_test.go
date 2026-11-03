package unit

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/enimatek-nl/lattice/internal/authcheck"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The authorization rules of a room version are the reason two homeservers that
// have never met agree about which events belong in a room. They are a numbered
// list, and a list that long drifts silently: one clause read loosely and a server
// accepts an event its peers reject, which shows up weeks later as a room that
// will not resolve. The table below walks the list. Each case names one rule and
// changes one fact, so a passing suite says each clause is enforced on its own.
//
// The cases state a room version where the rule differs by version, and every
// version this server can create rooms in is covered for the rules that moved.

// ruleCase describes one authorization question.
type ruleCase struct {
	name    string
	version string
	// state holds the events the room's state points at. They are also reachable
	// through the graph, because a state entry names an event the rules then read.
	state []*authcheck.Event
	// extra holds events that are not current state but are reachable through the
	// auth chains - a create event that has been superseded is the usual one.
	extra []*authcheck.Event
	// event is the event being judged.
	event *authcheck.Event
	// auth overrides the auth events the event cites. Empty means the ones the
	// selection rules name, which is what a correct sender cites.
	auth []string
	// rejected names events this server already rejected, with the reason.
	rejected map[string]string
	// want is empty when the rules allow the event, and otherwise a fragment of
	// the refusal.
	want string
}

// TestAuthorizationRules walks the numbered rules of the room versions.
func TestAuthorizationRules(t *testing.T) {
	const (
		alice = "@alice:alice.example" // the creator, an administrator
		bob   = "@bob:bob.example"     // a moderator
		carol = "@carol:carol.example" // a member with no power
		dave  = "@dave:dave.example"   // nobody in the room at all
	)
	roomID := "!room:alice.example"

	// Builders for the events a room is made of.
	stateOf := func(id, typ, stateKey, sender string, content matrix.Object) *authcheck.Event {
		key := stateKey
		return &authcheck.Event{
			ID:             id,
			Type:           typ,
			StateKey:       &key,
			Sender:         sender,
			OriginServerTS: 1_700_000_000_000,
			Content:        content,
		}
	}
	create := func(sender string, content matrix.Object) *authcheck.Event {
		return stateOf("$create", matrix.TypeCreate, "", sender, content)
	}
	levels := func(content matrix.Object) *authcheck.Event {
		return stateOf("$pl", matrix.TypePowerLevels, "", alice, content)
	}
	joinRules := func(rule string, extra ...matrix.Object) *authcheck.Event {
		content := matrix.Object{"join_rule": rule}
		for _, entry := range extra {
			for key, value := range entry {
				content[key] = value
			}
		}
		return stateOf("$jr", matrix.TypeJoinRules, "", alice, content)
	}
	// admitsThrough is the join rules event of a room that lets people in from another room.
	admitsThrough := func(rule string, rooms ...string) *authcheck.Event {
		allow := make([]any, 0, len(rooms))
		for _, room := range rooms {
			allow = append(allow, matrix.Object{"type": "m.room_membership", "room_id": room})
		}
		return joinRules(rule, matrix.Object{"allow": allow})
	}
	member := func(target, membership, sender string, extra matrix.Object) *authcheck.Event {
		content := matrix.Object{"membership": membership}
		for key, value := range extra {
			content[key] = value
		}
		return stateOf("$member-"+target, matrix.TypeMember, target, sender, content)
	}
	// users maps the three named users onto their levels, so a case can change one
	// of them without rewriting the whole event.
	levelsOf := func(entries matrix.Object) matrix.Object {
		// A case that names users replaces the map whole, so it says exactly which
		// levels the room's levels event holds.
		users := matrix.Object{alice: int64(100), bob: int64(50), carol: int64(0)}
		if over, ok := entries["users"].(matrix.Object); ok {
			users = matrix.Object{}
			for user, level := range over {
				users[user] = level
			}
		}
		out := matrix.Object{"users": users}
		for field, value := range entries {
			if field != "users" {
				out[field] = value
			}
		}
		return out
	}

	// The default room: alice's, public, with alice, bob and carol joined.
	basics := func(rule string, extra ...*authcheck.Event) []*authcheck.Event {
		state := []*authcheck.Event{
			create(alice, matrix.Object{"creator": alice}),
			levels(levelsOf(matrix.Object{})),
			joinRules(rule),
			member(alice, matrix.MembershipJoin, alice, nil),
			member(bob, matrix.MembershipJoin, alice, nil),
			member(carol, matrix.MembershipJoin, alice, nil),
		}
		return append(state, extra...)
	}
	// withJoinRule replaces the room's join rules event for one case.
	withJoinRule := func(state []*authcheck.Event, rule *authcheck.Event) []*authcheck.Event {
		out := append([]*authcheck.Event{}, state...)
		for i, ev := range out {
			if ev.Type == matrix.TypeJoinRules {
				out[i] = rule
				break
			}
		}
		return out
	}
	// withLevels replaces the room's power levels event for one case.
	withLevels := func(state []*authcheck.Event, entries matrix.Object) []*authcheck.Event {
		out := append([]*authcheck.Event{}, state...)
		out[1] = levels(levelsOf(entries))
		return out
	}

	// membershipEvent builds the membership event a case judges.
	membershipEvent := func(id, target, membership, sender string, extra matrix.Object) *authcheck.Event {
		content := matrix.Object{"membership": membership}
		for key, value := range extra {
			content[key] = value
		}
		return stateOf(id, matrix.TypeMember, target, sender, content)
	}
	redaction := func(id, sender, target string) *authcheck.Event {
		event := message(id, matrix.TypeRedaction, sender, matrix.Object{})
		event.Redacts = target
		return event
	}

	tests := []ruleCase{
		// Rule 1: the create event.
		{
			name: "a create event cannot have prev_events",
			event: func() *authcheck.Event {
				event := create(alice, matrix.Object{"creator": alice})
				event.PrevIDs = []string{"$somewhere_else"}
				return event
			}(),
			want: "cannot have prev_events",
		},
		{
			name:  "a create event from a server other than the room's is refused",
			event: create(bob, matrix.Object{"creator": bob}),
			want:  "different servers",
		},
		{
			name:  "room version 11 does not need content.creator",
			event: create(alice, matrix.Object{"room_version": "11"}),
		},
		{
			name:    "room version 10 does need content.creator",
			version: "10",
			event:   create(alice, matrix.Object{}),
			want:    "must name its creator",
		},
		{
			name:  "a create event that spells room_version as a number is refused",
			event: create(alice, matrix.Object{"creator": alice, "room_version": int64(11)}),
			want:  "must be a string",
		},

		// Rule 3: a room that does not federate.
		{
			name: "a room that set m.federate to false holds only its own server's events",
			state: func() []*authcheck.Event {
				state := basics(matrix.JoinRulesPublic)
				state[0] = create(alice, matrix.Object{"creator": alice, "m.federate": false})
				return state
			}(),
			event: message("$msg", matrix.TypeMessage, bob, matrix.Object{"msgtype": "m.text", "body": "hi"}),
			want:  "m.federate to false",
		},

		// Rule 4: joining.
		{
			name:  "joining a public room is allowed",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
		},
		{
			name:  "joining an invite-only room without an invite is refused",
			state: basics(matrix.JoinRulesInvite),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
			want:  "has not been invited (membership: none)",
		},
		{
			name: "joining an invite-only room with an invite is allowed",
			state: basics(matrix.JoinRulesInvite,
				member(dave, matrix.MembershipInvite, alice, nil)),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
		},
		{
			name: "a user that left a public room may join it again",
			state: basics(matrix.JoinRulesPublic,
				member(dave, matrix.MembershipLeave, dave, nil)),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
		},
		{
			name: "a banned user cannot join",
			state: basics(matrix.JoinRulesPublic,
				member(dave, matrix.MembershipBan, alice, nil)),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
			want:  "is banned",
		},
		{
			name:  "a user cannot join as somebody else",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, bob, nil),
			want:  "@bob:bob.example cannot join as " + dave,
		},
		{
			name:  "an empty membership is not a membership",
			state: basics(matrix.JoinRulesPublic),
			event: stateOf("$member-x", matrix.TypeMember, carol, carol, matrix.Object{}),
			want:  "must name a membership",
		},
		{
			name: "the creator may join the room it made before it has joined",
			state: []*authcheck.Event{
				create(alice, matrix.Object{"creator": alice}),
				levels(matrix.Object{}),
				joinRules(matrix.JoinRulesInvite),
			},
			event: func() *authcheck.Event {
				event := membershipEvent("$join", alice, matrix.MembershipJoin, alice, nil)
				event.PrevIDs = []string{"$create"}
				return event
			}(),
		},
		{
			name: "a room with no m.room.join_rules event is invite-only",
			state: []*authcheck.Event{
				create(alice, matrix.Object{"creator": alice}),
				levels(levelsOf(matrix.Object{})),
				member(alice, matrix.MembershipJoin, alice, nil),
			},
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
			want:  "join rule is invite",
		},

		// Restricted joins, room version 8 and later.
		{
			name:  "a restricted join needs an authorising user",
			state: basics(matrix.JoinRulesRestricted),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
			want:  "names no authorising user",
		},
		{
			name:  "a restricted join through an authoriser that can invite is allowed",
			state: basics(matrix.JoinRulesRestricted),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave,
				matrix.Object{"join_authorised_via_users_server": bob}),
		},
		{
			// The joiner may authorise their own join. What admits them is membership of a
			// room on the allow list, which is not a fact about this room's state, so the
			// snapshot here cannot check it and the rules say nothing against it. The caller
			// checks the other room.
			name: "a restricted join may be authorised by the joiner themself",
			state: withJoinRule(basics(matrix.JoinRulesRestricted),
				admitsThrough(matrix.JoinRulesRestricted, "!allowed:domain")),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave,
				matrix.Object{"join_authorised_via_users_server": dave}),
		},
		{
			// A self-authorised join stands on the allow list and nothing else. A list of junk
			// is no list, so the claim "I belong to one of those rooms" has nothing to stand on.
			name: "a restricted join with a malformed allow list is refused",
			state: withJoinRule(basics(matrix.JoinRulesRestricted),
				joinRules(matrix.JoinRulesRestricted, matrix.Object{"allow": []any{"invalid"}})),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave,
				matrix.Object{"join_authorised_via_users_server": dave}),
			want: "its allow list names no room this server can read",
		},
		{
			name:  "a restricted join cannot be authorised by a user that is not joined",
			state: basics(matrix.JoinRulesRestricted),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave,
				matrix.Object{"join_authorised_via_users_server": "@nobody:domain"}),
			want: "is not joined to the room, so it cannot authorise",
		},
		{
			name:  "a restricted join cannot be authorised by a user that cannot invite",
			state: withLevels(basics(matrix.JoinRulesRestricted), matrix.Object{"invite": int64(50)}),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave,
				matrix.Object{"join_authorised_via_users_server": carol}),
			want: "cannot invite, so it cannot authorise",
		},
		{
			name:    "room version 7 does not know the restricted join rule",
			version: "7",
			state:   basics(matrix.JoinRulesRestricted),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave,
				matrix.Object{"join_authorised_via_users_server": bob}),
			want: "does not know the restricted join rule",
		},
		{
			name:  "an authorising user on a public join changes nothing",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave,
				matrix.Object{"join_authorised_via_users_server": carol}),
		},

		// Knocking, room version 7 and later.
		{
			name:  "knocking a knock room is allowed",
			state: basics(matrix.JoinRulesKnock),
			event: membershipEvent("$knock", dave, matrix.MembershipKnock, dave, nil),
		},
		{
			name:  "knocking a public room is refused",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$knock", dave, matrix.MembershipKnock, dave, nil),
			want:  "so nobody can knock",
		},
		{
			name:    "room version 5 has no knock membership",
			version: "5",
			state:   basics(matrix.JoinRulesKnock),
			event:   membershipEvent("$knock", dave, matrix.MembershipKnock, dave, nil),
			want:    "no knock membership",
		},
		{
			name:  "a joined user cannot knock",
			state: basics(matrix.JoinRulesKnock),
			event: membershipEvent("$knock", carol, matrix.MembershipKnock, carol, nil),
			want:  "already join in the room",
		},

		// Inviting.
		{
			name:  "a moderator can invite",
			state: basics(matrix.JoinRulesInvite),
			event: membershipEvent("$inv", dave, matrix.MembershipInvite, bob, nil),
		},
		{
			name:  "a room that raised the invite level shuts inviting to members",
			state: withLevels(basics(matrix.JoinRulesInvite), matrix.Object{"invite": int64(50)}),
			event: membershipEvent("$inv", dave, matrix.MembershipInvite, carol, nil),
			want:  "inviting needs power level 50",
		},
		{
			name: "a user that has not joined cannot invite",
			state: basics(matrix.JoinRulesInvite,
				member(dave, matrix.MembershipLeave, dave, nil)),
			event: membershipEvent("$inv", carol, matrix.MembershipInvite, dave, nil),
			want:  "is not joined to the room, so it cannot invite",
		},
		{
			name:  "inviting a joined user is refused",
			state: basics(matrix.JoinRulesInvite),
			event: membershipEvent("$inv", bob, matrix.MembershipInvite, alice, nil),
			want:  "already join in the room",
		},
		{
			name: "inviting a banned user is refused",
			state: basics(matrix.JoinRulesInvite,
				member(dave, matrix.MembershipBan, alice, nil)),
			event: membershipEvent("$inv", dave, matrix.MembershipInvite, alice, nil),
			want:  "already ban in the room",
		},

		// Leaving, kicking, banning.
		{
			name:  "a joined user may leave",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$lv", bob, matrix.MembershipLeave, bob, nil),
		},
		{
			name:  "a user may not leave a room they are not in",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$lv", dave, matrix.MembershipLeave, dave, nil),
			want:  "cannot leave a room it is not in",
		},
		{
			name:  "an administrator may kick a member",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$lv", carol, matrix.MembershipLeave, alice, nil),
		},
		{
			name:  "a moderator may not kick a user of greater power",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$lv", alice, matrix.MembershipLeave, bob, nil),
			want:  "kicking needs power level",
		},
		{
			name: "a knock may be retracted by the knocker",
			state: basics(matrix.JoinRulesKnock,
				member(dave, matrix.MembershipKnock, dave, nil)),
			event: membershipEvent("$lv", dave, matrix.MembershipLeave, dave, nil),
		},
		{
			name: "removing a banned user needs the ban level",
			state: basics(matrix.JoinRulesPublic,
				member(dave, matrix.MembershipBan, alice, nil)),
			event: membershipEvent("$lv", dave, matrix.MembershipLeave, carol, nil),
			want:  "removing a banned user needs the ban level",
		},
		{
			name:  "an administrator can ban",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$ban", dave, matrix.MembershipBan, alice, nil),
		},
		{
			name:  "a user cannot ban a user of greater power",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$ban", alice, matrix.MembershipBan, bob, nil),
			want:  "banning needs power level",
		},

		// Rule 5: the sender has to be joined.
		{
			name:  "a joined user may send a message",
			state: basics(matrix.JoinRulesPublic),
			event: message("$msg", matrix.TypeMessage, bob, matrix.Object{"msgtype": "m.text", "body": "hi"}),
		},
		{
			name:  "a user that has not joined may not send a message",
			state: basics(matrix.JoinRulesPublic),
			event: message("$msg", matrix.TypeMessage, dave, matrix.Object{"msgtype": "m.text", "body": "hi"}),
			want:  "is not joined to the room",
		},
		{
			name:  "a member with no power may not change state",
			state: basics(matrix.JoinRulesPublic),
			event: stateOf("$name", matrix.TypeName, "", carol, matrix.Object{"name": "Mine"}),
			want:  "sending m.room.name needs power level 50",
		},
		{
			name:  "a moderator may change state",
			state: basics(matrix.JoinRulesPublic),
			event: stateOf("$name", matrix.TypeName, "", bob, matrix.Object{"name": "Ours"}),
		},
		{
			name:  "a room that lowered state_default lets members rename it",
			state: withLevels(basics(matrix.JoinRulesPublic), matrix.Object{"state_default": int64(0)}),
			event: stateOf("$name", matrix.TypeName, "", carol, matrix.Object{"name": "Ours"}),
		},

		// Rule 8: a state key that names a user belongs to that user.
		{
			name:  "nobody but the owner writes a state key that names a user",
			state: basics(matrix.JoinRulesPublic),
			event: stateOf("$ign", "m.direct", carol, bob, matrix.Object{}),
			want:  "is not the sender",
		},

		// Rule 9: changing the power levels.
		{
			name:  "an administrator may set levels and grant power up to their own",
			state: basics(matrix.JoinRulesPublic),
			event: levels2("$pl2", alice, levelsOf(matrix.Object{"users": matrix.Object{
				alice: int64(100), bob: int64(50), carol: int64(50),
			}, "state_default": int64(50), "events": matrix.Object{matrix.TypeMessage: int64(0)}})),
		},
		{
			name:  "a moderator may not hand themselves the administrator level",
			state: basics(matrix.JoinRulesPublic),
			event: levels2("$pl2", bob, levelsOf(matrix.Object{"users": matrix.Object{
				alice: int64(100), bob: int64(100), carol: int64(0),
			}})),
			want: "giving @bob:bob.example power level 100 needs power level 100",
		},
		{
			name:  "a moderator may change their own level",
			state: basics(matrix.JoinRulesPublic),
			event: levels2("$pl2", bob, levelsOf(matrix.Object{"users": matrix.Object{
				alice: int64(100), bob: int64(0), carol: int64(0),
			}})),
		},
		{
			name:  "a moderator may not change a level they do not hold",
			state: withLevels(basics(matrix.JoinRulesPublic), matrix.Object{"ban": int64(90)}),
			event: levels2("$pl2", bob, levelsOf(matrix.Object{"ban": int64(0)})),
			want:  "changing or removing ban (90) needs power level 90",
		},
		{
			name:  "a moderator may change a level they hold",
			state: basics(matrix.JoinRulesPublic),
			event: levels2("$pl2", bob, levelsOf(matrix.Object{"ban": int64(50), "kick": int64(40)})),
		},
		{
			name: "a moderator may not remove an event level they do not hold",
			state: withLevels(basics(matrix.JoinRulesPublic), matrix.Object{
				"events": matrix.Object{"m.sensitive": int64(90)},
			}),
			event: levels2("$pl2", bob, levelsOf(matrix.Object{"events": matrix.Object{}})),
			want:  "changing or removing the level of m.sensitive",
		},
		{
			name:    "room version 10 refuses power levels spelled as strings",
			version: "10",
			state:   basics(matrix.JoinRulesPublic),
			event:   levels2("$pl2", alice, levelsOf(matrix.Object{"ban": "50"})),
			want:    "does not accept as a number",
		},
		{
			name:    "room version 9 still accepts them",
			version: "9",
			state:   basics(matrix.JoinRulesPublic),
			event:   levels2("$pl2", alice, levelsOf(matrix.Object{"ban": "50"})),
		},
		{
			name:  "a power levels event with a user id that is not one is refused",
			state: basics(matrix.JoinRulesPublic),
			event: levels2("$pl2", alice, matrix.Object{
				"users": matrix.Object{"not-a-user": int64(50)},
			}),
			want: "which is not a user id",
		},

		// Rule 2: the event's own auth events.
		{
			name:  "a join that omits the join rules from its auth events is refused",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
			auth:  []string{"$create", "$pl"},
			want:  "missing m.room.join_rules",
		},
		{
			name: "a join that cites an auth event the selection rules do not name is refused",
			state: basics(matrix.JoinRulesPublic,
				stateOf("$topic", matrix.TypeTopic, "", alice, matrix.Object{"topic": "x"})),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
			auth:  []string{"$create", "$pl", "$jr", "$topic"},
			want:  "the selection rules do not name",
		},
		{
			name:     "an event that cites an event this server rejected is refused",
			state:    basics(matrix.JoinRulesPublic),
			event:    message("$msg", matrix.TypeMessage, bob, matrix.Object{"msgtype": "m.text", "body": "hi"}),
			auth:     []string{"$create", "$pl", "$member-" + bob},
			rejected: map[string]string{"$pl": "a moderator may not write the levels"},
			want:     "which was rejected",
		},
		{
			name:  "an event that cites an auth event this server does not have is refused",
			state: basics(matrix.JoinRulesPublic),
			event: membershipEvent("$join", dave, matrix.MembershipJoin, dave, nil),
			auth:  []string{"$create", "$pl", "$jr", "$member-" + dave},
			want:  "which this server does not have",
		},

		// Redaction. Rule 11 of room versions 1 and 2; from version 3 on the auth
		// list says nothing more about a redaction than the power level of its type.
		{
			name:    "room version 2 lets a user redact on their own server without the level",
			version: "2",
			state:   basics(matrix.JoinRulesPublic),
			event:   redaction("$red:carol.example", carol, "$target:carol.example"),
		},
		{
			name:    "room version 2 refuses a redaction of another server's event without the level",
			version: "2",
			state:   basics(matrix.JoinRulesPublic),
			event:   redaction("$red:carol.example", carol, "$target:alice.example"),
			want:    "neither the redact level nor the target's server",
		},
		{
			name:  "room version 11 puts no rule of its own on a redaction",
			state: basics(matrix.JoinRulesPublic),
			event: redaction("$red:carol.example", carol, "$target:alice.example"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version := tt.version
			if version == "" {
				version = "11"
			}
			g := authcheck.NewGraph(version)
			state := authcheck.State{}
			for _, event := range append(append([]*authcheck.Event{}, tt.state...), tt.extra...) {
				if event.RoomID == "" {
					event.RoomID = roomID
				}
				g.Add(event)
				if event.IsState() {
					state[event.Key()] = event.ID
				}
			}
			for id, reason := range tt.rejected {
				g.Reject(id, reason)
			}

			tt.event.RoomID = roomID
			if tt.event.PrevIDs == nil && tt.event.Type != matrix.TypeCreate {
				tt.event.PrevIDs = []string{"$prev"}
			}
			if tt.auth == nil {
				for _, key := range authcheck.Selection(state, tt.event) {
					tt.event.AuthIDs = append(tt.event.AuthIDs, state[key])
				}
			} else {
				tt.event.AuthIDs = tt.auth
			}

			err := authcheck.Check(g, state, tt.event)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("the rules refused an event they should allow: %v", err)
			case tt.want != "" && err == nil:
				t.Fatalf("the rules allowed an event they should refuse (wanted %q)", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("the rules refused for the wrong reason: %q, wanted %q", err, tt.want)
			}
		})
	}
}

// TestThirdPartyInviteSignature checks the half of the invite rules that cannot be
// answered without a key: the identity server's signature has to verify against a
// key the room's own m.room.third_party_invite event published.
func TestThirdPartyInviteSignature(t *testing.T) {
	const (
		alice = "@alice:alice.example"
		bob   = "@bob:bob.example"
		carol = "@carol:carol.example"
		token = "secret-token"
	)
	roomID := "!room:alice.example"

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("cannot make an identity server key: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("cannot make a second identity server key: %v", err)
	}
	// signedFor makes the statement an identity server makes: a signature over the
	// canonical form of the object that names who is invited and with what token.
	signedFor := func(mxid string) matrix.Object {
		body := mustCanonical(t, matrix.Object{"mxid": mxid, "token": token})
		return matrix.Object{
			"mxid":  mxid,
			"token": token,
			"signatures": matrix.Object{"identity.example": map[string]any{
				"ed25519:0": sign(t, priv, body),
			}},
		}
	}
	published := stateEventWithID("$tpi", matrix.TypeThirdPartyInvite, token, alice, matrix.Object{
		"display_name": "Bob",
		"public_key":   base64.RawStdEncoding.EncodeToString(pub),
	})
	listForm := stateEventWithID("$tpi", matrix.TypeThirdPartyInvite, token, alice, matrix.Object{
		"public_keys": []any{matrix.Object{"public_key": base64.RawStdEncoding.EncodeToString(pub)}},
	})
	otherPublished := stateEventWithID("$tpi", matrix.TypeThirdPartyInvite, token, alice, matrix.Object{
		"display_name": "Bob",
		"public_key":   base64.RawStdEncoding.EncodeToString(otherPub),
	})
	base := []*authcheck.Event{
		stateEventWithID("$create", matrix.TypeCreate, "", alice, matrix.Object{"creator": alice}),
		stateEventWithID("$pl", matrix.TypePowerLevels, "", alice, matrix.Object{"users": matrix.Object{alice: int64(100)}}),
		stateEventWithID("$jr", matrix.TypeJoinRules, "", alice, matrix.Object{"join_rule": matrix.JoinRulesInvite}),
		stateEventWithID("$ma", matrix.TypeMember, alice, alice, matrix.Object{"membership": matrix.MembershipJoin}),
	}

	// judge runs the rules over a room state and an invite that claims a token.
	judge := func(t *testing.T, state []*authcheck.Event, signed matrix.Object) error {
		t.Helper()
		g := authcheck.NewGraph("11")
		roomState := authcheck.State{}
		for _, event := range state {
			event.RoomID = roomID
			g.Add(event)
			roomState[event.Key()] = event.ID
		}
		event := stateEventWithID("$inv", matrix.TypeMember, bob, alice, matrix.Object{
			"membership":         matrix.MembershipInvite,
			"third_party_invite": matrix.Object{"signed": signed},
		})
		event.RoomID = roomID
		event.PrevIDs = []string{"$prev"}
		for _, key := range authcheck.Selection(roomState, event) {
			event.AuthIDs = append(event.AuthIDs, roomState[key])
		}
		return authcheck.Check(g, roomState, event)
	}

	tests := []struct {
		name   string
		state  []*authcheck.Event
		signed matrix.Object
		want   string
	}{
		{
			name:   "a signature from the published key is accepted",
			state:  append(append([]*authcheck.Event{}, base...), published),
			signed: signedFor(bob),
		},
		{
			name:   "the list form of public_keys is accepted too",
			state:  append(append([]*authcheck.Event{}, base...), listForm),
			signed: signedFor(bob),
		},
		{
			name:   "a signature from another key is refused",
			state:  append(append([]*authcheck.Event{}, base...), otherPublished),
			signed: signedFor(bob),
			want:   "no signature on the third-party invite matches",
		},
		{
			name:   "a token the room never published is refused",
			state:  base,
			signed: signedFor(bob),
			want:   "holds no m.room.third_party_invite with state key",
		},
		{
			name:   "an invite signed for another user is refused",
			state:  append(append([]*authcheck.Event{}, base...), published),
			signed: signedFor(carol),
			want:   "is for @carol:carol.example, not for @bob:bob.example",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := judge(t, tt.state, tt.signed)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("a valid third-party invite was refused: %v", err)
			case tt.want != "" && err == nil:
				t.Fatal("a third-party invite the rules should refuse was allowed")
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("refused for the wrong reason: %q, wanted %q", err, tt.want)
			}
		})
	}
}

// TestAuthSelection pins the set of auth events a sender must cite. Rule 2 refuses
// an event whose auth events are not exactly this set, so both sides of a
// federation have to compute the same list from the same state.
func TestAuthSelection(t *testing.T) {
	const (
		alice = "@alice:alice.example"
		bob   = "@bob:bob.example"
		carol = "@carol:carol.example"
	)
	state := authcheck.State{
		authcheck.Key{Type: matrix.TypeCreate}:                          "$create",
		authcheck.Key{Type: matrix.TypePowerLevels}:                     "$pl",
		authcheck.Key{Type: matrix.TypeJoinRules}:                       "$jr",
		authcheck.Key{Type: matrix.TypeMember, StateKey: alice}:         "$ma",
		authcheck.Key{Type: matrix.TypeMember, StateKey: bob}:           "$mb",
		authcheck.Key{Type: matrix.TypeMember, StateKey: carol}:         "$mc",
		authcheck.Key{Type: matrix.TypeThirdPartyInvite, StateKey: "t"}: "$tpi",
		authcheck.Key{Type: matrix.TypeTopic}:                           "$topic",
	}
	tests := []struct {
		name  string
		event *authcheck.Event
		want  []authcheck.Key
	}{
		{
			name:  "a message",
			event: message("$e", matrix.TypeMessage, alice, nil),
			want: []authcheck.Key{
				{Type: matrix.TypeCreate}, {Type: matrix.TypePowerLevels},
				{Type: matrix.TypeMember, StateKey: alice},
			},
		},
		{
			name:  "a join cites the target and the join rules",
			event: joinFor(bob, matrix.MembershipJoin, nil),
			want: []authcheck.Key{
				{Type: matrix.TypeCreate}, {Type: matrix.TypePowerLevels},
				{Type: matrix.TypeMember, StateKey: bob}, {Type: matrix.TypeJoinRules},
			},
		},
		{
			name:  "a join that authorises through another user cites that user too",
			event: joinFor(bob, matrix.MembershipJoin, matrix.Object{"join_authorised_via_users_server": carol}),
			want: []authcheck.Key{
				{Type: matrix.TypeCreate}, {Type: matrix.TypePowerLevels},
				{Type: matrix.TypeMember, StateKey: bob}, {Type: matrix.TypeJoinRules},
				{Type: matrix.TypeMember, StateKey: carol},
			},
		},
		{
			name:  "a leave does not cite the join rules",
			event: joinFor(bob, matrix.MembershipLeave, nil),
			want: []authcheck.Key{
				{Type: matrix.TypeCreate}, {Type: matrix.TypePowerLevels},
				{Type: matrix.TypeMember, StateKey: bob},
			},
		},
		{
			name: "a third-party invite cites the invite it claims",
			event: joinFor(bob, matrix.MembershipInvite, matrix.Object{
				"third_party_invite": matrix.Object{"signed": matrix.Object{"token": "t"}},
			}),
			want: []authcheck.Key{
				{Type: matrix.TypeCreate}, {Type: matrix.TypePowerLevels},
				{Type: matrix.TypeMember, StateKey: bob}, {Type: matrix.TypeJoinRules},
				{Type: matrix.TypeThirdPartyInvite, StateKey: "t"},
			},
		},
		{
			name:  "a join by a user into their own membership does not cite the target twice",
			event: joinFor(alice, matrix.MembershipJoin, nil),
			want: []authcheck.Key{
				{Type: matrix.TypeCreate}, {Type: matrix.TypePowerLevels},
				{Type: matrix.TypeMember, StateKey: alice}, {Type: matrix.TypeJoinRules},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := authcheck.Selection(state, tt.event)
			if len(got) != len(tt.want) {
				t.Fatalf("the selection named %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("the selection named %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// Builders used by the table above.

func stateEventWithID(id, typ, stateKey, sender string, content matrix.Object) *authcheck.Event {
	key := stateKey
	return &authcheck.Event{
		ID:             id,
		Type:           typ,
		StateKey:       &key,
		Sender:         sender,
		OriginServerTS: 1_700_000_000_000,
		Content:        content,
		AuthIDs:        nil,
	}
}

func message(id, typ, sender string, content matrix.Object) *authcheck.Event {
	return &authcheck.Event{
		ID:             id,
		Type:           typ,
		Sender:         sender,
		OriginServerTS: 1_700_000_000_000,
		Content:        content,
	}
}

func joinFor(target, membership string, extra matrix.Object) *authcheck.Event {
	content := matrix.Object{"membership": membership}
	for key, value := range extra {
		content[key] = value
	}
	return stateEventWithID("$m", matrix.TypeMember, target, target, content)
}

func levels2(id, sender string, content matrix.Object) *authcheck.Event {
	return stateEventWithID(id, matrix.TypePowerLevels, "", sender, content)
}

func mustCanonical(t *testing.T, object matrix.Object) []byte {
	t.Helper()
	body, err := matrix.CanonicalJSON(object)
	if err != nil {
		t.Fatalf("cannot canonicalise: %v", err)
	}
	return body
}

func sign(t *testing.T, priv ed25519.PrivateKey, body []byte) string {
	t.Helper()
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, body))
}
