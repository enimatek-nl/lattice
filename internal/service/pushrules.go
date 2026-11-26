package service

import (
	"context"
	"encoding/json"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/push"
)

// PushRuleKinds are the push rule kinds in evaluation order; the list belongs to
// the rule engine in internal/push, which is also what sorts rules by it.
var PushRuleKinds = push.Kinds

// defaultRulesVersion names the default rule set this build seeds.
//
// It is written to the account beside the rules it describes, and
// RepairDefaultPushRules compares an account against it. Bump it whenever
// defaultPushRules gains or moves a rule, or when the conditions of one change,
// so accounts written by an older binary are brought up once.
const defaultRulesVersion = 3

// The keys the default rules of v1.7 and later name carry an escaped dot: the
// property is called m.mentions, one name, and a plain dot would read it as a path
// through an object called m. The appendices spell this out under "Dot-separated
// property paths".
const (
	keyMentionsUserIDs = `content.m\.mentions.user_ids`
	keyMentionsRoom    = `content.m\.mentions.room`
	keyRelatesToRel    = `content.m\.relates_to.rel_type`
)

// defaultPushRules are the server default rules seeded for every new account.
// They follow the "Default Ruleset" of the specification the server advertises
// (v1.11 in /versions), in the order that section lists them, because that order
// is the order a client shows and the order rules are evaluated in.
//
// userID is not decoration: `.m.rule.is_user_mention` names the recipient inside a
// stored condition, which is the only way a rule that is a row can say "my own
// Matrix ID". `.m.rule.contains_user_name` needs the same of its pattern: the
// specification writes it as "[the local part of the user's Matrix ID]" and servers
// substitute it, and a content rule without a pattern is a rule a strict client
// cannot read at all.
func defaultPushRules(userID string) []persist.PushRule {
	dontNotify := matrix.Array{"dont_notify"}
	silent := matrix.Array{}
	count := matrix.Array{"notify", matrix.Object{"set_tweak": "count"}}
	sound := func(name string) matrix.Array {
		return matrix.Array{"notify", matrix.Object{"set_tweak": "sound", "value": name}}
	}
	// mentionActions and roomMentionActions are the two action lists the v1.7 mention
	// rules carry: a user mention makes a sound and highlights, a room mention only
	// highlights.
	mentionActions := matrix.Array{"notify",
		matrix.Object{"set_tweak": "sound", "value": "default"},
		matrix.Object{"set_tweak": "highlight"}}
	roomMentionActions := matrix.Array{"notify", matrix.Object{"set_tweak": "highlight"}}
	condition := func(entries ...matrix.Object) string {
		out := matrix.Array{}
		for _, entry := range entries {
			out = append(out, entry)
		}
		return string(matrix.Encode(out))
	}
	rules := []persist.PushRule{
		{Kind: "override", RuleID: ".m.rule.master", Enabled: false, Actions: string(matrix.Encode(dontNotify))},
		{Kind: "override", RuleID: ".m.rule.suppress_notices", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: condition(matrix.Object{"kind": "event_match", "key": "content.msgtype", "pattern": "m.notice"})},
		{Kind: "override", RuleID: ".m.rule.invite_for_me", Enabled: true, Actions: string(matrix.Encode(count)),
			Conditions: condition(matrix.Object{"kind": "member_count"})},
		{Kind: "override", RuleID: ".m.rule.member_event", Enabled: true, Actions: string(matrix.Encode(dontNotify))},
		{Kind: "override", RuleID: ".m.rule.is_user_mention", Enabled: true, Actions: string(matrix.Encode(mentionActions)),
			Conditions: condition(matrix.Object{"kind": "event_property_contains",
				"key": keyMentionsUserIDs, "value": userID})},
		{Kind: "override", RuleID: ".m.rule.contains_display_name", Enabled: true,
			Actions:    string(matrix.Encode(mentionActions)),
			Conditions: condition(matrix.Object{"kind": push.ConditionContainsDisplayName})},
		{Kind: "override", RuleID: ".m.rule.is_room_mention", Enabled: true, Actions: string(matrix.Encode(roomMentionActions)),
			Conditions: condition(
				matrix.Object{"kind": "event_property_is", "key": keyMentionsRoom, "value": true},
				matrix.Object{"kind": "sender_notification_permission", "key": "room"})},
		{Kind: "override", RuleID: ".m.rule.roomnotif", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: condition(matrix.Object{"kind": "event_match", "key": "content.body", "pattern": "@room"})},
		{Kind: "override", RuleID: ".m.rule.tombstone", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: condition(
				matrix.Object{"kind": "event_match", "key": "type", "pattern": "m.room.tombstone"},
				matrix.Object{"kind": "event_match", "key": "state_key", "pattern": ""})},
		{Kind: "override", RuleID: ".m.rule.reaction", Enabled: true, Actions: string(matrix.Encode(dontNotify))},
		{Kind: "override", RuleID: ".m.rule.room.server_acl", Enabled: true, Actions: string(matrix.Encode(silent)),
			Conditions: condition(
				matrix.Object{"kind": "event_match", "key": "type", "pattern": "m.room.server_acl"},
				matrix.Object{"kind": "event_match", "key": "state_key", "pattern": ""})},
		{Kind: "override", RuleID: ".m.rule.suppress_edits", Enabled: true, Actions: string(matrix.Encode(silent)),
			Conditions: condition(matrix.Object{"kind": "event_property_is",
				"key": keyRelatesToRel, "value": "m.replace"})},
		{Kind: "content", RuleID: ".m.rule.contains_user_name", Enabled: true,
			Actions: string(matrix.Encode(mentionActions)),
			// A content rule's one condition is a pattern on content.body, and it is stored
			// that way: the wire form reads the pattern back out of it.
			Conditions: condition(matrix.Object{"kind": "event_match", "key": "content.body",
				"pattern": matrix.LocalpartOf(userID)})},
		{Kind: "underride", RuleID: ".m.rule.call", Enabled: true, Actions: string(matrix.Encode(sound("ring"))),
			Conditions: condition(matrix.Object{"kind": "event_match", "key": "type", "pattern": "m.call.invite"})},
		{Kind: "underride", RuleID: ".m.rule.room_one_to_one", Enabled: true, Actions: string(matrix.Encode(sound("default"))),
			Conditions: condition(matrix.Object{"kind": "room_member_count", "is": "2"})},
		{Kind: "underride", RuleID: ".m.rule.encrypted_room_one_to_one", Enabled: true,
			Actions: string(matrix.Encode(sound("default"))),
			Conditions: condition(
				matrix.Object{"kind": "room_member_count", "is": "2"},
				matrix.Object{"kind": "event_match", "key": "type", "pattern": "m.room.encrypted"})},
		{Kind: "underride", RuleID: ".m.rule.message", Enabled: true, Actions: string(matrix.Encode(sound("default"))),
			Conditions: condition(matrix.Object{"kind": "event_match", "key": "content.msgtype", "pattern": "m.*"})},
		{Kind: "underride", RuleID: ".m.rule.encrypted", Enabled: true, Actions: string(matrix.Encode(count)),
			Conditions: condition(matrix.Object{"kind": "event_match", "key": "type", "pattern": "m.room.encrypted"})},
		{Kind: "underride", RuleID: ".m.rule.default", Enabled: true, Actions: string(matrix.Encode(count)),
			Conditions: condition(matrix.Object{"kind": "event_match", "key": "content.msgtype", "pattern": "m.notice"})},
		{Kind: "underride", RuleID: ".m.rule.notice", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: condition(matrix.Object{"kind": "sender_notification_permission", "key": "m.notice"})},
	}
	for i := range rules {
		rules[i].Default = true
		rules[i].Positions = int64(i)
	}
	return rules
}

// SeedPushRules installs the default rule set for a user.
func (s *Service) SeedPushRules(ctx context.Context, userID string) error {
	for _, rule := range defaultPushRules(userID) {
		rule.UserID = userID
		if err := s.db.PushRuleRepo.Set(&rule); err != nil {
			return matrix.ErrInternal(err)
		}
	}
	if err := s.db.UserRepo.SetPushRulesVersion(userID, defaultRulesVersion); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.publishPushRules(ctx, userID)
}

// RepairDefaultPushRules gives every account the default rules it was never seeded
// with.
//
// Default rules are rows like any other, written once when the account is created, so a
// server that learns a rule of the specification keeps answering 404 for it on every
// account that existed the day before. What a person sees is a settings screen that
// cannot turn room mentions on, on the accounts they actually use and on no other.
//
// The account's User.PushRulesVersion says whether it holds the set this build seeds.
// That marker, rather than a sweep of every start, is what keeps a rule a person deleted
// deleted: a stored default rule that is absent cannot be told apart from one this server
// never wrote, so the server can only promise the set once per rule set per account.
func (s *Service) RepairDefaultPushRules(ctx context.Context) error {
	count, err := s.db.UserRepo.Count()
	if err != nil {
		return matrix.ErrInternal(err)
	}
	if count == 0 {
		return nil
	}
	users, err := s.db.UserRepo.List(int(count), 0)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	for _, user := range users {
		if user.PushRulesVersion >= defaultRulesVersion {
			continue
		}
		changed, err := s.addMissingDefaultRules(user.ID)
		if err != nil {
			return err
		}
		if err := s.db.UserRepo.SetPushRulesVersion(user.ID, defaultRulesVersion); err != nil {
			return matrix.ErrInternal(err)
		}
		// Clients read the rule set out of the m.push_rules account data as well as from
		// the endpoint, so a rule that arrived by repair has to reach that copy too.
		if changed {
			if err := s.publishPushRules(ctx, user.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// addMissingDefaultRules writes the default rules an account does not have and puts them
// where the specification puts them. Everything already there keeps its row, its flags
// and its place: a repair that reset the rules it came to add would also undo whatever a
// person had switched off.
func (s *Service) addMissingDefaultRules(user string) (bool, error) {
	rows, err := s.db.PushRuleRepo.List(user, "")
	if err != nil {
		return false, matrix.ErrInternal(err)
	}
	storedRules := make(map[string]*persist.PushRule, len(rows))
	order := map[string][]string{}
	for _, row := range rows {
		storedRules[ruleKey(row.Kind, row.RuleID)] = row
		order[row.Kind] = append(order[row.Kind], row.RuleID)
	}
	// The specification's order per kind, so a rule that arrives late can be put where it
	// would have been had the account been written today.
	wanted := map[string][]string{}
	for _, rule := range defaultPushRules(user) {
		wanted[rule.Kind] = append(wanted[rule.Kind], rule.RuleID)
	}
	moved := map[string]bool{}
	changed := false
	for _, rule := range defaultPushRules(user) {
		stored := storedRules[ruleKey(rule.Kind, rule.RuleID)]
		if stored != nil && !stored.Default {
			// The id is taken by a rule this account wrote itself, so there is no default
			// rule of that name left to repair.
			continue
		}
		if stored != nil {
			// The conditions of a default rule are the one part of it a person cannot change:
			// no endpoint writes them. That makes them the part an older binary can have stored
			// wrong, and the part this may correct without undoing anything anybody asked for.
			if stored.Conditions == rule.Conditions {
				continue
			}
			stored.Conditions = rule.Conditions
			if err := s.db.PushRuleRepo.Set(stored); err != nil {
				return changed, matrix.ErrInternal(err)
			}
			changed = true
			continue
		}
		rule.UserID = user
		if err := s.db.PushRuleRepo.Set(&rule); err != nil {
			return changed, matrix.ErrInternal(err)
		}
		storedRules[ruleKey(rule.Kind, rule.RuleID)] = &rule
		order[rule.Kind] = placeAfterNeighbour(order[rule.Kind], wanted[rule.Kind], rule.RuleID)
		moved[rule.Kind] = true
		changed = true
	}
	for kind, ids := range order {
		if !moved[kind] {
			continue
		}
		if err := s.db.PushRuleRepo.Order(user, kind, ids); err != nil {
			return changed, matrix.ErrInternal(err)
		}
	}
	return changed, nil
}

// ruleKey addresses a rule the way the stored rows do: a kind and an id together, since
// the same id may exist in two kinds.
func ruleKey(kind, ruleID string) string { return kind + " " + ruleID }

// placeAfterNeighbour inserts a rule behind the last default rule the specification puts
// before it, which is the place it had in the list this account never got. Rules a person
// wrote themselves are left where they are: they were placed by a before or after
// parameter, and moving them to make a repair tidier would move them out from under the
// client that asked for that place.
func placeAfterNeighbour(ids, spec []string, ruleID string) []string {
	at := 0
	for _, id := range spec {
		if id == ruleID {
			break
		}
		for j, current := range ids {
			if current == id && j+1 > at {
				at = j + 1
			}
		}
	}
	out := make([]string, 0, len(ids)+1)
	out = append(out, ids[:at]...)
	out = append(out, ruleID)
	return append(out, ids[at:]...)
}

// publishPushRules writes the whole rule set as the user's m.push_rules global account data.
//
// MSC4010 spells out the pairing that used to be folklore: a change made through /pushrules "is
// sent to all clients via a m.push_rules event in the account_data section in the next /sync".
// MSC4548 then asks the sliding sync extension for the same row, in as many words. Without it a
// second device never learns that the first one changed a rule, and a client that reads its
// rules out of the account data section - which is where the MSC says they are - reads nothing.
//
// The whole document every time is deliberate. Clients replace the content they hold for a type,
// the set is a few kilobytes, and a partial entry would leave every client merging rule lists.
// The rule table stays the thing that decides whether an event notifies: this is the copy clients
// read, never the other way round, which is exactly why the write endpoint refuses it.
func (s *Service) publishPushRules(ctx context.Context, userID string) error {
	rules, err := s.PushRules(ctx, userID)
	if err != nil {
		return err
	}
	content, err := json.Marshal(rules)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	return s.db.AccountDataRepo.Set(userID, "", matrix.TypePushRules, string(content), s.nextStreamPos())
}

// PushRuleWire is a rule as exposed to clients.
type PushRuleWire struct {
	RuleID     string       `json:"rule_id"`
	Default    bool         `json:"default"`
	Enabled    bool         `json:"enabled"`
	Conditions matrix.Array `json:"conditions,omitempty"`
	Actions    matrix.Array `json:"actions"`
	// Pattern is a pointer because a content rule carries the field even when there is
	// no pattern to show: the two kinds of rule are told apart by which of the two
	// fields they carry, and a content rule whose answer has no pattern at all is a rule
	// a strict reader refuses - ruma stops parsing the whole rule set on it.
	Pattern *string  `json:"pattern,omitempty"`
	Tuple   []string `json:"tuple,omitempty"`
}

// PushRules returns the whole rule set of a user in wire format.
func (s *Service) PushRules(ctx context.Context, userID string) (matrix.Object, error) {
	global := matrix.Object{}
	for _, kind := range PushRuleKinds {
		rules, err := s.db.PushRuleRepo.List(userID, kind)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		wire := make([]PushRuleWire, 0, len(rules))
		for _, rule := range rules {
			wire = append(wire, pushRuleWire(rule))
		}
		global[kind] = wire
	}
	return matrix.Object{"global": global}, nil
}

// PushRuleKind returns the rules of a single kind.
func (s *Service) PushRuleKind(ctx context.Context, userID, kind string) (matrix.Object, error) {
	if !validKind(kind) {
		return nil, unknownPushKind(kind)
	}
	rules, err := s.db.PushRuleRepo.List(userID, kind)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := make([]PushRuleWire, 0, len(rules))
	for _, rule := range rules {
		out = append(out, pushRuleWire(rule))
	}
	return matrix.Object{"global": matrix.Object{kind: out}}, nil
}

// PushRuleOne returns a single rule.
func (s *Service) PushRuleOne(ctx context.Context, userID, kind, ruleID string) (*PushRuleWire, error) {
	if !validKind(kind) {
		return nil, unknownPushKind(kind)
	}
	rule, err := s.db.PushRuleRepo.Get(userID, kind, ruleID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("no such rule %s", ruleID)
		}
		return nil, matrix.ErrInternal(err)
	}
	wire := pushRuleWire(rule)
	return &wire, nil
}

// pushRuleWire renders a stored rule for the wire.
func pushRuleWire(rule *persist.PushRule) PushRuleWire {
	out := PushRuleWire{
		RuleID:  rule.RuleID,
		Default: rule.Default,
		Enabled: rule.Enabled,
		Actions: toAnySlice(rule.Actions),
	}
	if rule.Conditions != "" {
		out.Conditions = toAnySlice(rule.Conditions)
	}
	if rule.Kind == "content" {
		// The specification stores a content rule's one condition as a field: "the spec
		// allows a shorthand rule with a single content.body pattern". Reading it back out
		// is what puts `pattern` on the answer, so a content rule is seeded with that
		// condition rather than with a pattern kept beside it. A content rule with no such
		// condition publishes an empty pattern, which is what a content rule always having
		// one comes down to.
		pattern := ""
		for _, condition := range out.Conditions {
			entry, ok := condition.(matrix.Object)
			if !ok {
				continue
			}
			if kind, _ := entry["kind"].(string); kind != "event_match" {
				continue
			}
			if key, _ := entry["key"].(string); key != "content.body" {
				continue
			}
			if value, ok := entry["pattern"].(string); ok {
				pattern = value
			}
		}
		out.Pattern = &pattern
	}
	return out
}

// toAnySlice decodes a JSON array into a slice, returning nil for invalid or
// empty input.
func toAnySlice(raw string) matrix.Array {
	if raw == "" {
		return nil
	}
	var out matrix.Array
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// PushRulePlacement says where a newly created rule has to be inserted in the
// rule order of its kind. At most one of the two rule ids may be given.
type PushRulePlacement struct {
	Before string
	After  string
}

// SetPushRule creates or updates a rule.
func (s *Service) SetPushRule(ctx context.Context, userID, kind, ruleID string, body matrix.Object, placement PushRulePlacement) error {
	if !validKind(kind) {
		return matrix.ErrInvalidParam("unknown push rule kind %q", kind)
	}
	if ruleID == "" {
		return matrix.ErrInvalidParam("a rule id is required")
	}
	if placement.Before != "" && placement.After != "" {
		return matrix.ErrInvalidParam("only one of before and after may be given")
	}
	if err := s.checkRuleAnchor(userID, kind, placement); err != nil {
		return err
	}
	actions, _ := json.Marshal(body["actions"])
	if string(actions) == "" || string(actions) == "null" {
		return matrix.ErrInvalidRequest("actions are required")
	}
	conditions := ""
	if raw, ok := body["conditions"]; ok && raw != nil {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return matrix.ErrInvalidRequest("conditions must be an array")
		}
		conditions = string(encoded)
	}
	if pattern, ok := body["pattern"].(string); ok && conditions == "" {
		conditions = string(matrix.Encode(matrix.Array{
			matrix.Object{"kind": "event_match", "key": "content.body", "pattern": pattern},
		}))
	}
	enabled := true
	if value, ok := body["enabled"].(bool); ok {
		enabled = value
	}
	// PushRuleRepo.Get hands back a zero row when the rule does not exist, so
	// only a nil error means the rule is already there.
	existing, err := s.db.PushRuleRepo.Get(userID, kind, ruleID)
	if err != nil && !persist.IsNotFound(err) {
		return matrix.ErrInternal(err)
	}
	positions := s.nextRulePosition(userID, kind)
	// A rule that is already there keeps three things a PUT body never says: its place in
	// the rule order, the flag saying the server wrote it, and, for a default rule, its
	// conditions. A settings screen changing a default rule's actions sends actions and
	// nothing else, so reading that silence for "no conditions" would turn
	// `.m.rule.suppress_notices` into a rule matching every event, and losing the flag
	// turns a rule of the server into a rule of the person in every client that asks.
	isDefault := false
	if err == nil {
		positions = existing.Positions
		isDefault = existing.Default
		if existing.Default {
			conditions = existing.Conditions
		}
	}
	rule := &persist.PushRule{
		UserID:     userID,
		Kind:       kind,
		RuleID:     ruleID,
		Enabled:    enabled,
		Default:    isDefault,
		Actions:    string(actions),
		Conditions: conditions,
		Positions:  positions,
	}
	if err := s.db.PushRuleRepo.Set(rule); err != nil {
		return matrix.ErrInternal(err)
	}
	if err := s.repositionPushRule(userID, kind, ruleID, placement); err != nil {
		return err
	}
	return s.publishPushRules(ctx, userID)
}

// checkRuleAnchor verifies that a before or after parameter names a rule that
// exists in the same kind.
func (s *Service) checkRuleAnchor(userID, kind string, placement PushRulePlacement) error {
	for _, anchor := range []string{placement.Before, placement.After} {
		if anchor == "" {
			continue
		}
		_, err := s.db.PushRuleRepo.Get(userID, kind, anchor)
		if err != nil && !persist.IsNotFound(err) {
			return matrix.ErrInternal(err)
		}
		if err != nil {
			return matrix.ErrInvalidParam("no rule %q of kind %s", anchor, kind)
		}
	}
	return nil
}

// nextRulePosition returns the position a new rule of this kind gets: one past
// the highest position in use, which puts it at the end of the kind.
func (s *Service) nextRulePosition(userID, kind string) int64 {
	rules, err := s.db.PushRuleRepo.List(userID, kind)
	if err != nil {
		return 0
	}
	highest := int64(-1)
	for _, rule := range rules {
		if rule.Positions > highest {
			highest = rule.Positions
		}
	}
	return highest + 1
}

// repositionPushRule moves a stored rule to the requested place in the rule
// order of its kind. The whole kind is renumbered from zero so that positions
// stay dense after repeated moves.
func (s *Service) repositionPushRule(userID, kind, ruleID string, placement PushRulePlacement) error {
	if placement.Before == "" && placement.After == "" {
		return nil
	}
	rules, err := s.db.PushRuleRepo.List(userID, kind)
	if err != nil {
		return matrix.ErrInternal(err)
	}
	order := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.RuleID != ruleID {
			order = append(order, rule.RuleID)
		}
	}
	insertAt := len(order)
	for i, id := range order {
		if id == placement.Before {
			insertAt = i
			break
		}
		if id == placement.After {
			insertAt = i + 1
			break
		}
	}
	order = append(order[:insertAt], append([]string{ruleID}, order[insertAt:]...)...)
	if err := s.db.PushRuleRepo.Order(userID, kind, order); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// DeletePushRule removes a rule.
func (s *Service) DeletePushRule(ctx context.Context, userID, kind, ruleID string) error {
	if err := s.pushRuleThere(userID, kind, ruleID); err != nil {
		return err
	}
	if err := s.db.PushRuleRepo.Delete(userID, kind, ruleID); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.publishPushRules(ctx, userID)
}

// SetPushRuleEnabled enables or disables a rule.
func (s *Service) SetPushRuleEnabled(ctx context.Context, userID, kind, ruleID string, enabled bool) error {
	if err := s.pushRuleThere(userID, kind, ruleID); err != nil {
		return err
	}
	if err := s.db.PushRuleRepo.SetEnabled(userID, kind, ruleID, enabled); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.publishPushRules(ctx, userID)
}

// SetPushRuleActions replaces the actions of a rule.
func (s *Service) SetPushRuleActions(ctx context.Context, userID, kind, ruleID string, actions matrix.Array) error {
	if err := s.pushRuleThere(userID, kind, ruleID); err != nil {
		return err
	}
	if err := s.db.PushRuleRepo.SetActions(userID, kind, ruleID, string(matrix.Encode(actions))); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.publishPushRules(ctx, userID)
}

// validKind reports whether kind is one of the five push rule kinds.
func validKind(kind string) bool {
	for _, candidate := range PushRuleKinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

// unknownPushKind is what a kind outside PushRuleKinds answers: 404 M_NOT_FOUND,
// because the rule named behind it cannot exist. The spec says so for every
// read and every mutation of an existing rule ("The push rule was not found."),
// and Complement depends on the difference: its MSC4306 probe reads a 404 on the
// kind `postcontent` as "no such rule to disable" and carries on, while a 400 is
// a server error to it. Only PUT — *creating* a rule — keeps 400 M_INVALID_PARAM,
// since there nothing is missing and the request itself is the problem.
func unknownPushKind(kind string) error {
	return matrix.ErrNotFound("no push rule kind %q", kind)
}

// pushRuleThere is the check the three mutation endpoints (delete, enabled,
// actions) make before writing: the repository updates by key and reports
// nothing when no row matched, so without this a rule that does not exist is
// “successfully” mutated with a 200. Default rules are rows like any other, so
// a built-in rule answers here too.
func (s *Service) pushRuleThere(userID, kind, ruleID string) error {
	if !validKind(kind) {
		return unknownPushKind(kind)
	}
	if _, err := s.db.PushRuleRepo.Get(userID, kind, ruleID); err != nil {
		if persist.IsNotFound(err) {
			return matrix.ErrNotFound("no push rule %s of kind %s", ruleID, kind)
		}
		return matrix.ErrInternal(err)
	}
	return nil
}

// SetPusher creates, updates or deletes a pusher. A nil or empty body deletes
// the pusher identified by its key and app id.
func (s *Service) SetPusher(ctx context.Context, userID, sessionDeviceID string, body matrix.Object) error {
	pushKey, _ := body["pushkey"].(string)
	appID, _ := body["app_id"].(string)
	if pushKey == "" || appID == "" {
		return matrix.ErrInvalidParam("pushkey and app_id are required")
	}
	deleting := len(body) == 2
	if value, ok := body["kind"].(string); ok && value == "" {
		deleting = true
	}
	if deleting {
		return s.db.PusherRepo.Delete(userID, pushKey, appID)
	}
	kind, _ := body["kind"].(string)
	if kind == "" {
		kind = "http"
	}
	data := matrix.Object{}
	if raw, ok := body["data"].(matrix.Object); ok {
		data = raw
	}
	active := true
	if tweak, ok := body["pusher.tweak"].(string); ok {
		active = tweak != ""
	}
	// The pusher belongs to a device: it is that device that will be notified. A
	// client may name one in the body, and when it does not the device of the access
	// token that made the request is the only honest answer. Without this the pusher
	// belongs to no device, and a password change or a sign-out of everything cannot
	// tell whose pushers to drop.
	device := stringOf(body, "device_id")
	if device == "" {
		device = sessionDeviceID
	}
	pusher := &persist.Pusher{
		PushKey:    pushKey,
		Kind:       kind,
		AppID:      appID,
		AppName:    stringOf(body, "app_display_name"),
		DeviceName: s.pusherDeviceName(ctx, userID, device, stringOf(body, "device_display_name")),
		ProfileTag: stringOf(body, "profile_tag"),
		DeviceID:   device,
		UserID:     userID,
		Lang:       stringOf(body, "lang"),
		Data:       string(matrix.Encode(data)),
		Active:     active,
	}
	if kind != "http" && kind != "email" {
		return matrix.ErrInvalidParam("pusher kind must be http or email")
	}
	return s.db.PusherRepo.Set(pusher)
}

// pusherDeviceName settles the pusher's device_display_name, which the spec marks Required on
// every Pusher object - so a client that leaves it out of the request must not be answered with a
// pusher that has no such field, because that answer is unreadable rather than merely thin: the
// Matrix Rust SDK refuses the whole list over a missing required field, and with it the client's
// push settings. A name the client did send is kept as it came. Otherwise the device says what it
// is called, and failing that its id is the name the client itself would have to fall back to.
func (s *Service) pusherDeviceName(ctx context.Context, userID, deviceID, asked string) string {
	if asked != "" {
		return asked
	}
	device, err := s.db.DeviceRepo.Get(userID, deviceID)
	if err != nil || device == nil {
		return deviceID
	}
	if device.DisplayName != "" {
		return device.DisplayName
	}
	return deviceID
}

// DeletePusher removes the pusher with this identity, which is what
// DELETE /v1/pushers/{pushkey} does. Pushers are keyed by pushkey and app id,
// so the app id has to match as well.
func (s *Service) DeletePusher(ctx context.Context, userID, pushKey, appID string) error {
	pusher, err := s.db.PusherRepo.Get(pushKey, appID)
	if err != nil && !persist.IsNotFound(err) {
		return matrix.ErrInternal(err)
	}
	// A pusher that does not exist, or belongs to another user, is not visible
	// at all.
	if err != nil || pusher.UserID != userID {
		return matrix.ErrNotFound("no pusher with pushkey %q and app id %q", pushKey, appID)
	}
	if err := s.db.PusherRepo.Delete(userID, pushKey, appID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// Pushers lists the pushers registered by a user.
func (s *Service) Pushers(ctx context.Context, userID string) ([]matrix.Object, error) {
	pushers, err := s.db.PusherRepo.List(userID)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := make([]matrix.Object, 0, len(pushers))
	for _, pusher := range pushers {
		name := pusher.DeviceName
		if name == "" {
			// A row written before this field was kept has no name at all, and a required field is
			// still required: the device it belongs to says what to answer.
			name = s.pusherDeviceName(ctx, userID, pusher.DeviceID, "")
		}
		entry := matrix.Object{
			"pushkey":             pusher.PushKey,
			"kind":                pusher.Kind,
			"app_id":              pusher.AppID,
			"app_display_name":    pusher.AppName,
			"device_display_name": name,
			"device_id":           pusher.DeviceID,
			"lang":                pusher.Lang,
			"data":                matrix.MustParseObject([]byte(pusher.Data)),
			"active":              pusher.Active,
		}
		// The two optional fields of a Pusher stay optional: the spec says so, and a client that
		// reads device_id to match a pusher to its own session should not be told an empty string
		// is a device.
		if pusher.ProfileTag != "" {
			entry["profile_tag"] = pusher.ProfileTag
		}
		if pusher.DeviceID != "" {
			entry["device_id"] = pusher.DeviceID
		}
		out = append(out, entry)
	}
	return out, nil
}

// stringOf reads a string field from an object, tolerating absence.
func stringOf(body matrix.Object, key string) string {
	value, _ := body[key].(string)
	return value
}
