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

// defaultPushRules are the server default rules seeded for every new account.
// They follow the "Default Ruleset" of the specification.
func defaultPushRules() []persist.PushRule {
	dontNotify := matrix.Array{"dont_notify"}
	highlight := matrix.Array{"notify", matrix.Object{"set_tweak": "highlight", "value": true}}
	count := matrix.Array{"notify", matrix.Object{"set_tweak": "count"}}
	sound := func(name string) matrix.Array {
		return matrix.Array{"notify", matrix.Object{"set_tweak": "sound", "value": name}}
	}
	rules := []persist.PushRule{
		{Kind: "override", RuleID: ".m.rule.master", Enabled: false, Actions: string(matrix.Encode(dontNotify))},
		{Kind: "override", RuleID: ".m.rule.suppress_notices", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: `[{"kind":"event_match","key":"content.msgtype","pattern":"m.notice"}]`},
		{Kind: "override", RuleID: ".m.rule.invite_for_me", Enabled: true, Actions: string(matrix.Encode(count)),
			Conditions: `[{"kind":"member_count"}]`},
		{Kind: "override", RuleID: ".m.rule.member_event", Enabled: true, Actions: string(matrix.Encode(dontNotify))},
		{Kind: "override", RuleID: ".m.rule.contains_display_name", Enabled: true, Actions: string(matrix.Encode(highlight))},
		{Kind: "override", RuleID: ".m.rule.roomnotif", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: `[{"kind":"event_match","key":"content.body","pattern":"@room"}]`},
		{Kind: "override", RuleID: ".m.rule.tombstone", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: `[{"kind":"event_match","key":"type","pattern":"m.room.tombstone"},{"kind":"event_match","key":"state_key","pattern":""}]`},
		{Kind: "override", RuleID: ".m.rule.reaction", Enabled: true, Actions: string(matrix.Encode(dontNotify))},
		{Kind: "content", RuleID: ".m.rule.contains_user_name", Enabled: true, Actions: string(matrix.Encode(highlight))},
		{Kind: "underride", RuleID: ".m.rule.call", Enabled: true, Actions: string(matrix.Encode(sound("ring"))),
			Conditions: `[{"kind":"event_match","key":"type","pattern":"m.call.invite"}]`},
		{Kind: "underride", RuleID: ".m.rule.room_one_to_one", Enabled: true, Actions: string(matrix.Encode(sound("default"))),
			Conditions: `[{"kind":"room_member_count","is":"2"}]`},
		{Kind: "underride", RuleID: ".m.rule.encrypted_room_one_to_one", Enabled: true,
			Actions:    string(matrix.Encode(sound("default"))),
			Conditions: `[{"kind":"room_member_count","is":"2"},{"kind":"event_match","key":"type","pattern":"m.room.encrypted"}]`},
		{Kind: "underride", RuleID: ".m.rule.message", Enabled: true, Actions: string(matrix.Encode(sound("default"))),
			Conditions: `[{"kind":"event_match","key":"content.msgtype","pattern":"m.*"}]`},
		{Kind: "underride", RuleID: ".m.rule.encrypted", Enabled: true, Actions: string(matrix.Encode(count)),
			Conditions: `[{"kind":"event_match","key":"type","pattern":"m.room.encrypted"}]`},
		{Kind: "underride", RuleID: ".m.rule.default", Enabled: true, Actions: string(matrix.Encode(count)),
			Conditions: `[{"kind":"event_match","key":"content.msgtype","pattern":"m.notice"}]`},
		{Kind: "underride", RuleID: ".m.rule.notice", Enabled: true, Actions: string(matrix.Encode(dontNotify)),
			Conditions: `[{"kind":"sender_notification_permission","key":"m.notice"}]`},
	}
	for i := range rules {
		rules[i].Default = true
		rules[i].Positions = int64(i)
	}
	return rules
}

// SeedPushRules installs the default rule set for a user.
func (s *Service) SeedPushRules(ctx context.Context, userID string) error {
	for _, rule := range defaultPushRules() {
		rule.UserID = userID
		if err := s.db.PushRuleRepo.Set(&rule); err != nil {
			return matrix.ErrInternal(err)
		}
	}
	return nil
}

// PushRuleWire is a rule as exposed to clients.
type PushRuleWire struct {
	RuleID     string       `json:"rule_id"`
	Default    bool         `json:"default"`
	Enabled    bool         `json:"enabled"`
	Conditions matrix.Array `json:"conditions,omitempty"`
	Actions    matrix.Array `json:"actions"`
	Pattern    string       `json:"pattern,omitempty"`
	Tuple      []string     `json:"tuple,omitempty"`
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
		return nil, matrix.ErrInvalidParam("unknown push rule kind %q", kind)
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
		return nil, matrix.ErrInvalidParam("unknown push rule kind %q", kind)
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
		// The spec allows a shorthand rule with a single content.body pattern.
		for _, condition := range out.Conditions {
			entry, ok := condition.(matrix.Object)
			if !ok {
				continue
			}
			if key, _ := entry["key"].(string); key == "content.body" {
				if pattern, ok := entry["pattern"].(string); ok {
					out.Pattern = pattern
				}
			}
		}
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
	if err == nil {
		// Replacing a rule keeps its place in the rule order.
		positions = existing.Positions
	}
	rule := &persist.PushRule{
		UserID:     userID,
		Kind:       kind,
		RuleID:     ruleID,
		Enabled:    enabled,
		Actions:    string(actions),
		Conditions: conditions,
		Positions:  positions,
	}
	if err := s.db.PushRuleRepo.Set(rule); err != nil {
		return matrix.ErrInternal(err)
	}
	return s.repositionPushRule(userID, kind, ruleID, placement)
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
	if !validKind(kind) {
		return matrix.ErrInvalidParam("unknown push rule kind %q", kind)
	}
	if err := s.db.PushRuleRepo.Delete(userID, kind, ruleID); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// SetPushRuleEnabled enables or disables a rule.
func (s *Service) SetPushRuleEnabled(ctx context.Context, userID, kind, ruleID string, enabled bool) error {
	if !validKind(kind) {
		return matrix.ErrInvalidParam("unknown push rule kind %q", kind)
	}
	if err := s.db.PushRuleRepo.SetEnabled(userID, kind, ruleID, enabled); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
}

// SetPushRuleActions replaces the actions of a rule.
func (s *Service) SetPushRuleActions(ctx context.Context, userID, kind, ruleID string, actions matrix.Array) error {
	if !validKind(kind) {
		return matrix.ErrInvalidParam("unknown push rule kind %q", kind)
	}
	if err := s.db.PushRuleRepo.SetActions(userID, kind, ruleID, string(matrix.Encode(actions))); err != nil {
		return matrix.ErrInternal(err)
	}
	return nil
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

// SetPusher creates, updates or deletes a pusher. A nil or empty body deletes
// the pusher identified by its key and app id.
func (s *Service) SetPusher(ctx context.Context, userID string, body matrix.Object) error {
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
	pusher := &persist.Pusher{
		PushKey:  pushKey,
		Kind:     kind,
		AppID:    appID,
		AppName:  stringOf(body, "app_display_name"),
		DeviceID: stringOf(body, "device_id"),
		UserID:   userID,
		Lang:     stringOf(body, "lang"),
		Data:     string(matrix.Encode(data)),
		Active:   active,
	}
	if kind != "http" && kind != "email" {
		return matrix.ErrInvalidParam("pusher kind must be http or email")
	}
	return s.db.PusherRepo.Set(pusher)
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
		out = append(out, matrix.Object{
			"pushkey":          pusher.PushKey,
			"kind":             pusher.Kind,
			"app_id":           pusher.AppID,
			"app_display_name": pusher.AppName,
			"device_id":        pusher.DeviceID,
			"lang":             pusher.Lang,
			"data":             matrix.MustParseObject([]byte(pusher.Data)),
			"active":           pusher.Active,
		})
	}
	return out, nil
}

// stringOf reads a string field from an object, tolerating absence.
func stringOf(body matrix.Object, key string) string {
	value, _ := body[key].(string)
	return value
}
