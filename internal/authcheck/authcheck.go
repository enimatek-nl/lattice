// Package authcheck answers the question the authorization rules of a room version
// ask: may this event be in this room, given the state those rules read?
//
// The rules are the numbered list the room version chapters of the specification
// give ("Authorization Rules" in /rooms/v2/ and each later version's own copy of
// it, marked where it changed), and they are the reason two homeservers that have
// never met can agree that an event belongs. The same list runs three times over
// an event's life: against the event's own `auth_events`, against the state before
// the event, and against the current state of the room - a failure in that last
// one is a soft failure rather than a rejection, which is the caller's decision
// and not this package's. It also runs inside state resolution, which decides
// between two conflicting states by asking which events the rules allow. That is
// why the rules are a package of their own rather than a method on the service.
//
// Everything here is pure: it reads a snapshot of state and the events the state
// and the auth chains name, and it answers. Two rules cannot be answered that way,
// because they need a public key rather than a fact, and the caller enforces them:
//
//   - room version 9 and later, rule 4.2: a member event that names an authorising
//     user for a restricted join has to be signed by that user's server.
//   - the third-party invite signature comparison is in here, because the keys it
//     checks against travel in the room's own m.room.third_party_invite event.
package authcheck

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/power"
)

// Key names one entry of a room state: the specification's 2-tuple
// (event_type, state_key).
type Key struct {
	Type     string
	StateKey string
}

// String renders the key the way a log line or an error message wants it.
func (k Key) String() string { return k.Type + "/" + k.StateKey }

// State is a snapshot of room state: which event holds each (type, state_key).
type State map[Key]string

// Event is the part of an event the authorization rules read. It is deliberately
// neither the storage row nor the wire object: the rules are a pure function, and
// both of those types carry things the rules have no business reaching for.
type Event struct {
	ID     string
	RoomID string
	Type   string
	// StateKey is nil for a message event. A pointer to the empty string is a
	// state event: m.room.name, m.room.create and friends use the empty key.
	StateKey       *string
	Sender         string
	OriginServerTS int64
	// PrevIDs and AuthIDs are the event's own naming of the graph, which rules 1
	// and 2 read.
	PrevIDs []string
	AuthIDs []string
	// Content is the decoded content object.
	Content matrix.Object
	// Redacts names the target of an m.room.redaction event. Room versions before
	// 11 keep it as a property of the event, versions 11 and later under content,
	// and the v1/v2 redaction rule reads it.
	Redacts string
}

// StateKeyOf returns a state key pointer, for a caller that builds an event.
func StateKeyOf(key string) *string { return &key }

// StateKeyOfString reads a state key pointer back out, giving the empty string for a
// message event.
func StateKeyOfString(key *string) string {
	if key == nil {
		return ""
	}
	return *key
}

// IsState reports whether the event is a state event. The presence of the pointer
// is the whole test: a state key of "" is a state event.
func (e *Event) IsState() bool { return e.StateKey != nil }

// Key returns the state entry the event would occupy.
func (e *Event) Key() Key {
	key := Key{Type: e.Type}
	if e.StateKey != nil {
		key.StateKey = *e.StateKey
	}
	return key
}

// Membership returns the membership a member event names, or the empty string for
// any other event - or for a member event that names none, which rule 4 refuses.
func (e *Event) Membership() string {
	if e.Type != matrix.TypeMember {
		return ""
	}
	value, _ := e.Content["membership"].(string)
	return value
}

// RedactionTarget returns the event an m.room.redaction event points at, from
// whichever of the two places the room version keeps it.
func (e *Event) RedactionTarget() string {
	if e.Redacts != "" {
		return e.Redacts
	}
	if target, ok := e.Content["redacts"].(string); ok {
		return target
	}
	return ""
}

// Graph is everything besides the state that the rules may read: the events the
// state names, the events the auth chains name, and which of those this server has
// already rejected.
type Graph struct {
	// Version is the room version. It decides which list of rules applies and
	// which entries of it have changed.
	Version string
	// Events holds every event the state and the auth chains can reach, keyed by
	// event id. An id that is not in here is an event this server does not have.
	Events map[string]*Event
	// Rejected names the events that failed their auth checks, with the reason
	// beside the id. Rule 2 rejects any event that cites one of them.
	Rejected map[string]string
}

// NewGraph returns an empty graph for a room version.
func NewGraph(version string) *Graph {
	return &Graph{Version: version, Events: map[string]*Event{}, Rejected: map[string]string{}}
}

// Add records an event the rules may read.
func (g *Graph) Add(event *Event) {
	if event == nil || event.ID == "" {
		return
	}
	if g.Events == nil {
		g.Events = map[string]*Event{}
	}
	g.Events[event.ID] = event
}

// Lookup returns the event with that id, or nil when this server does not have it.
func (g *Graph) Lookup(eventID string) *Event {
	if eventID == "" || g.Events == nil {
		return nil
	}
	return g.Events[eventID]
}

// Reject records that an event was rejected, with the reason an operator reads.
func (g *Graph) Reject(eventID, reason string) {
	if g.Rejected == nil {
		g.Rejected = map[string]string{}
	}
	g.Rejected[eventID] = reason
}

// versionNumber reads the number a room version names itself by. Room versions are
// not ordered as strings, so the rules ask the number; a version this server does
// not know is treated as the newest set of rules it does know, which is the reading
// that refuses fewer events than inventing rules for a version nobody has written.
func (g *Graph) versionNumber() int {
	parsed, err := strconv.Atoi(strings.TrimSpace(g.Version))
	if err != nil {
		return 11
	}
	return parsed
}

// SupportsKnocking reports whether the room version knows the knock membership,
// which room version 7 introduced.
func (g *Graph) SupportsKnocking() bool { return g.versionNumber() >= 7 }

// SupportsRestrictedJoins reports whether the room version knows `restricted` join
// rules and the `join_authorised_via_users_server` field. Room version 8 introduced
// the join rule and version 9 the field, and they are treated together because a
// version 8 room has no way to name an authorising user.
func (g *Graph) SupportsRestrictedJoins() bool { return g.versionNumber() >= 8 }

// RequiresCreatorInCreate reports whether an m.room.create event has to name its
// creator in its content. Room version 11 removed the requirement because the
// event's sender says the same thing.
func (g *Graph) RequiresCreatorInCreate() bool { return g.versionNumber() < 11 }

// EnforcesRedactionRule reports whether redactions are still part of the
// authorization list. They were rule 11 of room versions 1 and 2 and are gone from
// version 3 on, where a redaction is authorised by power level like any message.
func (g *Graph) EnforcesRedactionRule() bool { return g.versionNumber() <= 2 }

// EnforcesAliasesRule reports whether an m.room.aliases event is checked against
// the sender's domain. The type is historical, and the rule is absent from the
// list in the versions this server creates rooms in.
func (g *Graph) EnforcesAliasesRule() bool { return g.versionNumber() <= 2 }

// RequiresIntegerPowerLevels reports whether the values of an m.room.power_levels
// event have to be integers. Room versions before 10 also accepted a string holding
// an integer, which is a compatibility rule from the earliest implementations.
func (g *Graph) RequiresIntegerPowerLevels() bool { return g.versionNumber() >= 10 }

// Check applies the room version's authorization rules to an event, including the rule
// that reads the event's own choice of auth events - the one that catches a sender
// picking a friendlier set of rules for itself. State resolution wants the rules
// without that rule, because re-deriving that choice is what it is there for; see
// [Rules]. A nil answer means the rules allow the event; an error names the rule that
// refused it, in words an operator can act on.
//
// `state` is the room state the rules read: the state before the event when the
// event is being judged, the state being resolved during state resolution, or the
// current state of the room for the soft-failure check. `graph` holds the events
// those states name and the events the auth chains name.
func Check(g *Graph, state State, event *Event) error {
	if g == nil || event == nil {
		return fmt.Errorf("there is nothing to check")
	}
	if event.ID == "" {
		return fmt.Errorf("an event has to be named before it can be authorised")
	}
	if event.Type == "" {
		return fmt.Errorf("an event must carry a type")
	}
	if event.Sender == "" {
		return fmt.Errorf("an event must name its sender")
	}
	if event.Content == nil {
		event.Content = matrix.Object{}
	}

	// Rule 1: the room's create event.
	if event.Type == matrix.TypeCreate {
		return checkCreate(g, event)
	}

	// Rule 2: the event's own auth events.
	if err := checkAuthEvents(g, state, event); err != nil {
		return err
	}
	return Rules(g, state, event)
}

// Rules applies the authorization rules to an event, without the rule that reads the
// event's own choice of auth events.
//
// State resolution needs this half. It builds a state from two claims about the room
// and asks, for each event, whether the rules allow it in the state assembled so far -
// and the state it assembles is not the state the event was created against, so the
// event's auth_events are exactly what the resolution is there to re-derive. Running
// that rule here would refuse almost every event under resolution. The specification
// says the same thing from the other side: rejected auth events are left out of the
// iterative checks, because auth events are not re-authenticated.
func Rules(g *Graph, state State, event *Event) error {
	if g == nil || event == nil {
		return fmt.Errorf("there is nothing to check")
	}
	if event.Content == nil {
		event.Content = matrix.Object{}
	}
	if event.Type == matrix.TypeCreate {
		return checkCreate(g, event)
	}

	// Rule 3: a room that does not federate holds only its own server's events.
	create := stateEvent(g, state, matrix.TypeCreate, "")
	if create != nil && federateDisabled(create) &&
		matrix.DomainOf(event.Sender) != matrix.DomainOf(create.Sender) {
		return fmt.Errorf("the room's create event sets m.federate to false, so only events "+
			"from %s belong in it, and this one is from %s",
			matrix.DomainOf(create.Sender), matrix.DomainOf(event.Sender))
	}

	// Rule 4 of room versions 1 and 2: an alias belongs to the server that made it.
	if event.Type == "m.room.aliases" && g.EnforcesAliasesRule() {
		if event.StateKey == nil {
			return fmt.Errorf("an m.room.aliases event must have a state key")
		}
		if matrix.DomainOf(event.Sender) != *event.StateKey {
			return fmt.Errorf("the aliases of %s cannot be named by %s", *event.StateKey, event.Sender)
		}
		return nil
	}

	// Rule 4: membership. This comes before the "sender must be joined" rule,
	// because an invite is precisely the event that lets a non-member into the
	// room, and each membership carries its own rules.
	if event.Type == matrix.TypeMember {
		return checkMember(g, state, event)
	}

	// Rule 5: anything else asks that the sender has joined.
	if membershipOf(g, state, event.Sender) != matrix.MembershipJoin {
		return fmt.Errorf("%s is not joined to the room", event.Sender)
	}

	levels := powerLevels(g, state)
	creator := roomCreator(g, state)

	// Rule 6: publishing a third-party invite is an inviter's act.
	if event.Type == matrix.TypeThirdPartyInvite {
		if level := power.Level(levels, event.Sender, creator); level < levels.Invite {
			return fmt.Errorf("publishing a third-party invite needs power level %d, %s has %d",
				levels.Invite, event.Sender, level)
		}
		return nil
	}

	// Rule 7: the required power level of the event type.
	required := power.EventRequired(levels, event.Type, event.IsState())
	if senderLevel := power.Level(levels, event.Sender, creator); required > senderLevel {
		return fmt.Errorf("sending %s needs power level %d, %s has %d",
			event.Type, required, event.Sender, senderLevel)
	}

	// Rule 8: a state key that names a user is that user's to write.
	if event.StateKey != nil && strings.HasPrefix(*event.StateKey, "@") && *event.StateKey != event.Sender {
		return fmt.Errorf("the state key %s is not the sender %s", *event.StateKey, event.Sender)
	}

	// Rule 9: changing the power levels has rules of its own, because a user who
	// can write them can hand themselves any power in the room.
	if event.Type == matrix.TypePowerLevels {
		return checkPowerLevelsChange(g, state, event, levels, creator)
	}

	// Rule 11 of room versions 1 and 2.
	if event.Type == matrix.TypeRedaction && g.EnforcesRedactionRule() {
		return checkRedaction(g, event, levels, creator)
	}
	return nil
}

// checkCreate applies rule 1.
func checkCreate(g *Graph, event *Event) error {
	if len(event.PrevIDs) > 0 {
		return fmt.Errorf("an m.room.create event cannot have prev_events: it is the room's first event")
	}
	if event.RoomID != "" && matrix.DomainOf(event.RoomID) != matrix.DomainOf(event.Sender) {
		return fmt.Errorf("the room %s and its creator %s belong to different servers",
			event.RoomID, event.Sender)
	}
	if version, present := event.Content["room_version"]; present {
		if _, isString := version.(string); !isString {
			return fmt.Errorf("content.room_version of an m.room.create event must be a string")
		}
	}
	if g.RequiresCreatorInCreate() {
		if creator, ok := event.Content["creator"].(string); !ok || creator == "" {
			return fmt.Errorf("an m.room.create event must name its creator in room version %s", g.Version)
		}
	}
	return nil
}

// checkAuthEvents applies rule 2: no duplicates, no rejected events, exactly the
// set the selection algorithm names, and a create event among them.
func checkAuthEvents(g *Graph, state State, event *Event) error {
	if len(event.AuthIDs) == 0 {
		return fmt.Errorf("the event cites no auth events, so the rules that read them cannot be run")
	}
	seen := map[Key]string{}
	for _, id := range event.AuthIDs {
		auth := g.Lookup(id)
		if auth == nil {
			return fmt.Errorf("the event cites auth event %s, which this server does not have", id)
		}
		if reason, rejected := g.Rejected[auth.ID]; rejected {
			return fmt.Errorf("the event cites auth event %s, which was rejected: %s", auth.ID, reason)
		}
		key := auth.Key()
		if other, duplicate := seen[key]; duplicate {
			return fmt.Errorf("the event cites two %s events (%s and %s)", key.Type, other, auth.ID)
		}
		seen[key] = auth.ID
	}
	if _, hasCreate := seen[Key{Type: matrix.TypeCreate}]; !hasCreate {
		return fmt.Errorf("the event's auth events hold no m.room.create event")
	}
	want := Selection(state, event)
	for _, key := range want {
		if _, ok := seen[key]; !ok {
			return fmt.Errorf("the event's auth events are missing %s", key)
		}
	}
	for key := range seen {
		if !contains(want, key) {
			return fmt.Errorf("the event cites a %s auth event, which the selection rules do not name", key.Type)
		}
	}
	return nil
}

// Selection returns the auth events the selection rules name for an event read
// against a state: the create event, the power levels, the sender's membership, the
// target's membership for a membership event, the join rule when the membership is
// a join, invite or knock, the third-party invite a membership claims, and the
// member that authorised a restricted join. A key the state does not hold names no
// event, so it is not selected - an event is not required to cite a state entry
// that was never there.
//
// This is the same list the service uses when it puts an event into the graph, and
// it has to stay the same list: a receiver re-runs the rules against exactly the
// events the sender chose, so a sender that chose otherwise has changed which rules
// apply to its own event.
func Selection(state State, event *Event) []Key {
	var want []Key
	selectKey := func(key Key) {
		if _, ok := state[key]; ok {
			want = append(want, key)
		}
	}
	selectKey(Key{Type: matrix.TypeCreate})
	selectKey(Key{Type: matrix.TypePowerLevels})
	selectKey(Key{Type: matrix.TypeMember, StateKey: event.Sender})
	if event.Type == matrix.TypeMember {
		target := ""
		if event.StateKey != nil {
			target = eStateKey(event)
		}
		if target != event.Sender {
			selectKey(Key{Type: matrix.TypeMember, StateKey: target})
		}
		switch event.Membership() {
		case matrix.MembershipJoin, matrix.MembershipInvite, matrix.MembershipKnock:
			selectKey(Key{Type: matrix.TypeJoinRules})
		}
		if event.Membership() == matrix.MembershipInvite {
			if token := thirdPartyToken(event.Content); token != "" {
				selectKey(Key{Type: matrix.TypeThirdPartyInvite, StateKey: token})
			}
		}
		if event.Membership() == matrix.MembershipJoin {
			if authorisedBy, ok := event.Content["join_authorised_via_users_server"].(string); ok &&
				authorisedBy != "" && authorisedBy != event.Sender {
				selectKey(Key{Type: matrix.TypeMember, StateKey: authorisedBy})
			}
		}
	}
	return want
}

// eStateKey returns the state key of a state event, or the empty string.
func eStateKey(event *Event) string {
	if event.StateKey == nil {
		return ""
	}
	return *event.StateKey
}

// contains reports whether a key is in a selection.
func contains(keys []Key, key Key) bool {
	for _, candidate := range keys {
		if candidate == key {
			return true
		}
	}
	return false
}

// checkMember applies the membership half of rule 4.
func checkMember(g *Graph, state State, event *Event) error {
	if event.StateKey == nil {
		return fmt.Errorf("an m.room.member event must have a state key")
	}
	target := *event.StateKey
	membership := event.Membership()
	if membership == "" {
		return fmt.Errorf("an m.room.member event must name a membership in its content")
	}
	levels := powerLevels(g, state)
	creator := roomCreator(g, state)
	senderLevel := power.Level(levels, event.Sender, creator)
	targetLevel := power.Level(levels, target, creator)
	senderMembership := membershipOf(g, state, event.Sender)
	targetMembership := membershipOf(g, state, target)
	authorisedBy, claimsAuthoriser := event.Content["join_authorised_via_users_server"].(string)
	if claimsAuthoriser && authorisedBy == "" {
		claimsAuthoriser = false
	}

	switch membership {
	case matrix.MembershipJoin:
		// The creator joining the room it just made is allowed before there is any
		// state to read. Room version 11 compares the sender of the create event;
		// earlier versions compare its content.creator, and the two are the same
		// user.
		if len(event.PrevIDs) == 1 && isCreateEvent(g, event.PrevIDs[0]) && stateKeyIsCreator(g, event) {
			return nil
		}
		if event.Sender != target {
			return fmt.Errorf("%s cannot join as %s", event.Sender, target)
		}
		if targetMembership == matrix.MembershipBan {
			return fmt.Errorf("%s is banned from the room", target)
		}
		switch rule := joinRule(g, state); rule {
		case matrix.JoinRulesPublic:
			// A public room asks nothing of the joiner. An authorising user named on
			// such a join is decoration: the room is open, and refusing an open join
			// because of a field its rules do not read is how a server ends up the only
			// one that could not read the room.
			return nil
		case matrix.JoinRulesInvite, matrix.JoinRulesKnock:
			if targetMembership == matrix.MembershipInvite || targetMembership == matrix.MembershipJoin {
				return nil
			}
			return fmt.Errorf("the room's join rule is %s and %s has not been invited (membership: %s)",
				rule, target, orNone(targetMembership))
		case matrix.JoinRulesRestricted, matrix.JoinRulesKnockRestricted:
			if !g.SupportsRestrictedJoins() {
				return fmt.Errorf("room version %s does not know the %s join rule", g.Version, rule)
			}
			if targetMembership == matrix.MembershipJoin || targetMembership == matrix.MembershipInvite {
				return nil
			}
			if !claimsAuthoriser {
				return fmt.Errorf("the room is joined through a space, and the event names no authorising user")
			}
			// Whether the authorising user's *server* signed the claim is a rule no
			// state snapshot can answer, so it is the caller's to enforce; what the
			// rules can say is that the authoriser has joined and may invite.
			if membershipOf(g, state, authorisedBy) != matrix.MembershipJoin {
				return fmt.Errorf("%s is not joined to the room, so it cannot authorise a join", authorisedBy)
			}
			if power.Level(levels, authorisedBy, creator) < levels.Invite {
				return fmt.Errorf("%s cannot invite, so it cannot authorise a restricted join", authorisedBy)
			}
			return nil
		default:
			return fmt.Errorf("the room's join rule %q is not one that authorises a join", rule)
		}

	case matrix.MembershipInvite:
		if thirdParty, ok := asObject(event.Content["third_party_invite"]); ok {
			return checkThirdPartyInvite(g, state, event, target, thirdParty)
		}
		if senderMembership != matrix.MembershipJoin {
			return fmt.Errorf("%s is not joined to the room, so it cannot invite anyone", event.Sender)
		}
		if targetMembership == matrix.MembershipJoin || targetMembership == matrix.MembershipBan {
			return fmt.Errorf("%s is already %s in the room", target, targetMembership)
		}
		if senderLevel >= levels.Invite {
			return nil
		}
		return fmt.Errorf("inviting needs power level %d, %s has %d", levels.Invite, event.Sender, senderLevel)

	case matrix.MembershipLeave:
		if event.Sender == target {
			switch targetMembership {
			case matrix.MembershipInvite, matrix.MembershipJoin:
				return nil
			case matrix.MembershipKnock:
				if g.SupportsKnocking() {
					return nil
				}
				return fmt.Errorf("room version %s knows no knock membership to retract", g.Version)
			default:
				return fmt.Errorf("%s cannot leave a room it is not in (membership: %s)", target, orNone(targetMembership))
			}
		}
		if senderMembership != matrix.MembershipJoin {
			return fmt.Errorf("%s is not joined to the room, so it cannot remove anyone", event.Sender)
		}
		if targetMembership == matrix.MembershipBan && senderLevel < levels.Ban {
			return fmt.Errorf("removing a banned user needs the ban level %d, %s has %d",
				levels.Ban, event.Sender, senderLevel)
		}
		if senderLevel >= levels.Kick && targetLevel < senderLevel {
			return nil
		}
		return fmt.Errorf("kicking needs power level %d and a level below the sender's %d, the target has %d",
			levels.Kick, senderLevel, targetLevel)

	case matrix.MembershipBan:
		if senderMembership != matrix.MembershipJoin {
			return fmt.Errorf("%s is not joined to the room, so it cannot ban anyone", event.Sender)
		}
		if senderLevel >= levels.Ban && targetLevel < senderLevel {
			return nil
		}
		return fmt.Errorf("banning needs power level %d and a target below the sender's %d, the target has %d",
			levels.Ban, senderLevel, targetLevel)

	case matrix.MembershipKnock:
		if !g.SupportsKnocking() {
			return fmt.Errorf("room version %s has no knock membership", g.Version)
		}
		if rule := joinRule(g, state); rule != matrix.JoinRulesKnock && rule != matrix.JoinRulesKnockRestricted {
			return fmt.Errorf("the room's join rule is %s, so nobody can knock", rule)
		}
		if event.Sender != target {
			return fmt.Errorf("%s cannot knock as %s", event.Sender, target)
		}
		switch targetMembership {
		case matrix.MembershipBan, matrix.MembershipInvite, matrix.MembershipJoin:
			return fmt.Errorf("%s is already %s in the room", target, targetMembership)
		default:
			return nil
		}

	default:
		return fmt.Errorf("%q is not a membership this server knows", membership)
	}
}

// checkThirdPartyInvite applies the third-party half of an invite: the invite the
// room published has to be there, the token has to match it, and the signature the
// identity server made has to verify against a key that invite published.
func checkThirdPartyInvite(g *Graph, state State, event *Event, target string, thirdParty matrix.Object) error {
	if membershipOf(g, state, target) == matrix.MembershipBan {
		return fmt.Errorf("%s is banned from the room", target)
	}
	signed, ok := asObject(thirdParty["signed"])
	if !ok {
		return fmt.Errorf("a third-party invite must carry a signed object")
	}
	mxID, _ := signed["mxid"].(string)
	token, _ := signed["token"].(string)
	if mxID == "" || token == "" {
		return fmt.Errorf("the signed object of a third-party invite must name an mxid and a token")
	}
	if mxID != target {
		return fmt.Errorf("the third-party invite is for %s, not for %s", mxID, target)
	}
	published := stateEvent(g, state, matrix.TypeThirdPartyInvite, token)
	if published == nil {
		return fmt.Errorf("the room holds no m.room.third_party_invite with state key %s", token)
	}
	if published.Sender != event.Sender {
		return fmt.Errorf("the third-party invite was published by %s, so %s cannot use it to invite",
			published.Sender, event.Sender)
	}
	keys := thirdPartyKeys(published.Content)
	if len(keys) == 0 {
		return fmt.Errorf("the m.room.third_party_invite for %s publishes no key to check against", token)
	}
	if !signedMatches(signed, keys) {
		return fmt.Errorf("no signature on the third-party invite matches a key of the room's invite for %s", token)
	}
	return nil
}

// thirdPartyKeys reads the public keys an m.room.third_party_invite publishes, in
// either of the two forms the specification allows.
func thirdPartyKeys(content matrix.Object) [][]byte {
	content = asObjectOr(content)
	var out [][]byte
	add := func(value any) {
		encoded, ok := value.(string)
		if !ok || encoded == "" {
			return
		}
		key, err := base64.RawStdEncoding.DecodeString(encoded)
		if err == nil && len(key) == ed25519.PublicKeySize {
			out = append(out, key)
		}
	}
	add(content["public_key"])
	if list, ok := content["public_keys"].([]any); ok {
		for _, entry := range list {
			object, ok := asObject(entry)
			if !ok {
				continue
			}
			add(object["public_key"])
		}
	}
	return out
}

// signedMatches reports whether any signature of the signed object verifies under
// any key the room's invite published. The signature is made over the canonical
// form of the signed object with the signatures removed, which is what keeps the
// identity server's statement checkable after a redaction.
func signedMatches(signed matrix.Object, keys [][]byte) bool {
	signatures, ok := asObject(signed["signatures"])
	if !ok {
		return false
	}
	without := matrix.Object{}
	for field, value := range signed {
		if field == "signatures" {
			continue
		}
		without[field] = value
	}
	canonical, err := matrix.CanonicalJSON(without)
	if err != nil {
		return false
	}
	for _, byServerAny := range signatures {
		byServer, ok := asObject(byServerAny)
		if !ok {
			continue
		}
		for _, valueAny := range byServer {
			encoded, ok := valueAny.(string)
			if !ok {
				continue
			}
			signature, err := base64.RawStdEncoding.DecodeString(encoded)
			if err != nil {
				continue
			}
			for _, key := range keys {
				if ed25519.Verify(key, canonical, signature) {
					return true
				}
			}
		}
	}
	return false
}

// checkPowerLevelsChange applies rule 9: a level can only be moved by somebody who
// holds both ends of the move.
func checkPowerLevelsChange(g *Graph, state State, event *Event, levels matrix.PowerLevelsContent, creator string) error {
	content := asObjectOr(event.Content)
	strict := g.RequiresIntegerPowerLevels()
	senderLevel := power.Level(levels, event.Sender, creator)
	fields := []string{"users_default", "events_default", "state_default", "ban", "redact", "kick", "invite"}

	// The shape of the content, which room version 10 tightened from "a string
	// holding an integer" to "an integer".
	for _, field := range fields {
		if value, present := content[field]; present {
			if _, err := intValue(value, strict); err != nil {
				return fmt.Errorf("content.%s of an m.room.power_levels event %v", field, err)
			}
		}
	}
	if value, present := content["events"]; present {
		object, ok := asObject(value)
		if !ok {
			return fmt.Errorf("content.events of an m.room.power_levels event must be an object")
		}
		for name, entry := range object {
			if _, err := intValue(entry, strict); err != nil {
				return fmt.Errorf("content.events.%s of an m.room.power_levels event %v", name, err)
			}
		}
	}
	newUsers := matrix.Object{}
	usersWritten := false
	if users, present := content["users"]; present {
		usersWritten = true
		object, ok := asObject(users)
		if !ok {
			return fmt.Errorf("content.users of an m.room.power_levels event must be an object")
		}
		for userID, entry := range object {
			if !strings.HasPrefix(userID, "@") {
				return fmt.Errorf("content.users of an m.room.power_levels event names %q, which is not a user id", userID)
			}
			value, err := intValue(entry, strict)
			if err != nil {
				return fmt.Errorf("content.users.%s of an m.room.power_levels event %v", userID, err)
			}
			newUsers[userID] = value
		}
	}

	if _, had := state[Key{Type: matrix.TypePowerLevels}]; !had {
		// A room with no levels yet has nothing to protect: writing the first set
		// is what the creator does when it makes the room.
		return nil
	}

	for _, field := range fields {
		current, had := powerField(levels, field)
		next, present := content[field]
		if !had && !present {
			continue
		}
		if present {
			want, err := intValue(next, strict)
			if err != nil {
				continue
			}
			if had && want == current {
				// Writing the same level again alters nothing.
				continue
			}
			if want > senderLevel {
				return fmt.Errorf("setting %s to %d needs power level %d, %s has %d",
					field, want, want, event.Sender, senderLevel)
			}
		}
		if had && current > senderLevel {
			return fmt.Errorf("changing or removing %s (%d) needs power level %d, %s has %d",
				field, current, current, event.Sender, senderLevel)
		}
	}
	nextEvents := matrix.Object{}
	if object, ok := asObject(content["events"]); ok {
		nextEvents = object
	}
	for name, current := range levels.Events {
		want, stillThere := nextEvents[name]
		if stillThere {
			if parsed, err := intValue(want, strict); err == nil && parsed == current {
				continue
			}
		}
		if current > senderLevel {
			return fmt.Errorf("changing or removing the level of %s (%d) needs power level %d, %s has %d",
				name, current, current, event.Sender, senderLevel)
		}
	}
	for name, entry := range nextEvents {
		want, err := intValue(entry, strict)
		if err != nil {
			continue
		}
		if want > senderLevel {
			return fmt.Errorf("setting the level of %s to %d needs power level %d, %s has %d",
				name, want, want, event.Sender, senderLevel)
		}
	}
	for userID, current := range levels.Users {
		if userID == event.Sender {
			continue
		}
		next, stillThere := newUsers[userID]
		if stillThere {
			if parsed, err := intValue(next, strict); err == nil && parsed == current {
				continue
			}
		} else if !usersWritten {
			// The event did not touch the users map at all.
			continue
		}
		if current >= senderLevel {
			return fmt.Errorf("changing or removing %s's power level (%d) needs more than %s has (%d)",
				userID, current, event.Sender, senderLevel)
		}
	}
	for userID, entry := range newUsers {
		want, err := intValue(entry, strict)
		if err != nil {
			continue
		}
		if current, had := levels.Users[userID]; had && current == want {
			// An entry the event leaves alone is not being added or changed.
			continue
		}
		if want > senderLevel {
			return fmt.Errorf("giving %s power level %d needs power level %d, %s has %d",
				userID, want, want, event.Sender, senderLevel)
		}
	}
	return nil
}

// checkRedaction applies rule 11 of room versions 1 and 2: a redaction is allowed
// by the redact level, or by the redaction and the target sharing a server.
func checkRedaction(g *Graph, event *Event, levels matrix.PowerLevelsContent, creator string) error {
	if power.Level(levels, event.Sender, creator) >= levels.Redact {
		return nil
	}
	target := event.RedactionTarget()
	if target == "" {
		return fmt.Errorf("an m.room.redaction event must name the event it redacts")
	}
	if matrix.DomainOf(target) != "" && matrix.DomainOf(target) == matrix.DomainOf(event.ID) {
		return nil
	}
	return fmt.Errorf("%s may not redact %s: it holds neither the redact level nor the target's server",
		event.Sender, target)
}

// powerLevels reads the levels the state holds.
func powerLevels(g *Graph, state State) matrix.PowerLevelsContent {
	return PowerLevelsOf(g, stateEvent(g, state, matrix.TypePowerLevels, ""))
}

// PowerLevelsOf returns the levels an m.room.power_levels event sets, with the
// specification's defaults restored for the keys the room left out. A room with no such
// event is a room where the creator administers and nobody else may do anything that
// needs a level.
//
// The state resolver sorts events by the power their senders held, and the authorization
// rules enforce the same levels; both read them through this function so that there is
// one answer to "what did the room's levels say".
func PowerLevelsOf(g *Graph, event *Event) matrix.PowerLevelsContent {
	if event == nil || event.Type != matrix.TypePowerLevels {
		return power.Defaults()
	}
	content := asObjectOr(event.Content)
	strict := g.RequiresIntegerPowerLevels()
	levels := power.Defaults()
	for _, pair := range []struct {
		field string
		dest  *int64
	}{
		{"invite", &levels.Invite},
		{"kick", &levels.Kick},
		{"ban", &levels.Ban},
		{"redact", &levels.Redact},
		{"events_default", &levels.SendDefault},
		{"state_default", &levels.StateDefault},
		{"users_default", &levels.UsersDefault},
	} {
		if value, ok := content[pair.field]; ok {
			if parsed, err := intValue(value, strict); err == nil {
				*pair.dest = parsed
			}
		}
	}
	levels.Events = map[string]int64{}
	if events, ok := asObject(content["events"]); ok {
		for name, entry := range events {
			if parsed, err := intValue(entry, strict); err == nil {
				levels.Events[name] = parsed
			}
		}
	}
	if users, ok := asObject(content["users"]); ok {
		levels.Users = map[string]int64{}
		for name, entry := range users {
			if parsed, err := intValue(entry, strict); err == nil {
				levels.Users[name] = parsed
			}
		}
	}
	if notifications, ok := asObject(content["notifications"]); ok {
		levels.Notifications = map[string]int64{}
		for name, entry := range notifications {
			if parsed, err := intValue(entry, strict); err == nil {
				levels.Notifications[name] = parsed
			}
		}
	}
	// The defaults came from power.Defaults, and only the keys the room actually
	// wrote replaced them. That is what lets a room set a level of 0 on purpose:
	// state_default of 0 means every member may change state, and reading it back
	// as the absent default would refuse events the room means to allow.
	return levels
}

// powerField reads one of the named levels, for the rule that asks what the value
// was before the change.
func powerField(levels matrix.PowerLevelsContent, field string) (int64, bool) {
	switch field {
	case "users_default":
		return levels.UsersDefault, true
	case "events_default":
		return levels.SendDefault, true
	case "state_default":
		return levels.StateDefault, true
	case "ban":
		return levels.Ban, true
	case "redact":
		return levels.Redact, true
	case "kick":
		return levels.Kick, true
	case "invite":
		return levels.Invite, true
	}
	return 0, false
}

// intValue reads an integer out of decoded JSON. Room versions before 10 also
// allowed the value to be spelled as a string holding an integer, a compatibility
// rule from the earliest implementations that version 10 did away with.
func intValue(value any, strict bool) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("is not a whole number: %s", typed.String())
		}
		return parsed, nil
	case float64:
		if typed != float64(int64(typed)) {
			return 0, fmt.Errorf("is not a whole number: %v", typed)
		}
		return int64(typed), nil
	case string:
		if strict {
			return 0, fmt.Errorf("is the string %q, which this room version does not accept as a number", typed)
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("is the string %q, which is not an integer", typed)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("is not a number: %v", value)
	}
}

// membershipOf returns the membership a user holds in the room the state describes,
// or the empty string when the room holds no member event for them.
func membershipOf(g *Graph, state State, userID string) string {
	event := stateEvent(g, state, matrix.TypeMember, userID)
	if event == nil {
		return ""
	}
	return event.Membership()
}

// joinRule returns the room's join rule, defaulting to invite: a room with no
// m.room.join_rules event is a room you have to be invited to.
func joinRule(g *Graph, state State) string {
	event := stateEvent(g, state, matrix.TypeJoinRules, "")
	if event == nil {
		return matrix.JoinRulesInvite
	}
	rule, _ := asObjectOr(event.Content)["join_rule"].(string)
	if rule == "" {
		return matrix.JoinRulesInvite
	}
	return rule
}

// roomCreator returns the room's creator as the state names it. Room version 11
// dropped the content.creator property, so the sender of the create event answers
// for every version.
func roomCreator(g *Graph, state State) string {
	event := stateEvent(g, state, matrix.TypeCreate, "")
	if event == nil {
		return ""
	}
	return event.Sender
}

// federateDisabled reports whether a create event says the room does not federate.
func federateDisabled(create *Event) bool {
	value, ok := asObjectOr(create.Content)["m.federate"].(bool)
	return ok && !value
}

// stateEvent returns the event a state entry points at, or nil when the state holds
// no such entry or this server does not have the event it names.
func stateEvent(g *Graph, state State, typ, stateKey string) *Event {
	id, ok := state[Key{Type: typ, StateKey: stateKey}]
	if !ok || id == "" {
		return nil
	}
	return g.Lookup(id)
}

// isCreateEvent reports whether an event this server holds is a create event.
func isCreateEvent(g *Graph, eventID string) bool {
	event := g.Lookup(eventID)
	return event != nil && event.Type == matrix.TypeCreate
}

// stateKeyIsCreator reports whether the event's state key names the creator of the
// room, which is how rule 4 lets a creator join the room it just made.
func stateKeyIsCreator(g *Graph, event *Event) bool {
	if event.StateKey == nil {
		return false
	}
	for _, prevID := range event.PrevIDs {
		create := g.Lookup(prevID)
		if create == nil || create.Type != matrix.TypeCreate {
			continue
		}
		if g.RequiresCreatorInCreate() {
			creator, _ := asObjectOr(create.Content)["creator"].(string)
			return creator == *event.StateKey
		}
		return create.Sender == *event.StateKey
	}
	return false
}

// thirdPartyToken reads the token a third-party invite claims.
func thirdPartyToken(content matrix.Object) string {
	thirdParty, ok := asObject(content["third_party_invite"])
	if !ok {
		return ""
	}
	signed, ok := asObject(thirdParty["signed"])
	if !ok {
		return ""
	}
	token, _ := signed["token"].(string)
	return token
}

// asObject reads a decoded JSON object however a decoder spelled it.
func asObject(value any) (matrix.Object, bool) {
	if object, ok := value.(matrix.Object); ok {
		return object, true
	}
	if object, ok := value.(map[string]any); ok {
		return object, true
	}
	return nil, false
}

// asObjectOr returns the object, or an empty one.
func asObjectOr(value any) matrix.Object {
	if object, ok := asObject(value); ok {
		return object
	}
	return matrix.Object{}
}

// orNone renders an absent value for an error message.
func orNone(value string) string {
	if value == "" {
		return "none"
	}
	return value
}
