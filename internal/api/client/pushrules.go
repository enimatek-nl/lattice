package client

import (
	"net/http"
	"strings"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/pushrules"
)

// Push rules: GET, PUT and DELETE on /_matrix/client/v3/pushrules and everything below
// it.
//
// The specification's paths under this prefix are a small tree — the whole set, a scope,
// a kind, a rule, and a rule's `enabled` or `actions` — and a rule ID may be a room ID or
// a user ID, which is a path segment full of escapes. Go's mux cannot express a wildcard
// in the middle of a path, so one route per method catches the subtree and this handler
// walks the segments. The cost is that a rule ID containing an encoded slash would be
// read as two segments; that is recorded as a limitation rather than hidden.

// handlePushRulesGet serves the read side of the push rule tree.
func (d *Deps) handlePushRulesGet(w http.ResponseWriter, r *http.Request) {
	path := parsePushRulePath(r)
	userID := auth.UserID(r)
	// The scope comes first: a path under a scope this server has no rules under is
	// not a bad kind or a missing rule, and answering those instead would send a client
	// looking in the wrong place.
	if err := checkPushScope(path.scope()); err != nil {
		d.serviceError(w, err)
		return
	}

	switch len(path.segments) {
	case 0, 1:
		// The whole set, or one scope of it. Both answer with the ruleset under the
		// scope name, which is what the drill-down in the specification returns.
		set, err := d.PushRules.Ruleset(r.Context(), userID)
		if err != nil {
			d.serviceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{pushrules.ScopeName: set})
	case 2:
		kind, err := pushrules.ParseKind(path.kind())
		if err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error()))
			return
		}
		rules, err := d.PushRules.KindRules(r.Context(), userID, path.scope(), kind)
		if err != nil {
			d.serviceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{string(kind): rules})
	case 3:
		rule, err := d.PushRules.Rule(r.Context(), userID, path.scope(), path.mustKind(), path.ruleID())
		if err != nil {
			d.serviceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, rule)
	case 4:
		switch path.leaf() {
		case "enabled":
			rule, err := d.PushRules.Rule(r.Context(), userID, path.scope(), path.mustKind(), path.ruleID())
			if err != nil {
				d.serviceError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"enabled": rule.Enabled})
		case "actions":
			rule, err := d.PushRules.Rule(r.Context(), userID, path.scope(), path.mustKind(), path.ruleID())
			if err != nil {
				d.serviceError(w, err)
				return
			}
			httpx.WriteJSON(w, http.StatusOK, map[string]any{"actions": rule.Actions})
		default:
			d.unknownPath(w, r)
		}
	default:
		d.unknownPath(w, r)
	}
}

// handlePushRulesPut serves the write side: a whole rule, a rule's enabled flag, or a
// rule's actions.
func (d *Deps) handlePushRulesPut(w http.ResponseWriter, r *http.Request) {
	path := parsePushRulePath(r)
	if err := checkPushScope(path.scope()); err != nil {
		d.serviceError(w, err)
		return
	}
	kind, err := pushrules.ParseKind(path.kind())
	if err != nil && len(path.segments) >= 3 {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error()))
		return
	}
	switch {
	case len(path.segments) == 3:
		var body struct {
			Actions    *[]pushrules.Action `json:"actions"`
			Conditions *[]map[string]any   `json:"conditions"`
			Pattern    *string             `json:"pattern"`
			Enabled    *bool               `json:"enabled"`
			Default    *bool               `json:"default"`
		}
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
		if body.Actions == nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"A push rule needs an action list."))
			return
		}
		in := pushrules.SetInput{
			Actions: *body.Actions,
			Enabled: body.Enabled,
			Default: body.Default,
		}
		if body.Pattern != nil {
			in.HasPattern, in.Pattern = true, *body.Pattern
		}
		if body.Conditions != nil {
			in.HasCond, in.Conditions = true, *body.Conditions
		}
		err = d.PushRules.Set(r.Context(), auth.UserID(r), path.scope(), kind, path.ruleID(), in,
			r.URL.Query().Get("before"), r.URL.Query().Get("after"))
	case len(path.segments) == 4 && path.leaf() == "enabled":
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
		if body.Enabled == nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"enabled must be a boolean."))
			return
		}
		err = d.PushRules.SetEnabled(r.Context(), auth.UserID(r), path.scope(), kind, path.ruleID(), *body.Enabled)
	case len(path.segments) == 4 && path.leaf() == "actions":
		var body struct {
			Actions *[]pushrules.Action `json:"actions"`
		}
		if !httpx.DecodeJSONOrError(w, r, d.maxBody(), &body) {
			return
		}
		if body.Actions == nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"A push rule needs an action list."))
			return
		}
		err = d.PushRules.SetActions(r.Context(), auth.UserID(r), path.scope(), kind, path.ruleID(), *body.Actions)
	default:
		d.unknownPath(w, r)
		return
	}
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handlePushRulesDelete removes one rule.
func (d *Deps) handlePushRulesDelete(w http.ResponseWriter, r *http.Request) {
	path := parsePushRulePath(r)
	if len(path.segments) != 3 {
		d.unknownPath(w, r)
		return
	}
	if err := checkPushScope(path.scope()); err != nil {
		d.serviceError(w, err)
		return
	}
	kind, err := pushrules.ParseKind(path.kind())
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam, "%s", err.Error()))
		return
	}
	if err := d.PushRules.Delete(r.Context(), auth.UserID(r), path.scope(), kind, path.ruleID()); err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{})
}

// pushRulePath is the decoded tail of a push rule path.
type pushRulePath struct {
	segments []string
}

// parsePushRulePath splits the part of the path below `/pushrules`. The value the mux
// gives back is already unescaped, which is what a rule ID such as a room ID (%21…%3A…)
// or a user ID (%40…%3A…) needs to be useful; the cost is that a rule ID with an encoded
// slash in it would be read as two segments. No rule the specification describes does
// that — identifiers may not contain slashes at all — so the trade is stated rather than
// worked around.
func parsePushRulePath(r *http.Request) *pushRulePath {
	raw := strings.Trim(pathValue(r, "rest"), "/")
	path := &pushRulePath{}
	if raw == "" {
		return path
	}
	path.segments = strings.Split(raw, "/")
	return path
}

// scope reads the scope segment. The path below /pushrules may be empty — that is the
// whole set — and the whole set is the global scope, because it is the only one.
func (p *pushRulePath) scope() string {
	if len(p.segments) == 0 {
		return pushrules.ScopeGlobal
	}
	return p.segments[0]
}

func (p *pushRulePath) kind() string {
	if len(p.segments) < 3 {
		return p.segments[len(p.segments)-1]
	}
	return p.segments[1]
}

// mustKind reads the kind of a rule path already known to be deep enough.
func (p *pushRulePath) mustKind() pushrules.Kind {
	return pushrules.Kind(p.segments[1])
}

func (p *pushRulePath) ruleID() string { return p.segments[2] }

func (p *pushRulePath) leaf() string { return p.segments[3] }

// checkPushScope rejects a scope this server has no rules under.
func checkPushScope(scope string) error {
	if scope != pushrules.ScopeGlobal {
		return matrixerr.Newf(matrixerr.CodeNotFound, "This server has no push rules under the scope %q.", scope)
	}
	return nil
}
