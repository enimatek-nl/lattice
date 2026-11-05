package pushrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// ScopeGlobal is the only push rule scope the specification defines. A device scope is
// mentioned in a few endpoint descriptions but has no defined ruleset semantics, and a
// client that asked for one would get rules it could not predict.
const ScopeGlobal = ScopeName

// Service stores and edits a user's push rules.
type Service struct {
	db      *persist.Database
	account persist.AccountDataRepo
	users   persist.UserRepo
	log     *slog.Logger
}

// NewService wires the push rule service to the storage layer.
func NewService(db *persist.Database, log *slog.Logger) *Service {
	return &Service{db: db, account: db.AccountData, users: db.Users, log: log}
}

// SetInput is what a client submits when it writes a rule. The pointers say which fields
// the client actually sent, because "no pattern" and "an empty pattern" are different
// statements, and a rule update that quietly dropped a field the client did not mention
// would lose it.
type SetInput struct {
	Actions    []Action
	Conditions []map[string]any
	HasPattern bool
	Pattern    string
	HasCond    bool
	Enabled    *bool
	// Default is what the client asked for the `default` flag. Only false is allowed:
	// the flag records that the server provided the rule.
	Default *bool
}

// Ruleset returns the user's rules, creating the server-default set the first time they
// are needed. A user with no rules at all would otherwise see an empty list and be told
// they had suppressed everything.
func (s *Service) Ruleset(ctx context.Context, userID string) (Ruleset, error) {
	set, _, err := s.load(ctx, userID)
	return set, err
}

// EnsureDefaults gives a user their default rules if they do not have them yet.
func (s *Service) EnsureDefaults(ctx context.Context, userID string) error {
	_, err := s.Ruleset(ctx, userID)
	return err
}

// EnsureAllDefaults gives every account on this server the rules that feature assumes,
// which matters for accounts created before push rules existed here. It runs at start-up;
// a user who is somehow missed still gets them on their first read.
func (s *Service) EnsureAllDefaults(ctx context.Context) error {
	ids, err := s.users.ListUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("pushrules: cannot list the accounts: %w", err)
	}
	for _, id := range ids {
		if err := s.EnsureDefaults(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// load reads the ruleset and reports whether it had to be created.
func (s *Service) load(ctx context.Context, userID string) (Ruleset, bool, error) {
	row, err := s.account.Get(ctx, userID, AccountDataType)
	if err != nil {
		if !errors.Is(err, persist.ErrNotFound) {
			return Ruleset{}, false, fmt.Errorf("pushrules: cannot read the stored rules: %w", err)
		}
		// Materialising the defaults goes through this service's own handle, which is
		// the transaction's when the read happens inside one. Opening a fresh
		// transaction from here would wait on the one that is already open.
		set := Defaults(userID)
		if err := s.write(ctx, userID, set); err != nil {
			return Ruleset{}, false, err
		}
		return set, true, nil
	}
	set, err := RulesetFromAccountData(row.Content)
	if err != nil {
		return Ruleset{}, false, matrixerr.Newf(matrixerr.CodeUnknown,
			"This server cannot read its stored push rules for %s.", userID)
	}
	return set, false, nil
}

// write stores the ruleset as account data through whichever handle this service holds.
// The new stream position is what tells every client of this user that their rules
// changed. It deliberately does not open a transaction: doing that inside one would wait
// for a connection the outer transaction is already holding, which on a single-writer
// database is a deadlock and not a slowdown.
func (s *Service) write(ctx context.Context, userID string, set Ruleset) error {
	body, err := json.Marshal(set)
	if err != nil {
		return fmt.Errorf("pushrules: cannot encode the rules: %w", err)
	}
	content, err := json.Marshal(map[string]any{"global": json.RawMessage(body)})
	if err != nil {
		return fmt.Errorf("pushrules: cannot encode the account data: %w", err)
	}
	if _, err := s.account.Put(ctx, userID, AccountDataType, string(content)); err != nil {
		return fmt.Errorf("pushrules: cannot store the rules: %w", err)
	}
	return nil
}

// RoomUpgraded copies each local user's rule about the old room onto the room that
// replaced it.
//
// A room rule is about a conversation. A user who muted the old room said "not this one", and
// an upgrade moves the conversation to a new identifier — so a rule that stays behind leaves
// the user notified about what they asked not to be notified about, and un-muted about what
// they muted.
//
// This behaviour is not written in the specification's upgrade section. Synapse and Dendrite
// do it and Complement tests it, so it is implemented and recorded as an implementation
// convention rather than presented as a specification requirement.
//
// Every local account is read, because a rule may name a room the user has since left. That
// is a linear scan over accounts, which is acceptable for a rare operation at this scale and
// would need an index if it did not.
func (s *Service) RoomUpgraded(ctx context.Context, oldRoomID, newRoomID string) error {
	if oldRoomID == "" || newRoomID == "" || oldRoomID == newRoomID {
		return nil
	}
	ids, err := s.users.ListUserIDs(ctx)
	if err != nil {
		return fmt.Errorf("pushrules: cannot list the accounts: %w", err)
	}
	var carried []string
	for _, userID := range ids {
		set, created, err := s.load(ctx, userID)
		if err != nil {
			return err
		}
		if created {
			// This user had no rules of their own, only the defaults this server just
			// handed them. A default is never about a room, so there is nothing to carry.
			continue
		}
		old, _, err := findRule(set.Room, oldRoomID)
		if err != nil {
			if errors.Is(err, persist.ErrNotFound) {
				continue
			}
			return err
		}
		if _, _, err := findRule(set.Room, newRoomID); err == nil {
			// The user already said something about the new room. Their own words win.
			continue
		} else if !errors.Is(err, persist.ErrNotFound) {
			return err
		}
		moved := old
		moved.RuleID = newRoomID
		set.Room = append(set.Room, moved)
		if err := s.write(ctx, userID, set); err != nil {
			return err
		}
		carried = append(carried, userID)
	}
	if len(carried) > 0 {
		s.log.Info("pushrules: carried room rules over an upgrade",
			"old_room_id", oldRoomID, "new_room_id", newRoomID, "users", len(carried))
	}
	return nil
}

// checkScope rejects a scope this server has no rules for.
func checkScope(scope string) error {
	if scope != ScopeGlobal {
		return matrixerr.Newf(matrixerr.CodeNotFound,
			"This server has no push rules under the scope %q.", scope)
	}
	return nil
}

// Rule returns one rule.
func (s *Service) Rule(ctx context.Context, userID, scope string, kind Kind, ruleID string) (Rule, error) {
	if err := checkScope(scope); err != nil {
		return Rule{}, err
	}
	set, _, err := s.load(ctx, userID)
	if err != nil {
		return Rule{}, err
	}
	rule, _, err := findRule(set.KindRules(kind), ruleID)
	return rule, ruleError(err)
}

// KindRules returns the rules of one kind, most important first.
func (s *Service) KindRules(ctx context.Context, userID, scope string, kind Kind) ([]Rule, error) {
	if err := checkScope(scope); err != nil {
		return nil, err
	}
	set, _, err := s.load(ctx, userID)
	if err != nil {
		return nil, err
	}
	return set.KindRules(kind), nil
}

// Set creates or updates a rule.
//
// The order of rules within a kind is the priority, and the two sections in it are kept
// apart: the user's own rules come first, the server's defaults after. A new rule is
// therefore inserted at the front of the user's section, which is what "the most
// important user defined rule" means. `before` and `after` name a rule in that section;
// naming one of the defaults is refused, because the user cannot re-rank the defaults
// against each other.
func (s *Service) Set(ctx context.Context, userID, scope string, kind Kind, ruleID string, in SetInput, before, after string) error {
	if err := checkScope(scope); err != nil {
		return err
	}
	if err := ValidateRuleID(ruleID); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error())
	}
	if in.Default != nil && *in.Default {
		return matrixerr.Newf(matrixerr.CodeInvalidParam,
			"Only this server can say that a rule is one of its defaults.")
	}
	if err := ValidateActions(in.Actions); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error())
	}
	if err := validatePerKind(kind, in); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error())
	}
	if before != "" && after != "" {
		return matrixerr.Newf(matrixerr.CodeInvalidParam,
			"A rule cannot be placed both before and after another rule.")
	}
	return s.db.Tx(ctx, func(tx *gorm.DB) error {
		set, _, err := s.WithTx(tx).load(ctx, userID)
		if err != nil {
			return err
		}
		rules := set.KindRules(kind)
		existing, index, err := findRule(rules, ruleID)
		if err != nil && !errors.Is(err, persist.ErrNotFound) {
			return err
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		rule := Rule{
			RuleID:     ruleID,
			Default:    existing.Default,
			Enabled:    enabled,
			Actions:    in.Actions,
			Conditions: in.Conditions,
		}
		if in.HasPattern {
			rule.Pattern = &in.Pattern
		} else {
			rule.Pattern = existing.Pattern
		}
		if !in.HasCond {
			rule.Conditions = existing.Conditions
		}
		if err := validateStored(kind, rule); err != nil {
			return matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error())
		}
		if existing.RuleID != "" {
			rules[index] = rule
		} else {
			at, err := insertAt(rules, before, after)
			if err != nil {
				return err
			}
			rules = append(rules[:at], append([]Rule{rule}, rules[at:]...)...)
		}
		set.setKind(kind, rules)
		return s.WithTx(tx).write(ctx, userID, set)
	})
}

// SetEnabled turns a rule on or off. A disabled rule never matches, which is how a client
// suppresses one of the server's defaults without deleting it — the defaults cannot be
// deleted, and turning them off is the way the specification intends.
func (s *Service) SetEnabled(ctx context.Context, userID, scope string, kind Kind, ruleID string, enabled bool) error {
	if err := checkScope(scope); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx *gorm.DB) error {
		set, _, err := s.WithTx(tx).load(ctx, userID)
		if err != nil {
			return err
		}
		rules := set.KindRules(kind)
		rule, index, err := findRule(rules, ruleID)
		if err != nil {
			return ruleError(err)
		}
		rule.Enabled = enabled
		rules[index] = rule
		set.setKind(kind, rules)
		return s.WithTx(tx).write(ctx, userID, set)
	})
}

// SetActions replaces a rule's actions. This is allowed even for a default rule: the
// specification lets a client decide how a built-in rule should notify, only not remove
// it.
func (s *Service) SetActions(ctx context.Context, userID, scope string, kind Kind, ruleID string, actions []Action) error {
	if err := checkScope(scope); err != nil {
		return err
	}
	if err := ValidateActions(actions); err != nil {
		return matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error())
	}
	return s.db.Tx(ctx, func(tx *gorm.DB) error {
		set, _, err := s.WithTx(tx).load(ctx, userID)
		if err != nil {
			return err
		}
		rules := set.KindRules(kind)
		rule, index, err := findRule(rules, ruleID)
		if err != nil {
			return ruleError(err)
		}
		rule.Actions = actions
		rules[index] = rule
		set.setKind(kind, rules)
		return s.WithTx(tx).write(ctx, userID, set)
	})
}

// Delete removes a user's rule. The server's default rules are not the user's to remove;
// the way to turn one off is to disable it, so a delete request naming a default answers
// "not found" rather than pretending a default can be unmade.
func (s *Service) Delete(ctx context.Context, userID, scope string, kind Kind, ruleID string) error {
	if err := checkScope(scope); err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx *gorm.DB) error {
		set, _, err := s.WithTx(tx).load(ctx, userID)
		if err != nil {
			return err
		}
		rules := set.KindRules(kind)
		rule, index, err := findRule(rules, ruleID)
		if err != nil {
			return ruleError(err)
		}
		if rule.Default {
			return matrixerr.Newf(matrixerr.CodeNotFound,
				"%s is one of this server's own rules and cannot be deleted; disable it instead.", ruleID)
		}
		rules = append(rules[:index], rules[index+1:]...)
		set.setKind(kind, rules)
		return s.WithTx(tx).write(ctx, userID, set)
	})
}

// WithTx returns a service whose reads and writes happen inside tx, so that reading the
// ruleset, editing it and storing it again cannot interleave with another write.
func (s *Service) WithTx(tx *gorm.DB) *Service {
	return &Service{db: s.db, account: s.account.WithTx(tx), users: s.users, log: s.log}
}

// ruleError turns a storage-level "no such rule" into the protocol error the
// specification prescribes for this endpoint. Leaving it as a storage error would reach
// the client as a 500, which tells a client nothing about what it asked for.
func ruleError(err error) error {
	if errors.Is(err, persist.ErrNotFound) {
		return matrixerr.New(matrixerr.CodeNotFound, "The push rule was not found.")
	}
	return err
}

// findRule locates a rule by ID. A missing rule is ErrNotFound.
func findRule(rules []Rule, ruleID string) (Rule, int, error) {
	for i, rule := range rules {
		if rule.RuleID == ruleID {
			return rule, i, nil
		}
	}
	return Rule{}, -1, persist.ErrNotFound
}

// insertAt works out where a new rule goes relative to the rules the client named.
func insertAt(rules []Rule, before, after string) (int, error) {
	at := 0
	if before != "" || after != "" {
		anchor := before
		offset := 0
		if after != "" {
			anchor, offset = after, 1
		}
		_, index, err := findRule(rules, anchor)
		if err != nil || rules[index].Default {
			return 0, matrixerr.Newf(matrixerr.CodeUnknown,
				"before/after rule not found: %s", anchor)
		}
		at = index + offset
	}
	// Never let a user's rule land among the defaults: the sections carry the priority.
	if end := userSectionEnd(rules); at > end {
		at = end
	}
	return at, nil
}

// userSectionEnd is the index at which the server's default rules begin.
func userSectionEnd(rules []Rule) int {
	for i, rule := range rules {
		if rule.Default {
			return i
		}
	}
	return len(rules)
}

// validatePerKind checks what the client sent against what the rule kind can hold.
func validatePerKind(kind Kind, in SetInput) error {
	switch kind {
	case KindContent:
		if !in.HasPattern {
			return fmt.Errorf("a content rule needs a pattern to match")
		}
		if in.HasCond {
			return fmt.Errorf("a content rule takes its condition from its pattern, not from a condition list")
		}
	case KindRoom, KindSender:
		if in.HasPattern {
			return fmt.Errorf("the identifier of a %s rule is what it matches; a pattern would be ignored", kind)
		}
		if in.HasCond {
			return fmt.Errorf("a %s rule takes its condition from its identifier, not from a condition list", kind)
		}
	default:
		if in.HasPattern {
			return fmt.Errorf("a %s rule matches by conditions; a pattern would be ignored", kind)
		}
	}
	return nil
}

// validateStored checks the rule as it will be kept, including the fields an update
// carried over from the rule it replaced.
func validateStored(kind Kind, rule Rule) error {
	if kind == KindContent && rule.Pattern == nil {
		return fmt.Errorf("a content rule needs a pattern to match")
	}
	for _, condition := range rule.Conditions {
		if kind, ok := condition["kind"].(string); !ok || kind == "" {
			return fmt.Errorf("every condition needs a kind")
		}
	}
	return nil
}
