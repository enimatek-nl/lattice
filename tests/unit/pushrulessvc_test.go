package unit

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/pushrules"
)

// newPushRuleService opens a throwaway database and wires the push rule service to it.
func newPushRuleService(t *testing.T) *pushrules.Service {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "push.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening storage: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// The rules service needs an account to attach rules to, and reading a rule set for
	// an account that was never registered is a case worth knowing about: it must not
	// invent one silently.
	if err := db.Users.Create(context.Background(), &persist.User{
		UserID:    "@tester:example.org",
		Localpart: "tester",
	}); err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	return pushrules.NewService(db, slog.New(slog.DiscardHandler))
}

const tester = "@tester:example.org"

// TestPushRulesSetAndRead walks the edits a client makes: add a rule, place another one
// beside it, turn it off, change its actions, delete it.
func TestPushRulesSetAndRead(t *testing.T) {
	ctx := context.Background()
	svc := newPushRuleService(t)

	set, err := svc.Ruleset(ctx, tester)
	if err != nil {
		t.Fatalf("reading the default rules: %v", err)
	}
	if len(set.KindRules(pushrules.KindUnderride)) == 0 {
		t.Fatalf("a user with no settings of their own should still have the server's rules")
	}

	pattern := "cake"
	in := pushrules.SetInput{Actions: []pushrules.Action{{Name: "notify"}}, HasPattern: true, Pattern: pattern}
	if err := svc.Set(ctx, tester, "global", pushrules.KindContent, "cake-rule", in, "", ""); err != nil {
		t.Fatalf("creating a content rule: %v", err)
	}
	rule, err := svc.Rule(ctx, tester, "global", pushrules.KindContent, "cake-rule")
	if err != nil {
		t.Fatalf("reading the rule back: %v", err)
	}
	if rule.Pattern == nil || *rule.Pattern != "cake" {
		t.Errorf("pattern = %v, want cake", rule.Pattern)
	}
	if !rule.Enabled {
		t.Errorf("a newly created rule must be enabled")
	}
	if rule.Default {
		t.Errorf("a rule the user wrote is not one of the server's")
	}

	// A new rule goes ahead of the user's other rules: within a kind, priority is order,
	// and the specification puts new user rules at the top of their section.
	if err := svc.Set(ctx, tester, "global", pushrules.KindContent,
		"lie-rule", pushrules.SetInput{Actions: []pushrules.Action{}, HasPattern: true, Pattern: "cake*lie"}, "", ""); err != nil {
		t.Fatalf("creating a second content rule: %v", err)
	}
	rules, err := svc.KindRules(ctx, tester, "global", pushrules.KindContent)
	if err != nil {
		t.Fatalf("reading the kind: %v", err)
	}
	if len(rules) != 3 || rules[0].RuleID != "lie-rule" || rules[1].RuleID != "cake-rule" {
		t.Fatalf("the content rules are ordered %v; the user's newest rule should come first, the server's own after", ruleIDs(rules))
	}
	if !rules[2].Default {
		t.Errorf("the server's own content rule ended up among the user's")
	}

	// before and after place a rule next to one the user named.
	if err := svc.Set(ctx, tester, "global", pushrules.KindContent,
		"between-rule", pushrules.SetInput{Actions: []pushrules.Action{}, HasPattern: true, Pattern: "b"}, "", "cake-rule"); err != nil {
		t.Fatalf("creating a rule after cake-rule: %v", err)
	}
	rules, _ = svc.KindRules(ctx, tester, "global", pushrules.KindContent)
	if len(rules) != 4 || rules[2].RuleID != "between-rule" {
		t.Errorf("`after` put the rule in the wrong place: %v", ruleIDs(rules))
	}
	// Naming one of the server's own rules is refused: the user cannot re-rank the
	// defaults against each other.
	if err := svc.Set(ctx, tester, "global", pushrules.KindContent,
		"bad-anchor", pushrules.SetInput{Actions: []pushrules.Action{}, HasPattern: true, Pattern: "z"}, "", ".m.rule.contains_user_name"); err == nil {
		t.Errorf("placing a rule relative to a server default was accepted")
	}

	if err := svc.SetEnabled(ctx, tester, "global", pushrules.KindContent, "cake-rule", false); err != nil {
		t.Fatalf("disabling a rule: %v", err)
	}
	if rule, err := svc.Rule(ctx, tester, "global", pushrules.KindContent, "cake-rule"); err != nil || rule.Enabled {
		t.Errorf("the rule is still enabled after being switched off: %+v %v", rule, err)
	}
	if err := svc.SetActions(ctx, tester, "global", pushrules.KindContent, "cake-rule",
		[]pushrules.Action{{Name: "notify"}, {IsTweak: true, Tweak: "sound", Value: "default"}}); err != nil {
		t.Fatalf("setting actions: %v", err)
	}
	if rule, err := svc.Rule(ctx, tester, "global", pushrules.KindContent, "cake-rule"); err != nil || len(rule.Actions) != 2 {
		t.Errorf("the actions were not replaced: %+v %v", rule, err)
	}
	// An update that does not mention the pattern keeps it: dropping it would quietly
	// turn the rule into one that matches nothing.
	if rule, err := svc.Rule(ctx, tester, "global", pushrules.KindContent, "cake-rule"); err != nil || rule.Pattern == nil {
		t.Errorf("an action update lost the rule's pattern: %+v %v", rule, err)
	}

	if err := svc.Delete(ctx, tester, "global", pushrules.KindContent, "cake-rule"); err != nil {
		t.Fatalf("deleting the user's rule: %v", err)
	}
	if _, err := svc.Rule(ctx, tester, "global", pushrules.KindContent, "cake-rule"); !errors.Is(err, persist.ErrNotFound) {
		// The service speaks the protocol error a client can act on, not the storage
		// one: the rule is gone, and the answer must say so as M_NOT_FOUND.
		failure, ok := matrixerr.As(err)
		if !ok || failure.Code != matrixerr.CodeNotFound {
			t.Errorf("after deletion the rule reads as %v, want a not-found protocol error", err)
		}
	}
	// The server's own rules are not the user's to remove.
	if err := svc.Delete(ctx, tester, "global", pushrules.KindUnderride, ".m.rule.message"); err == nil {
		t.Errorf("deleting a server default was accepted")
	}
	// Turning one off is allowed, and is how a client is meant to silence it.
	if err := svc.SetEnabled(ctx, tester, "global", pushrules.KindUnderride, ".m.rule.message", false); err != nil {
		t.Errorf("disabling a server default: %v", err)
	}
}

// TestPushRulesRefusesBadWrites checks the requests a client should be told off for,
// including the ones that would let it forge the server's own rules.
func TestPushRulesRefusesBadWrites(t *testing.T) {
	ctx := context.Background()
	svc := newPushRuleService(t)

	for _, tc := range []struct {
		name  string
		kind  pushrules.Kind
		id    string
		in    pushrules.SetInput
		extra func() error
	}{
		{
			name: "a rule identifier the server has reserved",
			kind: pushrules.KindContent, id: ".m.rule.message",
			in: pushrules.SetInput{Actions: []pushrules.Action{{Name: "notify"}}, HasPattern: true, Pattern: "x"},
		},
		{
			name: "a content rule with no pattern",
			kind: pushrules.KindContent, id: "nopattern",
			in: pushrules.SetInput{Actions: []pushrules.Action{{Name: "notify"}}},
		},
		{
			name: "a room rule with a pattern",
			kind: pushrules.KindRoom, id: "!room:example.org",
			in: pushrules.SetInput{Actions: []pushrules.Action{{Name: "notify"}}, HasPattern: true, Pattern: "x"},
		},
		{
			name: "a sender rule with conditions",
			kind: pushrules.KindSender, id: "@spam:example.org",
			in: pushrules.SetInput{Actions: []pushrules.Action{{Name: "notify"}}, HasCond: true,
				Conditions: []map[string]any{{"kind": "event_match", "key": "type", "pattern": "m.room.message"}}},
		},
		{
			name: "an action this server does not know",
			kind: pushrules.KindRoom, id: "!room2:example.org",
			in: pushrules.SetInput{Actions: []pushrules.Action{{Name: "shout"}}},
		},
		{
			name: "claiming a rule is a server default",
			kind: pushrules.KindRoom, id: "!room3:example.org",
			in: pushrules.SetInput{Actions: []pushrules.Action{{Name: "notify"}}},
			extra: func() error {
				yes := true
				return svc.Set(ctx, tester, "global", pushrules.KindRoom, "!room4:example.org",
					pushrules.SetInput{Actions: []pushrules.Action{{Name: "notify"}}, Default: &yes}, "", "")
			},
		},
	} {
		var err error
		if tc.extra != nil {
			err = tc.extra()
		} else {
			err = svc.Set(ctx, tester, "global", tc.kind, tc.id, tc.in, "", "")
		}
		if err == nil {
			t.Errorf("%s: accepted, want it refused", tc.name)
		}
	}

	// A scope with no rules under it is not the same as a rule that does not exist.
	if _, err := svc.KindRules(ctx, tester, "device/phone", pushrules.KindContent); err == nil {
		t.Errorf("an unknown scope was accepted")
	}
}

// TestPushRulesAreStoredOnce checks that the rules are one current value per user rather
// than a per-device copy: two clients editing the same account see the same set, and the
// set does not accumulate versions.
func TestPushRulesAreStoredOnce(t *testing.T) {
	ctx := context.Background()
	svc := newPushRuleService(t)

	first, err := svc.Ruleset(ctx, tester)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := svc.Ruleset(ctx, tester)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(first.KindRules(pushrules.KindOverride)) != len(second.KindRules(pushrules.KindOverride)) {
		t.Errorf("the defaults were created twice: %d then %d override rules",
			len(first.KindRules(pushrules.KindOverride)), len(second.KindRules(pushrules.KindOverride)))
	}
}

// ruleIDs names a rule list for a failure message.
func ruleIDs(rules []pushrules.Rule) []string {
	ids := make([]string, 0, len(rules))
	for _, rule := range rules {
		ids = append(ids, rule.RuleID)
	}
	return ids
}
