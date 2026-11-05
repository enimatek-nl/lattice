// Package pushrules implements the push rules a user configures: which events are worth
// a notification, and how that notification should be presented.
//
// Two things shape the representation.
//
// The first is that a push rule is the user's data. They write it, and a client that
// reads it back must find what it put there, so rules are modelled as the JSON the
// specification describes rather than squeezed into a shape this server prefers.
//
// The second is that rules have an order and the order is the meaning. Two rules that
// both match an event do not both apply: the most important one does. Within a kind,
// user-defined rules outrank the server's defaults, and the defaults are not the user's
// to delete — which is why the stored arrays keep the two sections apart instead of
// flattening them.
package pushrules

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Kind is the class of a push rule, which also fixes its priority relative to other
// kinds: override outranks content, which outranks room, then sender, then underride.
type Kind string

// The five rule kinds the specification defines.
const (
	KindOverride  Kind = "override"
	KindContent   Kind = "content"
	KindRoom      Kind = "room"
	KindSender    Kind = "sender"
	KindUnderride Kind = "underride"
)

// Kinds lists the rule kinds in the order their names appear in a ruleset.
func Kinds() []Kind {
	return []Kind{KindOverride, KindContent, KindRoom, KindSender, KindUnderride}
}

// ParseKind reads a rule kind from a request path.
func ParseKind(raw string) (Kind, error) {
	switch Kind(raw) {
	case KindOverride, KindContent, KindRoom, KindSender, KindUnderride:
		return Kind(raw), nil
	default:
		return "", fmt.Errorf("%q is not a kind of push rule", raw)
	}
}

// Ruleset is a user's rules, scoped. The specification's ruleset always carries all five
// kinds, even the ones with no rules in them.
type Ruleset struct {
	Override  []Rule `json:"override"`
	Content   []Rule `json:"content"`
	Room      []Rule `json:"room"`
	Sender    []Rule `json:"sender"`
	Underride []Rule `json:"underride"`
}

// KindRules returns the slice that holds the rules of a kind. The returned slice is
// addressable through the returned index only by way of setKind: taking a rule out of it
// and appending to it does not change the ruleset.
func (r *Ruleset) KindRules(kind Kind) []Rule {
	switch kind {
	case KindOverride:
		return r.Override
	case KindContent:
		return r.Content
	case KindRoom:
		return r.Room
	case KindSender:
		return r.Sender
	case KindUnderride:
		return r.Underride
	default:
		return nil
	}
}

// setKind stores a ruleset back into the kind it belongs to.
func (r *Ruleset) setKind(kind Kind, rules []Rule) {
	switch kind {
	case KindOverride:
		r.Override = rules
	case KindContent:
		r.Content = rules
	case KindRoom:
		r.Room = rules
	case KindSender:
		r.Sender = rules
	case KindUnderride:
		r.Underride = rules
	}
}

// Rule is one push rule.
//
// Conditions are kept as decoded JSON objects rather than a Go struct: the
// specification's condition kinds carry different parameters, and a rule the server does
// not recognise must be stored anyway — it simply never matches — so inventing a struct
// here would either lose the user's parameters or reject data the specification allows.
type Rule struct {
	RuleID     string           `json:"rule_id"`
	Default    bool             `json:"default"`
	Enabled    bool             `json:"enabled"`
	Actions    []Action         `json:"actions"`
	Conditions []map[string]any `json:"conditions,omitempty"`
	// Pattern is only used by content rules. It is a pointer so that a rule with an
	// empty pattern is distinguishable from a rule that has no pattern at all.
	Pattern *string `json:"pattern,omitempty"`
}

// Action is one entry of a rule's action list. Actions are either a bare word
// ("notify") or a tweak object ({"set_tweak": "sound", "value": "default"}), so the
// JSON form is a union of a string and an object.
type Action struct {
	// Name is set for a plain action: notify, dont_notify or coalesce.
	Name string
	// Tweak and Value are set for a set_tweak action. Value may be absent, which the
	// specification reads as true for `highlight`.
	Tweak string
	Value any
	// IsTweak distinguishes the two forms: an action with an empty Name and a Tweak is
	// still a tweak.
	IsTweak bool
}

// MarshalJSON writes the action in the form the specification uses.
func (a Action) MarshalJSON() ([]byte, error) {
	if !a.IsTweak {
		return json.Marshal(a.Name)
	}
	body := map[string]any{"set_tweak": a.Tweak}
	if a.Value != nil {
		body["value"] = a.Value
	}
	return json.Marshal(body)
}

// UnmarshalJSON reads either form.
func (a *Action) UnmarshalJSON(data []byte) error {
	var plain string
	if err := json.Unmarshal(data, &plain); err == nil {
		a.Name, a.IsTweak = plain, false
		return nil
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return err
	}
	tweak, ok := body["set_tweak"].(string)
	if !ok {
		return fmt.Errorf("an action object needs a set_tweak name")
	}
	a.Tweak, a.Value, a.IsTweak, a.Name = tweak, body["value"], true, ""
	return nil
}

// Notify reports whether this action asks for the event to be notified at all. The
// legacy `dont_notify` and `coalesce` actions are no-ops that must still be accepted,
// and a rule whose actions contain no `notify` suppresses the notification.
func (a Action) Notify() bool { return !a.IsTweak && a.Name == actionNotify }

// The action names the specification defines.
const (
	actionNotify      = "notify"
	actionDontNotify  = "dont_notify"
	actionCoalesce    = "coalesce"
	actionSetTweakKey = "set_tweak"
)

// ValidateActions checks an action list as a client submitted it. The two legacy actions
// are accepted: the specification says they are to be ignored rather than rejected, and
// a client that still sends them is not doing anything wrong.
func ValidateActions(actions []Action) error {
	for _, action := range actions {
		if action.IsTweak {
			if strings.TrimSpace(action.Tweak) == "" {
				return fmt.Errorf("a set_tweak action needs a tweak name")
			}
			continue
		}
		switch action.Name {
		case actionNotify, actionDontNotify, actionCoalesce:
		case "":
			return fmt.Errorf("an action cannot be an empty string")
		default:
			return fmt.Errorf("%q is not an action this server understands", action.Name)
		}
	}
	return nil
}

// ValidateRuleID checks a rule identifier a client wants to write. A leading dot marks a
// server-default rule and is reserved; slashes and backslashes would move the identifier
// across path segments.
func ValidateRuleID(ruleID string) error {
	if ruleID == "" {
		return fmt.Errorf("a rule needs an identifier")
	}
	if strings.HasPrefix(ruleID, ".") {
		return fmt.Errorf("a rule identifier starting with %q is reserved for the server's own rules", ".")
	}
	if strings.ContainsAny(ruleID, `/\`) {
		return fmt.Errorf(`a rule identifier cannot contain "/" or "\"`)
	}
	return nil
}
