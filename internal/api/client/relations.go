package client

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/enimatek/lattice/internal/auth"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/rooms"
)

// handleRelations implements GET /rooms/{roomID}/relations/{eventID}[/{relType}[/{eventType}]]:
// what this server holds that says it relates to one event.
//
// The two optional path segments are filters, not separate endpoints, and they are named in the
// path rather than the query because a client asking for a thread's replies and a client asking
// for its reactions want different pages under the same URL shape. An empty segment — which a
// client cannot produce on this route — would mean "any".
//
// Direction and paging work exactly as they do on `/messages`, and that is deliberate: a client
// that knows how to walk a timeline already knows how to walk a thread.
func (d *Deps) handleRelations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	req := rooms.RelationsRequest{
		RoomID:        pathValue(r, "roomID"),
		TargetEventID: pathValue(r, "eventID"),
		RelType:       pathValue(r, "relType"),
		EventType:     pathValue(r, "eventType"),
		From:          query.Get("from"),
		Dir:           query.Get("dir"),
		Requester:     auth.UserID(r),
	}
	if req.Dir != "" && req.Dir != "f" && req.Dir != "b" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a direction; use \"f\" or \"b\".", req.Dir))
		return
	}
	// Both filters are event-type shaped: a relation type is a name in the same grammar, and
	// an event type has to be one. Anything else is a client's mistake, said plainly, rather
	// than a page that happens to be empty for a reason the client cannot see.
	for _, name := range []string{req.RelType, req.EventType} {
		if name == "" {
			continue
		}
		if err := matrixid.EventTypeValid(name); err != nil {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a valid event or relation type: %s", name, err))
			return
		}
	}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a limit; it has to be a non-negative integer.", raw))
			return
		}
		req.Limit = limit
	}
	page, err := d.Rooms.Relations(r.Context(), req)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

// handleThreads implements GET /rooms/{roomID}/threads: the room's conversations, newest first.
//
// A thread is not stored anywhere; it is the shape that events pointing at one event make, which
// is why this handler sits next to the relations one and reads the same index. MSC3440's
// `include` and `exclude` are comma-separated event-type lists, and they are validated rather
// than ignored: a request that asks for "m.room.messege" should be told, not answered with an
// empty room.
func (d *Deps) handleThreads(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	req := rooms.ThreadsRequest{
		RoomID:    pathValue(r, "roomID"),
		Include:   splitEventTypes(query.Get("include")),
		Exclude:   splitEventTypes(query.Get("exclude")),
		From:      query.Get("from"),
		Dir:       query.Get("dir"),
		Requester: auth.UserID(r),
	}
	if req.Dir != "" && req.Dir != "f" && req.Dir != "b" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a direction; use \"f\" or \"b\".", req.Dir))
		return
	}
	for _, names := range [][]string{req.Include, req.Exclude} {
		for _, name := range names {
			if isMSC3440Wildcard(name) {
				continue
			}
			if err := matrixid.EventTypeValid(name); err != nil {
				matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
					"%q is not a valid event type: %s", name, err))
				return
			}
		}
	}
	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 0 {
			matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
				"%q is not a limit; it has to be a non-negative integer.", raw))
			return
		}
		req.Limit = limit
	}
	// MSC3440 spells two names that are not event types. ".*" means every event in the thread,
	// which is what no include list at all already means; ".*non-timeline" means the events that
	// never appear in a room's timeline, and this server's thread index only ever holds timeline
	// events, so there is nothing for it to take away. Both are accepted rather than refused, and
	// neither is treated as a type name.
	req.Include, req.Exclude = expandMSC3440Wildcards(req.Include, req.Exclude)

	page, err := d.Rooms.Threads(r.Context(), req)
	if err != nil {
		d.serviceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, page)
}

// isMSC3440Wildcard names the two MSC3440 switches that are not event types.
func isMSC3440Wildcard(name string) bool {
	return name == ".*" || name == ".*non-timeline"
}

// expandMSC3440Wildcards turns those two names into what they ask for: ".*" asks for every type,
// which the service already does with no include list, and ".*non-timeline" asks about events
// that are not in a room's timeline at all, so it constrains nothing here.
func expandMSC3440Wildcards(include, exclude []string) ([]string, []string) {
	for _, name := range include {
		if name == ".*" {
			include = nil
			break
		}
	}
	include, exclude = withoutWildcard(include), withoutWildcard(exclude)
	return include, exclude
}

// withoutWildcard drops the MSC3440 switches from a type list.
func withoutWildcard(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if isMSC3440Wildcard(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// splitEventTypes reads MSC3440's comma-separated type list. An empty parameter is no list at
// all, which is a different thing from a list of one empty name.
func splitEventTypes(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
