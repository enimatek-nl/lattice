package api

import (
	"net/http"
	"strings"

	"github.com/enimatek-nl/lattice/internal/auth"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
)

// pushRulesPrefix is the path prefix of the push rule endpoints. The remaining
// path segments encode the (deprecated) scope, the rule kind and the rule ID,
// so a single subtree handler parses them.
const pushRulesPrefix = "/_matrix/client/v3/pushrules/"

// handlePushRules serves GET, PUT and DELETE under /_matrix/client/v3/pushrules/.
//
//	@Summary	Read and modify push rules
//	@Tags		push
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	matrix.Object
//	@Router		/_matrix/client/v3/pushrules/ [get]
func (a *App) handlePushRules(w http.ResponseWriter, r *http.Request) {
	userID := auth.GetUserID(r)
	segments := pushRuleSegments(r.URL.Path)

	switch r.Method {
	case http.MethodGet:
		a.getPushRules(w, r, userID, segments)
	case http.MethodPut:
		a.putPushRules(w, r, userID, segments)
	case http.MethodDelete:
		if len(segments) != 2 {
			a.writeError(w, r, matrix.ErrNotFound("unknown push rule endpoint"))
			return
		}
		if err := a.svc.DeletePushRule(r.Context(), userID, segments[0], segments[1]); err != nil {
			a.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	default:
		a.writeError(w, r, matrix.ErrUnrecognized("unknown method for the push rules endpoint"))
	}
}

// getPushRules serves the rule set, one kind or one rule.
func (a *App) getPushRules(w http.ResponseWriter, r *http.Request, userID string, segments []string) {
	switch len(segments) {
	case 0:
		rules, err := a.svc.PushRules(r.Context(), userID)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, rules)
	case 1:
		rules, err := a.svc.PushRuleKind(r.Context(), userID, segments[0])
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, rules)
	case 2:
		rule, err := a.svc.PushRuleOne(r.Context(), userID, segments[0], segments[1])
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, rule)
	default:
		a.writeError(w, r, matrix.ErrNotFound("unknown push rule endpoint"))
	}
}

// putPushRules creates or updates a rule, its enabled flag or its actions.
func (a *App) putPushRules(w http.ResponseWriter, r *http.Request, userID string, segments []string) {
	switch len(segments) {
	case 2:
		var body matrix.Object
		if err := decodeBody(r, &body); err != nil {
			a.writeError(w, r, err)
			return
		}
		// before and after say where a new rule has to be inserted.
		placement := service.PushRulePlacement{
			Before: queryValue(r, "before", ""),
			After:  queryValue(r, "after", ""),
		}
		if err := a.svc.SetPushRule(r.Context(), userID, segments[0], segments[1], body, placement); err != nil {
			a.writeError(w, r, err)
			return
		}
	case 3:
		switch segments[2] {
		case "enabled":
			var body struct {
				Enabled bool `json:"enabled"`
			}
			if err := decodeBody(r, &body); err != nil {
				a.writeError(w, r, err)
				return
			}
			if err := a.svc.SetPushRuleEnabled(r.Context(), userID, segments[0], segments[1], body.Enabled); err != nil {
				a.writeError(w, r, err)
				return
			}
		case "actions":
			var body struct {
				Actions matrix.Array `json:"actions"`
			}
			if err := decodeBody(r, &body); err != nil {
				a.writeError(w, r, err)
				return
			}
			if err := a.svc.SetPushRuleActions(r.Context(), userID, segments[0], segments[1], body.Actions); err != nil {
				a.writeError(w, r, err)
				return
			}
		default:
			a.writeError(w, r, matrix.ErrNotFound("unknown push rule endpoint"))
			return
		}
	default:
		a.writeError(w, r, matrix.ErrNotFound("unknown push rule endpoint"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// pushRuleSegments strips the prefix and the deprecated "global" scope from a
// push rule path.
func pushRuleSegments(path string) []string {
	rest := strings.TrimPrefix(path, pushRulesPrefix)
	segments := strings.Split(rest, "/")
	out := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == "" || segment == "global" {
			continue
		}
		out = append(out, segment)
	}
	return out
}

// handleSetPusher implements POST /_matrix/client/v3/pushers/set.
//
//	@Summary	Register a pusher
//	@Tags		push
//	@Accept		json
//	@Produce	json
//	@Success	200	{object}	map[string]any
//	@Router		/_matrix/client/v3/pushers/set [post]
func (a *App) handleSetPusher(w http.ResponseWriter, r *http.Request) {
	var body matrix.Object
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := a.svc.SetPusher(r.Context(), auth.GetUserID(r), auth.GetDeviceID(r), body); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handlePushers implements GET /_matrix/client/v3/pushers.
//
//	@Summary	List pushers
//	@Tags		push
//	@Produce	json
//	@Success	200	{object}	map[string][]matrix.Object
//	@Router		/_matrix/client/v3/pushers [get]
func (a *App) handlePushers(w http.ResponseWriter, r *http.Request) {
	pushers, err := a.svc.Pushers(r.Context(), auth.GetUserID(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]matrix.Object{"pushers": pushers})
}

// handleDeletePusher implements
// DELETE /_matrix/client/v3/pushers/{pushkey}/{appEncodedPushkey}. Pushers are
// identified by the pair of pushkey and app id, so the second path segment is
// the app id; the shorter form works with an ?app_id= query parameter.
//
//	@Summary	Remove a pusher
//	@Tags		push
//	@Produce	json
//	@Param		pushkey			path	string	true	"Push key"
//	@Param		appEncodedPushkey	path	string	true	"URL encoded app id"
//	@Success	200			{object}	map[string]any
//	@Router		/_matrix/client/v3/pushers/{pushkey}/{appEncodedPushkey} [delete]
func (a *App) handleDeletePusher(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("appEncodedPushkey")
	if appID == "" {
		appID = queryValue(r, "app_id", "")
	}
	if appID == "" {
		a.writeError(w, r, matrix.ErrInvalidParam("the app id of the pusher is required"))
		return
	}
	if err := a.svc.DeletePusher(r.Context(), auth.GetUserID(r), r.PathValue("pushkey"), appID); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
