package eventauth

import (
	"errors"
	"fmt"
	"github.com/enimatek/lattice/internal/events"
	"sort"
	"strings"

	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Key identifies a piece of room state the way Matrix does: by event type and
// state key.
type Key struct {
	Type     string
	StateKey string
}

func (k Key) String() string { return k.Type + "/" + k.StateKey }

// Snapshot is the room state an event is authorised against, plus the resolution
// of event IDs. It is an interface so the same rules run over an in-memory state
// (unit tests, iterative auth checks over a small auth chain) and over the
// database, without the rules knowing which.
type Snapshot interface {
	// State returns the current state event for the key.
	State(key Key) (map[string]any, bool)
	// Event resolves an event ID to the event itself. Authorisation needs this for
	// the auth-event checks and for the "only previous event is the create event"
	// rule; a snapshot that cannot resolve an ID makes those checks fail rather
	// than pass.
	Event(id string) (map[string]any, bool)
	// Rejected reports whether an event was rejected under the checks performed on
	// receipt of a PDU. Rejected auth events must not authorise anything.
	Rejected(id string) bool
}

// Opts carries the checks that authorisation needs but room state cannot answer.
// Leaving one nil makes the matching rule reject rather than skip, because trusting
// an unchecked signature is how a forged join or invitation gets into a room.
type Opts struct {
	// VerifyServerSignature reports whether the event carries a valid signature
	// from the named homeserver. Used for content.join_authorised_via_users_server.
	VerifyServerSignature func(event map[string]any, serverName string) error

	// VerifyThirdPartyInvite reports whether the signed invite in the member event
	// matches a public key published by the m.room.third_party_invite event.
	VerifyThirdPartyInvite func(event, thirdPartyInvite map[string]any) error

	// JoinedInRoom reports whether a user is a joined member of another room. The
	// restricted join-rule allow list needs it. See MayJoinRestricted.
	JoinedInRoom func(roomID, userID string) (bool, error)

	// AllowUnsignedTemplate relaxes rule 4.2 for the unsigned event template a resident server
	// hands to a joining server. A template cannot carry the authoriser's signature: it is not an
	// event yet, and the server that will seal it has not agreed to it. The signature is checked
	// where it exists — when the signed event arrives at /send_join — so skipping it here costs
	// nothing that is not checked again on the way in, and requiring it here would make every
	// restricted room unjoinable from anywhere but the server that holds it.
	//
	// This is only ever correct for a template this server is building. It must never be set for
	// an event arriving from a peer or being persisted.
	AllowUnsignedTemplate bool
}

// Failure says which rule rejected an event. The rule numbers are the ones in the
// specification's per-version authorisation lists, so a failure can be checked
// against the text without guessing.
// RuleAllowLookup names not a rule of the room but a check this server could not carry out: the
// membership an allow room would have to confirm could not be read. It is kept apart from the room's
// own rules because the answer it deserves is "ask a server that can see that room", not "this user
// is not allowed".
const RuleAllowLookup = "allow lookup"

type Failure struct {
	Rule   string
	Reason string
}

func (f Failure) Error() string { return "auth rule " + f.Rule + ": " + f.Reason }

// AsFailure unpacks a rule failure.
func AsFailure(err error) (Failure, bool) {
	var f Failure
	if errors.As(err, &f) {
		return f, true
	}
	return Failure{}, false
}

// IsUnjudgeable says whether an auth failure is a verdict about the event or an admission that
// this server could not make one yet. Rule 2.2 — an auth event the event cites cannot be
// resolved — is the second kind: the missing event may arrive a second later and the same event
// would then be perfectly good. A server that files a rejection for it turns a gap into a
// permanent ban, and then refuses every later event that cites it.
func IsUnjudgeable(err error) bool {
	failure, ok := AsFailure(err)
	return ok && failure.Rule == "2.2"
}

// ErrRulesetUnsupported is returned for a room version whose authorisation table
// this server does not implement. It is an error rather than a silent fallback:
// authorising version 1 rooms with the version 3 rules would invent state.
var ErrRulesetUnsupported = errors.New("eventauth: authorisation rules for this room version are not implemented")

// Membership values used by the rules.
const (
	MembershipJoin   = "join"
	MembershipInvite = "invite"
	MembershipLeave  = "leave"
	MembershipBan    = "ban"
	MembershipKnock  = "knock"
)

// Join rule values.
const (
	JoinRulePublic          = "public"
	JoinRuleInvite          = "invite"
	JoinRuleKnock           = "knock"
	JoinRuleRestricted      = "restricted"
	JoinRuleKnockRestricted = "knock_restricted"
)

// Event types the rules name directly.
const (
	TypeCreate           = "m.room.create"
	TypeMember           = "m.room.member"
	TypeJoinRules        = "m.room.join_rules"
	TypePowerLevels      = "m.room.power_levels"
	TypeThirdPartyInvite = "m.room.third_party_invite"
)

// AdditionalCreatorsKey is the create-event content key by which room version 12 names
// creators beyond the sender (MSC4289). Each of them holds infinite power in the room.
const AdditionalCreatorsKey = "additional_creators"

// JACSKey is the content key naming the user whose signature authorises a
// restricted join.
const JACSKey = "join_authorised_via_users_server"

// AllowConditionRoomMembership is the only allow condition the specification defines for a
// `restricted` join rule: the joining user must be a *joined* member of the room the condition names.
const AllowConditionRoomMembership = "m.room_membership"

// Check applies the room version's authorisation rules to an event, given the room
// state it is being authorised against. A nil return means the event is allowed.
//
// This is the contract federation depends on: two servers that disagree here derive
// different accepted histories, so the rules are implemented in the order and with
// the wording of the specification page named in each comment.
func Check(rules roomversion.Rules, event map[string]any, state Snapshot, opts Opts) error {
	switch rules.AuthRuleset {
	case "v1", "v2", "v3", "v4", "v5", "v6":
	default:
		return fmt.Errorf("%w: ruleset %q", ErrRulesetUnsupported, rules.AuthRuleset)
	}

	eventType, _ := stringOf(event, "type")
	roomID, _ := stringOf(event, "room_id")
	sender, _ := stringOf(event, "sender")
	if eventType == "" || sender == "" {
		return Failure{"0", "event is missing type or sender"}
	}
	// Room version 12 is the first version where an event may legitimately arrive without a room
	// identifier: its `m.room.create` event carries none, because the room's identifier is that
	// event's own identifier written with `!` instead of `$` (MSC4291). Requiring the field here
	// would reject the one event a room is made of before any rule got to read it.
	if roomID == "" && (!rules.DomainlessRoomIDs || eventType != TypeCreate) {
		return Failure{"0", "event is missing room_id"}
	}

	// Rule 1 (v3-v11 page, rule 1): the create event stands on its own.
	if eventType == TypeCreate {
		return checkCreate(rules, event, roomID, sender)
	}

	// Room version 12 rule 2 (MSC4291): the room identifier on an event has to be the create event's
	// own identifier written with `!`. It is the first time in the protocol that a room identifier is
	// checkable rather than agreed, and it is what a create event with no room_id of its own makes
	// possible: whoever names a room has to be holding bytes that hash to it.
	//
	// The check runs where a create event is in reach, and the two places that authorise an event
	// differ in whether one is. Against *room state* — the iterative checks of state resolution, a
	// client event authorised over the current state — it is there, and the check catches a room whose
	// create event has been rewritten under the identifier its other events still carry. Against an
	// event's own *auth chain* it is not there and cannot be, because this same version forbids an
	// event from citing the create event (rule 3.2) on the grounds that its room identifier already
	// names it. A clause that demanded the create event in the auth chain would refuse every event in
	// every valid version 12 room, which is what this one did on its first contact with one, in run
	// 20261108v12after.
	//
	// Nothing is left unguarded by that. On the receipt paths a room's identity is established from
	// the hash of its create event before any of the room's events are stored — bundle.
	// roomOfNamelessCreate refuses a bundle handed over as one room whose create-event bytes make
	// another, and an event naming a room this server does not hold is refused as a room this server
	// does not hold. This clause is the second look, taken from inside a state that has one.
	if rules.DomainlessRoomIDs {
		if create, ok := createEventOf(rules, roomID, state); ok {
			want, err := events.RoomIDFromCreate(rules, create)
			if err != nil {
				return Failure{"2", "the room's create event cannot name its own room: " + err.Error()}
			}
			if want != roomID {
				return Failure{"2", fmt.Sprintf("the room ID %s is not the identifier this room's create event hashes to (%s)", roomID, want)}
			}
		}
	}

	// Rule 2: the auth events themselves.
	if err := checkAuthEvents(rules, event, state); err != nil {
		return err
	}

	// Rule 3: a room that opted out of federation accepts no foreign events.
	if create, ok := createEventOf(rules, roomID, state); ok {
		if content := contentOf(create); content != nil {
			if federate, ok := content["m.federate"].(bool); ok && !federate {
				if serverName(sender) != serverName(stringOfOr(create["sender"], "")) {
					return Failure{"3", "room declares m.federate false and the sender is on another server"}
				}
			}
		}
	}

	// Rule 4: membership events have their own table. Room versions 1 and 2 use the
	// same table for the membership values those versions have (join, invite, leave,
	// ban): their page differs from the later one only in rules that concern knock,
	// restricted joins and the third-party invite sub-clauses, and each of those is
	// gated below by the room version's capability flags.
	if eventType == TypeMember {
		return checkMember(rules, event, state, opts)
	}

	// The historic m.room.aliases rule (room versions up to 5): a server may
	// publish its own aliases without being joined or having a power level, which
	// is how an administrator keeps their server's alias list current. Room version
	// 6 dropped this special case, so aliases become ordinary state events there.
	// gomatrixserverlib still applies the special case to every room version, while
	// Synapse gates it by version as the specification does; we follow the
	// specification and Synapse.
	if eventType == "m.room.aliases" && rules.SpecialCaseAliases {
		stateKey, ok := stringOf(event, "state_key")
		if !ok {
			return Failure{"aliases.1", "an aliases event must be a state event"}
		}
		if stateKey != serverName(sender) {
			return Failure{"aliases.2", "the aliases state key is not the sender's server name"}
		}
		return nil
	}

	// Rule 5: everything else requires the sender to be joined.
	if Membership(state, sender) != MembershipJoin {
		return Failure{"5", "sender is not joined to the room"}
	}

	pl, err := powerLevelsFor(rules, roomID, state)
	if err != nil {
		return err
	}

	// Rule 6: third-party invite events.
	if eventType == TypeThirdPartyInvite {
		if pl.User(sender) >= pl.Invite {
			return nil
		}
		return Failure{"6", "sender power level is below the invite level"}
	}

	// Rule 7: the power level required for this event type.
	_, isState := event["state_key"]
	if pl.ForEvent(eventType, isState) > pl.User(sender) {
		return Failure{"7", fmt.Sprintf("event type %s needs power level %d, sender has %d",
			eventType, pl.ForEvent(eventType, isState), pl.User(sender))}
	}

	// Rule 8: a state key that names a user must name the sender.
	if stateKey, ok := stringOf(event, "state_key"); ok && strings.HasPrefix(stateKey, "@") && stateKey != sender {
		return Failure{"8", "state key names another user"}
	}

	// Rule 9: power level changes.
	if eventType == TypePowerLevels {
		return checkPowerLevels(rules, event, state, pl)
	}

	// Rule 10: allow.
	return nil
}

// checkCreate implements rule 1 of the room version pages for versions 3 through
// 11, and the privileged-creator adjustment of version 12.
func checkCreate(rules roomversion.Rules, event map[string]any, roomID, sender string) error {
	if prev := events.ReferencedIDs(event, "prev_events"); len(prev) > 0 {
		return Failure{"1.1", "a create event must have no prev_events"}
	}
	if rules.DomainlessRoomIDs {
		// Room version 12 rewrote this clause. Where versions up to 11 asked that the room ID's
		// domain be the sender's domain, version 12 asks that the create event carry no room ID at
		// all, because the identifier is derived from these bytes: `!` plus the hash of this event.
		// The two rules cannot both be satisfied, which is the whole reason a create event in this
		// version looks different — and why a create event that brings its own room ID is not a
		// version difference but a forged room ID, naming a room these events do not create.
		if _, carriesRoomID := stringOf(event, "room_id"); carriesRoomID {
			return Failure{"1.2", "a create event in this room version must not carry a room_id"}
		}
	} else if serverName(roomID) != serverName(sender) {
		return Failure{"1.2", "the room ID domain does not match the sender domain"}
	}
	content := contentOf(event)
	if version, ok := stringOf(content, "room_version"); ok {
		if !roomversion.IsKnown(version) {
			return Failure{"1.3", "unrecognised room version " + version}
		}
	}
	// Room version 12 rule 1.4 (MSC4289): the room's creators are the sender plus the users this
	// event names, and each of them holds infinite power in the room from the moment it exists.
	// A value that is not a user ID is therefore not a malformed optional field — it is an
	// attempt to hand unlimited power to something the grammar does not accept as a person.
	if rules.PrivilegedCreators {
		if raw, named := content[AdditionalCreatorsKey]; named {
			list, ok := raw.([]any)
			if !ok {
				return Failure{"1.4", "content.additional_creators is not an array"}
			}
			for _, entry := range list {
				userID, ok := entry.(string)
				if !ok {
					return Failure{"1.4", "content.additional_creators holds a value that is not a string"}
				}
				if err := matrixid.UserValid(userID); err != nil {
					return Failure{"1.4", "content.additional_creators holds an invalid user ID: " + err.Error()}
				}
			}
		}
	}
	creator, hasCreator := stringOf(content, "creator")
	switch {
	case rules.CreatorInCreateContent && !hasCreator:
		// Room versions 3 through 10 require the creator property.
		return Failure{"1.4", "create event has no content.creator"}
	case hasCreator && rules.PrivilegedCreators && creator != sender:
		// Room version 12: a creator may be named, but only as the sender.
		return Failure{"1.5", "content.creator does not match the sender"}
	}
	if stateKey, ok := stringOf(event, "state_key"); ok && stateKey != "" {
		return Failure{"1.6", "a create event must have an empty state key"}
	}
	return nil
}

// checkAuthEvents implements rule 2.
func checkAuthEvents(rules roomversion.Rules, event map[string]any, state Snapshot) error {
	authIDs := events.ReferencedIDs(event, "auth_events")
	if len(authIDs) > 10 {
		return Failure{"2.0", "more than ten auth events"}
	}
	roomID, _ := stringOf(event, "room_id")

	seen := make(map[Key]bool, len(authIDs))
	var got []Key
	for _, id := range authIDs {
		// A refused event is asked about before it is looked up, and that order is not a
		// detail. A server that refuses an event does not keep it, so looking it up first
		// would answer "cannot be resolved" — which is true but is not the verdict, and it
		// loses the distinction the specification draws between an auth event nobody has
		// seen and one that was seen and refused. The second is what makes an event that
		// cites it invalid, and only the first is a gap someone could still fill.
		if state.Rejected(id) {
			return Failure{"2.3", "auth event " + id + " was rejected on receipt"}
		}
		ev, ok := state.Event(id)
		if !ok {
			return Failure{"2.2", "auth event " + id + " cannot be resolved, so it cannot be checked"}
		}
		typ, _ := stringOf(ev, "type")
		stateKey, _ := stringOf(ev, "state_key")
		key := Key{typ, stateKey}
		if seen[key] {
			return Failure{"2.1", "duplicate auth event for " + key.String()}
		}
		seen[key] = true
		if authRoom, _ := stringOf(ev, "room_id"); authRoom != roomID {
			return Failure{"2.5", "auth event " + id + " belongs to another room"}
		}
		got = append(got, key)
	}

	want := SelectKeys(rules, event, state)
	if !sameKeys(got, want) {
		return Failure{"2.2", "auth events are " + keyList(got) + ", the selection algorithm gives " + keyList(want)}
	}
	// Room versions up to 11 require the create event to be among the auth events by name. Room
	// version 12 forbids it, and forbids it twice over: the selection algorithm leaves it out, and
	// rule 3.2 of that page refuses an event whose auth events do not match the selection. The room
	// ID every version 12 event carries *is* the create event's identifier, so citing it would say
	// the same thing in two fields that can then disagree. Selection already reflects that; this is
	// the other half, and the label follows the page the rule is on.
	if rules.DomainlessRoomIDs {
		if seen[Key{TypeCreate, ""}] {
			return Failure{"3.2", "a create event is not one of this room version's auth events"}
		}
	} else if !seen[Key{TypeCreate, ""}] {
		return Failure{"2.4", "no m.room.create event among the auth events"}
	}
	return nil
}

// SelectKeys returns the state keys the auth events selection algorithm picks for an
// event. "if any" entries are only included when the state actually holds them,
// which is what makes the comparison in rule 2.2 meaningful.
func SelectKeys(rules roomversion.Rules, event map[string]any, state Snapshot) []Key {
	// The create event is the root of the room: it is not authorised by other state.
	if eventType, _ := stringOf(event, "type"); eventType == TypeCreate {
		return nil
	}

	// Room version 12 (MSC4291) takes the create event out of the selection entirely, because the
	// room ID — which every event in the room carries, and which is the create event's own
	// identifier — cites it instead. Listing it anyway is a violation of rule 3.2, which compares
	// an event's auth_events against exactly this list.

	var keys []Key
	listed := map[Key]bool{}
	add := func(k Key) {
		// The selection algorithm names both "the sender's member event" and "the
		// target's member event", which are the same event when a user acts on their
		// own membership. Listing it twice would make a correct event look like it
		// carries duplicate auth events.
		if listed[k] {
			return
		}
		if _, ok := state.State(k); ok {
			listed[k] = true
			keys = append(keys, k)
		}
	}

	if !rules.DomainlessRoomIDs {
		add(Key{TypeCreate, ""})
	}
	add(Key{TypePowerLevels, ""})

	sender, _ := stringOf(event, "sender")
	add(Key{TypeMember, sender})

	if eventType, _ := stringOf(event, "type"); eventType == TypeMember {
		stateKey, _ := stringOf(event, "state_key")
		add(Key{TypeMember, stateKey})
		content := contentOf(event)
		membership, _ := stringOf(content, "membership")
		switch membership {
		case MembershipJoin, MembershipInvite, MembershipKnock:
			add(Key{TypeJoinRules, ""})
		}
		if membership == MembershipInvite {
			if tpi, ok := content["third_party_invite"].(map[string]any); ok {
				if signed, ok := tpi["signed"].(map[string]any); ok {
					if token, ok := stringOf(signed, "token"); ok {
						add(Key{TypeThirdPartyInvite, token})
					}
				}
			}
		}
		if membership == MembershipJoin && rules.SupportsRestrictedJoin {
			if authoriser, ok := stringOf(content, JACSKey); ok {
				add(Key{TypeMember, authoriser})
			}
		}
	}
	return keys
}

// SelectEventIDs returns the event IDs the selection algorithm picks, which is what
// an outgoing event should put in auth_events.
func SelectEventIDs(rules roomversion.Rules, event map[string]any, state Snapshot) []string {
	keys := SelectKeys(rules, event, state)
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		ev, ok := state.State(key)
		if !ok {
			continue
		}
		// The room versions that derive their identifiers from the event do not necessarily carry
		// one — room version 12 events do not — and an outgoing event still has to cite its auth
		// events by the names every receiver computes for itself. Where the field is absent the name
		// is worked out here; where it is present it is the name the origin server chose, in the
		// versions that let one be chosen, and no hash could reconstruct it.
		//
		// An event whose identifier cannot be computed is left out of the selection, which is loud
		// one step later: rule 2.2 compares an event's auth_events against this list, so an event
		// citing an identifier this server cannot name is refused rather than quietly matched.
		if id, ok := stringOf(ev, "event_id"); ok {
			ids = append(ids, id)
			continue
		}
		if computed, err := events.EventID(rules, ev); err == nil {
			ids = append(ids, computed)
		}
	}
	sort.Strings(ids)
	return ids
}

// checkMember implements rule 4.
func checkMember(rules roomversion.Rules, event map[string]any, state Snapshot, opts Opts) error {
	stateKey, hasStateKey := stringOf(event, "state_key")
	content := contentOf(event)
	membership, hasMembership := stringOf(content, "membership")
	sender, _ := stringOf(event, "sender")
	// Needed to find the room's create event, which room version 12 leaves out of the very set this
	// function is handed as its state.
	roomID, _ := stringOf(event, "room_id")

	// 4.1
	if !hasStateKey || !hasMembership {
		return Failure{"4.1", "member event is missing a state key or a membership"}
	}

	// 4.2: a restricted join carries the authorising user's signature.
	authoriser, hasAuthoriser := stringOf(content, JACSKey)
	if hasAuthoriser {
		if !rules.SupportsRestrictedJoin {
			return Failure{"4.2", "room version " + rules.ID + " does not support restricted joins"}
		}
		switch {
		case opts.AllowUnsignedTemplate:
			// The template a resident server hands to a joining server cannot carry this
			// signature, because the servers that will sign it have not agreed to it yet. The
			// check is made where the signature exists, when the signed event arrives.
		case opts.VerifyServerSignature == nil:
			return Failure{"4.2", "no signature verifier available for join_authorised_via_users_server"}
		default:
			if err := opts.VerifyServerSignature(event, serverName(authoriser)); err != nil {
				return Failure{"4.2", "join_authorised_via_users_server is not validly signed: " + err.Error()}
			}
		}
	}

	pl, err := powerLevelsFor(rules, roomID, state)
	if err != nil {
		return err
	}
	joinRule := joinRuleOf(rules, state)
	currentSender := Membership(state, sender)
	currentTarget := Membership(state, stateKey)

	switch membership {
	case MembershipJoin:
		// 4.3.1: the room creator's first join needs no invitation. This clause only ever
		// allows; a join that does not match it falls through to the ordinary rules.
		//
		// It used to refuse when the single prev_event could not be resolved here, and that
		// was wrong in a way that only federation exposed: prev_events are not auth_events,
		// so a receiver checking an event against the auth events its sender cited can almost
		// never resolve them, and every remote join but the creator's own was refused.
		if prev := events.ReferencedIDs(event, "prev_events"); len(prev) == 1 {
			if create, ok := state.Event(prev[0]); ok {
				if typ, _ := stringOf(create, "type"); typ == TypeCreate && stateKey == creatorOf(rules, create) {
					return nil
				}
			}
		}
		// 4.3.2
		if sender != stateKey {
			return Failure{"4.3.2", "a join event must be sent by the user it joins"}
		}
		// 4.3.3
		if currentTarget == MembershipBan {
			return Failure{"4.3.3", "the user is banned"}
		}
		// 4.3.4
		switch joinRule {
		case JoinRuleInvite, JoinRuleKnock:
			if currentTarget == MembershipInvite || currentTarget == MembershipJoin {
				return nil
			}
			return Failure{"4.3.4", "join rule " + joinRule + " requires an invitation"}
		case JoinRuleRestricted, JoinRuleKnockRestricted:
			if !ruleSupportsJoinRule(rules, joinRule) {
				return Failure{"4.3.4", "room version " + rules.ID + " does not support join rule " + joinRule}
			}
			// 4.3.5.1
			if currentTarget == MembershipJoin || currentTarget == MembershipInvite {
				return nil
			}
			// 4.3.5.2
			if !hasAuthoriser {
				return Failure{"4.3.5.2", "a restricted join needs join_authorised_via_users_server"}
			}
			if Membership(state, authoriser) != MembershipJoin {
				return Failure{"4.3.5.2", "the authorising user is not a joined member of this room"}
			}
			if pl.User(authoriser) < pl.Invite {
				return Failure{"4.3.5.2", "the authorising user cannot invite"}
			}
			// 4.3.5.3
			return nil
		case JoinRulePublic:
			// 4.3.6
			return nil
		default:
			// 4.3.7
			return Failure{"4.3.7", "join rule " + joinRule + " does not allow this join"}
		}

	case MembershipInvite:
		// 4.4.1: third-party invites.
		if thirdParty, ok := content["third_party_invite"].(map[string]any); ok {
			if currentTarget == MembershipBan {
				return Failure{"4.4.1.1", "the target user is banned"}
			}
			signed, ok := thirdParty["signed"].(map[string]any)
			if !ok {
				return Failure{"4.4.1.2", "third_party_invite has no signed property"}
			}
			mxid, hasMXID := stringOf(signed, "mxid")
			token, hasToken := stringOf(signed, "token")
			if !hasMXID || !hasToken {
				return Failure{"4.4.1.3", "signed invite is missing mxid or token"}
			}
			if mxid != stateKey {
				return Failure{"4.4.1.4", "signed invite names another user"}
			}
			invite, ok := state.State(Key{TypeThirdPartyInvite, token})
			if !ok {
				return Failure{"4.4.1.5", "no m.room.third_party_invite event with that token"}
			}
			if inviteSender, _ := stringOf(invite, "sender"); inviteSender != sender {
				return Failure{"4.4.1.6", "sender does not match the third-party invite event sender"}
			}
			if opts.VerifyThirdPartyInvite == nil {
				return Failure{"4.4.1.7", "no third-party invite verifier available"}
			}
			if err := opts.VerifyThirdPartyInvite(event, invite); err != nil {
				return Failure{"4.4.1.7", "third-party invite signature does not verify: " + err.Error()}
			}
			return nil
		}
		// 4.4.2
		if currentSender != MembershipJoin {
			return Failure{"4.4.2", "the sender is not joined"}
		}
		// 4.4.3
		if currentTarget == MembershipJoin || currentTarget == MembershipBan {
			return Failure{"4.4.3", "the target user is already joined or banned"}
		}
		// 4.4.4
		if pl.User(sender) >= pl.Invite {
			return nil
		}
		return Failure{"4.4.5", "sender power level is below the invite level"}

	case MembershipLeave:
		// 4.5.1: leaving is allowed for a user who is invited, joined or knocking.
		// Room versions 1 and 2 have no knock membership, so for them the state a
		// user can leave from is invite or join, which is what their page says.
		if sender == stateKey {
			switch currentTarget {
			case MembershipInvite, MembershipJoin:
				return nil
			case MembershipKnock:
				if !rules.SupportsKnock {
					return Failure{"4.5.1", "room version " + rules.ID + " has no knock membership"}
				}
				return nil
			default:
				return Failure{"4.5.1", "the user is not in a state that can be left"}
			}
		}
		// 4.5.2
		if currentSender != MembershipJoin {
			return Failure{"4.5.2", "the sender is not joined"}
		}
		// 4.5.3
		if currentTarget == MembershipBan && pl.User(sender) < pl.Ban {
			return Failure{"4.5.3", "kicking a banned user needs the ban level"}
		}
		// 4.5.4
		if pl.User(sender) >= pl.Kick && pl.User(stateKey) < pl.User(sender) {
			return nil
		}
		return Failure{"4.5.5", "sender cannot kick the target user"}

	case MembershipBan:
		// 4.6.1
		if currentSender != MembershipJoin {
			return Failure{"4.6.1", "the sender is not joined"}
		}
		// 4.6.2
		if pl.User(sender) >= pl.Ban && pl.User(stateKey) < pl.User(sender) {
			return nil
		}
		return Failure{"4.6.3", "sender cannot ban the target user"}

	case MembershipKnock:
		// 4.7.1: knocking exists only where the join rule allows it.
		if joinRule != JoinRuleKnock && joinRule != JoinRuleKnockRestricted {
			return Failure{"4.7.1", "join rule " + joinRule + " does not allow knocking"}
		}
		if joinRule == JoinRuleKnock && !rules.SupportsKnock {
			return Failure{"4.7.1", "room version " + rules.ID + " has no knock join rule"}
		}
		if joinRule == JoinRuleKnockRestricted && !rules.SupportsKnockRestricted {
			return Failure{"4.7.1", "room version " + rules.ID + " has no knock_restricted join rule"}
		}
		// 4.7.2
		if sender != stateKey {
			return Failure{"4.7.2", "a knock event must be sent by the user it names"}
		}
		// 4.7.3
		switch currentTarget {
		case MembershipBan, MembershipInvite, MembershipJoin:
			return Failure{"4.7.4", "the user is already banned, invited or joined"}
		default:
			return nil
		}

	default:
		// 4.8
		return Failure{"4.8", "unknown membership " + membership}
	}
}

// checkPowerLevels implements rule 9: power can only move sideways or downwards for
// other people, and a level can only be handed out by someone who already holds it.
func checkPowerLevels(rules roomversion.Rules, event map[string]any, state Snapshot, current *PowerLevels) error {
	content := contentOf(event)
	sender, _ := stringOf(event, "sender")
	senderLevel := current.User(sender)

	if rules.EnforceIntegerPowerLevels {
		if _, err := LoadPowerLevels(rules, content); err != nil {
			return Failure{"9.1", err.Error()}
		}
	}

	// Room version 12 rule 10.4 (MSC4289): a creator's power level is not something a power levels
	// event may set, at any level, not even the level they already hold. The clause is placed here
	// — ahead of "no previous power levels event, allow", which is the version 11 numbering's 10.5 —
	// because the specification orders it that way and the difference is real: the first power
	// levels event of a room is otherwise unconditional, and in room version 12 it still may not
	// list the room's creator. Numbered as the version 12 page numbers it, since that is the page
	// this rule exists on.
	if rules.PrivilegedCreators {
		if create, ok := createEventOf(rules, stringOfOr(event["room_id"], ""), state); ok {
			if users, ok := stringKeyedMap(content["users"]); ok {
				for _, creator := range creatorsOf(rules, create) {
					if _, named := users[creator]; named {
						return Failure{"10.4", "a power levels event may not name a room creator"}
					}
				}
			}
		}
	}

	// 9.4: the first power levels event in the room is unconditional.
	if _, ok := state.State(Key{TypePowerLevels, ""}); !ok {
		return nil
	}

	next, err := LoadPowerLevels(rules, content)
	if err != nil {
		return Failure{"9.1", err.Error()}
	}

	// 9.5: the seven scalar levels.
	scalars := []struct {
		key     string
		current int
		future  int
	}{
		{"users_default", current.UsersDefault, next.UsersDefault},
		{"events_default", current.EventsDefault, next.EventsDefault},
		{"state_default", current.StateDefault, next.StateDefault},
		{"ban", current.Ban, next.Ban},
		{"redact", current.Redact, next.Redact},
		{"kick", current.Kick, next.Kick},
		{"invite", current.Invite, next.Invite},
	}
	for _, s := range scalars {
		_, hadIt := content[s.key]
		if hadIt || s.current != s.future {
			if s.current > senderLevel {
				return Failure{"9.5.1", "current " + s.key + " is above the sender's power level"}
			}
			if s.future > senderLevel {
				return Failure{"9.5.2", "new " + s.key + " is above the sender's power level"}
			}
		}
	}

	// 9.6 and 9.7: the events and notifications maps.
	for _, m := range []struct {
		name    string
		current map[string]int
		future  map[string]int
	}{{"events", current.Events, next.Events}, {"notifications", current.Notifications, next.Notifications}} {
		for eventType, old := range m.current {
			newValue, stillThere := m.future[eventType]
			if !stillThere || newValue != old {
				// 9.6.1
				if old > senderLevel {
					return Failure{"9.6.1", "current " + m.name + " level for " + eventType + " is above the sender's"}
				}
			}
		}
		for eventType, newValue := range m.future {
			if old, existed := m.current[eventType]; !existed || old != newValue {
				// 9.7.1
				if newValue > senderLevel {
					return Failure{"9.7.1", "new " + m.name + " level for " + eventType + " is above the sender's"}
				}
			}
		}
	}

	// 9.8: taking away or lowering someone else's level.
	for userID, old := range current.Users {
		newValue, stillThere := next.Users[userID]
		if userID == sender {
			continue
		}
		if !stillThere || newValue != old {
			if old >= senderLevel {
				return Failure{"9.8.1", "cannot change the level of a user who is not below the sender"}
			}
		}
	}
	// 9.9: granting or raising a level.
	for userID, newValue := range next.Users {
		old, existed := current.Users[userID]
		if !existed || old != newValue {
			if newValue > senderLevel {
				return Failure{"9.9.1", "cannot grant a power level above the sender's own"}
			}
		}
	}

	// 9.10
	return nil
}

// MayJoinRestricted applies the restricted join-rule allow list, which the
// per-version authorisation rules deliberately do not evaluate. It belongs on the
// path that decides whether this server may create and send a join of its own, not
// inside Check: the auth rules are the federation contract, and rejecting an event
// a peer's rules accept would split the room's history.
func MayJoinRestricted(rules roomversion.Rules, event map[string]any, state Snapshot, opts Opts) error {
	joinRule := joinRuleOf(rules, state)
	if joinRule != JoinRuleRestricted && joinRule != JoinRuleKnockRestricted {
		return nil
	}
	if !ruleSupportsJoinRule(rules, joinRule) {
		return Failure{"allow", "room version " + rules.ID + " does not support join rule " + joinRule}
	}
	create, ok := state.State(Key{TypeJoinRules, ""})
	if !ok {
		return nil
	}
	content := contentOf(create)
	allow, _ := content["allow"].([]any)
	if len(allow) == 0 {
		return Failure{"allow", "restricted join rule has no allow entries"}
	}
	roomID, _ := stringOf(event, "room_id")
	stateKey, _ := stringOf(event, "state_key")
	for _, entry := range allow {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		// `m.room_membership` is the whole of the condition vocabulary the protocol has today,
		// and the exact string matters: an entry of any other type is not a condition this server
		// is allowed to treat as satisfied, however much it resembles one. A room that grows a
		// second condition type in a later version must refuse here rather than guess.
		kind, _ := stringOf(item, "type")
		target, _ := stringOf(item, "room_id")
		if kind != AllowConditionRoomMembership || target == "" || target == roomID {
			continue
		}
		if opts.JoinedInRoom == nil {
			return Failure{"allow", "no membership lookup available for the allow list"}
		}
		joined, err := opts.JoinedInRoom(target, stateKey)
		if err != nil {
			// A check this server could not carry out is not the room refusing. It is named by a
			// rule of its own so that the caller can answer "I cannot confirm this" instead of
			// filing a verdict about a room whose state it cannot see.
			return Failure{RuleAllowLookup, "the allow list could not be checked: " + err.Error()}
		}
		if joined {
			return nil
		}
	}
	return Failure{"allow", "the user is not joined to any room in the allow list"}
}

// Membership returns a user's current membership in the snapshot. A missing member
// event means the user is not in the room; the rules treat that as "leave" without
// there being a leave event.
func Membership(state Snapshot, userID string) string {
	if userID == "" {
		return ""
	}
	ev, ok := state.State(Key{TypeMember, userID})
	if !ok {
		return MembershipLeave
	}
	membership, ok := stringOf(contentOf(ev), "membership")
	if !ok {
		return MembershipLeave
	}
	return membership
}

// joinRuleOf returns the effective join rule. A missing or malformed
// m.room.join_rules event means "invite", which is the safe reading: it refuses
// joins rather than inventing an open room.
func joinRuleOf(rules roomversion.Rules, state Snapshot) string {
	ev, ok := state.State(Key{TypeJoinRules, ""})
	if !ok {
		return JoinRuleInvite
	}
	rule, ok := stringOf(contentOf(ev), "join_rule")
	if !ok {
		return JoinRuleInvite
	}
	switch rule {
	case JoinRuleKnock:
		if !rules.SupportsKnock {
			return JoinRuleInvite
		}
	case JoinRuleRestricted:
		if !rules.SupportsRestrictedJoin {
			return JoinRuleInvite
		}
	case JoinRuleKnockRestricted:
		if !rules.SupportsKnockRestricted {
			return JoinRuleInvite
		}
	}
	return rule
}

func ruleSupportsJoinRule(rules roomversion.Rules, rule string) bool {
	switch rule {
	case JoinRulePublic, JoinRuleInvite:
		return true
	case JoinRuleKnock:
		return rules.SupportsKnock
	case JoinRuleRestricted:
		return rules.SupportsRestrictedJoin
	case JoinRuleKnockRestricted:
		return rules.SupportsKnockRestricted && rules.SupportsKnock
	default:
		return false
	}
}

// MaxPowerLevel is the largest power level a room can express: the maximum value
// representable exactly in JSON (2^53 - 1), which is also the ceiling the room
// version rules put on depth.
const MaxPowerLevel = 9007199254740991

// CreatorPowerLevel is what the power level machinery reports for a room creator in a version that
// makes them privileged, and it is one more than the largest level a room can express. That is not
// decoration. MSC4289 says a creator's power is infinite and, in the same breath, that another user can
// be given any power level the room can hold — including 2^53-1, which Complement hands out deliberately
// (`creator can kick admin at JSON max value`). Clamp the creator to the ceiling and the two tie, and
// every comparison that has to be strict — "the target's level must be below the sender's" for a kick, a
// ban, an unban, and the rule 10 comparisons over `users` — then refuses the creator their own room.
//
// The value is never written into an event: rule 10.4 keeps creators out of `users` in the first place,
// which is what lets the authority be a fact about the room rather than a number in its state.
const CreatorPowerLevel = MaxPowerLevel + 1

// powerLevelsFor loads the room's current power levels.
//
// A room with no m.room.power_levels event is not a room where nobody has power:
// the creator is treated as holding MaxPowerLevel, otherwise the first power levels
// event could never be sent (rule 7 asks for state_default, 50, and there is nothing
// to grant it). Synapse and gomatrixserverlib both do this; gomatrixserverlib cites
// Synapse's auth.py directly for it. The number matters: it decides whether a
// creator can set levels above 100 in that first event.
func powerLevelsFor(rules roomversion.Rules, roomID string, state Snapshot) (*PowerLevels, error) {
	create, hasCreate := createEventOf(rules, roomID, state)
	ev, ok := state.State(Key{TypePowerLevels, ""})
	if !ok {
		pl := DefaultPowerLevels()
		if hasCreate {
			for _, creator := range creatorsOf(rules, create) {
				pl.Users[creator] = CreatorPowerLevel
			}
		}
		return pl, nil
	}
	pl, err := LoadPowerLevels(rules, contentOf(ev))
	if err != nil {
		return pl, Failure{"state", "the room's m.room.power_levels event is not valid: " + err.Error()}
	}
	// Room version 12 (MSC4289): a creator's power level is infinite, and it is not the room's to
	// take away. Rule 10.4 refuses a power levels event that names a creator at all, so a room
	// whose rules this server checked itself cannot reach this line with a creator in `users` —
	// but the state this server authorises against can arrive from a server that did not check it,
	// and a creator silently demoted to zero is a room whose owner can no longer do anything. The
	// level is therefore asserted here rather than trusted from the event, which is also what makes
	// it immune to a room that predates the rule.
	if rules.PrivilegedCreators && hasCreate {
		for _, creator := range creatorsOf(rules, create) {
			pl.Users[creator] = CreatorPowerLevel
		}
	}
	return pl, nil
}

// creatorsOf names every user the room treats as its creator. Room versions up to 11 have one:
// content.creator, or the sender where the version does not carry the field. Room version 12
// (MSC4289) adds `content.additional_creators`, whose users share the creator's infinite power and
// its immunity to being demoted, so they have to be carried through every power level question
// rather than only the create event's own validation.
// CreatorsOf names every user a room treats as its creator under its room version.
//
// Exported for one caller, and the reason is a disagreement rather than a convenience. The rooms service
// answers a question that is really this package's — whether a named user could have invited somebody —
// and it answered it from the room's power levels event alone. A room version 12 creator is not in that
// event, because rule 10.4 of the version refuses an event that names a creator, so the service found
// nobody, used the default of zero, and refused the one user the version says outranks everyone: a
// version 12 room's own creator could not invite anybody into it through a restricted join rule while the
// authorisation rules here were treating that same user as infinite.
//
// The fix is not for the service to keep a second, narrower copy of the rules. It is for both to ask the
// same function.
func CreatorsOf(rules roomversion.Rules, create map[string]any) []string {
	return creatorsOf(rules, create)
}

func creatorsOf(rules roomversion.Rules, create map[string]any) []string {
	primary := creatorOf(rules, create)
	if !rules.PrivilegedCreators {
		return []string{primary}
	}
	creators := []string{primary}
	seen := map[string]bool{primary: true}
	if raw, named := contentOf(create)[AdditionalCreatorsKey]; named {
		if list, ok := raw.([]any); ok {
			for _, entry := range list {
				if userID, ok := entry.(string); ok && !seen[userID] {
					seen[userID] = true
					creators = append(creators, userID)
				}
			}
		}
	}
	return creators
}

// createEventOf returns the room's m.room.create event, which several rules need and room
// version 12 goes out of its way to make hard to obtain.
//
// In every earlier version the create event is one of an event's auth events, so a snapshot built
// from what an event cites holds it. Version 12 forbids citing it — the room identifier *is* its
// identifier, with `!` where an event identifier carries `$` — so the same snapshot does not, and
// every rule that reaches for the creator of the room quietly finds nobody: a room's creator
// arrives with the power level of a stranger, and their first power levels event is refused for
// wanting a level they hold by the fact that they made the room. That is not a corner case; it is
// the room's first event, and it is what run 20261108v12after died on.
//
// The specification supplies the substitute in the same breath as the rule that removes it: the
// room ID "being the m.room.create event's ID" implies the create event. So where the state does not
// carry one, ask the snapshot for the event whose identifier is this room's identifier under `$`.
// A snapshot that cannot resolve that is answering honestly that it does not know what room this
// is, and the callers treat it as not knowing, which is the same answer the create event being
// absent has always meant here.
func createEventOf(rules roomversion.Rules, roomID string, state Snapshot) (map[string]any, bool) {
	if create, ok := state.State(Key{TypeCreate, ""}); ok {
		return create, true
	}
	if rules.DomainlessRoomIDs && strings.HasPrefix(roomID, "!") {
		if create, ok := state.Event("$" + roomID[1:]); ok {
			if eventType, _ := stringOf(create, "type"); eventType == TypeCreate {
				return create, true
			}
		}
	}
	return nil, false
}

// creatorOf identifies a room's creator for the room version: room versions up to
// 10 name it in content.creator, later versions use the sender.
func creatorOf(rules roomversion.Rules, create map[string]any) string {
	if rules.CreatorInCreateContent {
		if creator, ok := stringOf(contentOf(create), "creator"); ok {
			return creator
		}
	}
	return stringOfOr(create["sender"], "")
}

func serverName(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return ""
}

func stringOfOr(v any, fallback string) string {
	s, ok := v.(string)
	if !ok {
		return fallback
	}
	return s
}

func sameKeys(a, b []Key) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[Key]bool, len(a))
	for _, k := range a {
		set[k] = true
	}
	for _, k := range b {
		if !set[k] {
			return false
		}
	}
	return true
}

func keyList(keys []Key) string {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k.String())
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// MemoryState is a Snapshot backed by maps, used by tests and by local operations
// over a small, known set of events.
type MemoryState struct {
	byKey    map[Key]map[string]any
	byID     map[string]map[string]any
	rejected map[string]bool
}

// NewMemoryState indexes the given events.
func NewMemoryState(events ...map[string]any) *MemoryState {
	s := &MemoryState{
		byKey:    map[Key]map[string]any{},
		byID:     map[string]map[string]any{},
		rejected: map[string]bool{},
	}
	for _, ev := range events {
		s.Add(ev)
	}
	return s
}

// Add indexes one event by ID, and by state key when it has one.
func (s *MemoryState) Add(event map[string]any) {
	typ, _ := stringOf(event, "type")
	if id, ok := stringOf(event, "event_id"); ok {
		s.byID[id] = event
	}
	if stateKey, ok := stringOf(event, "state_key"); ok {
		s.byKey[Key{typ, stateKey}] = event
	}
}

// Reject marks an event as rejected on receipt, for rule 2.3.
func (s *MemoryState) Reject(eventID string) { s.rejected[eventID] = true }

// State implements [Snapshot].
func (s *MemoryState) State(key Key) (map[string]any, bool) {
	ev, ok := s.byKey[key]
	return ev, ok
}

// Event implements [Snapshot].
func (s *MemoryState) Event(eventID string) (map[string]any, bool) {
	ev, ok := s.byID[eventID]
	return ev, ok
}

// Rejected implements [Snapshot].
func (s *MemoryState) Rejected(eventID string) bool { return s.rejected[eventID] }
