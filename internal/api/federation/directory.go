package federation

import (
	"context"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// AliasDirectory answers a peer's question about an alias this server owns. It is implemented by the room
// service, because the answer is this server's own alias table and its room's membership.
type AliasDirectory interface {
	AliasTarget(ctx context.Context, alias string) (roomID string, servers []string, err error)
}

// handleQueryDirectory implements GET /_matrix/federation/v1/query/directory?room_alias=...
//
// The whole of remote alias resolution starts here: a client on another server types `#room:example.org`
// into a join, that client's server asks this question of the server the alias belongs to, and whatever
// this answers decides whether the user gets into the room or is told the room does not exist. The
// specification's response is small enough to quote in a comment — `room_id`, `servers`, `origin`,
// `origin_server_ts` — and the `servers` list is the part a joining server acts on, because a room ID it
// cannot reach is a worse answer than a door it can knock on.
//
// An alias this server does not own is a 404 rather than a proxy: another server's alias is not this
// server's to answer, and forwarding the query would turn every typo'd alias on the internet into a
// request this server makes on somebody else's behalf.
func (d *Deps) handleQueryDirectory(w http.ResponseWriter, r *http.Request) {
	alias := r.URL.Query().Get("room_alias")
	if alias == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"The room_alias query parameter is missing."))
		return
	}
	if err := matrixid.RoomAliasValid(alias); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a valid room alias.", alias))
		return
	}
	if d.Aliases == nil {
		matrixerr.Write(w, http.StatusNotImplemented, matrixerr.New(matrixerr.CodeUnknown,
			"This server is not answering alias queries."))
		return
	}
	_, domain, err := matrixid.SplitID(matrixid.SigilAlias, alias)
	if err != nil || !matrixid.IsLocal(domain, d.Cfg.ServerName) {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(matrixerr.CodeNotFound,
			"This server does not own the alias %q.", alias))
		return
	}
	roomID, servers, err := d.Aliases.AliasTarget(r.Context(), alias)
	if err != nil {
		matrixerr.WriteServiceError(w, err)
		return
	}
	answer := map[string]any{
		"alias":            alias,
		"origin":           d.Cfg.ServerName,
		"origin_server_ts": time.Now().UTC().UnixMilli(),
		"servers":          servers,
	}
	// The room identifier is only present when the alias names a room. The specification allows an
	// alias that exists and resolves to nothing, and dropping the key is the difference between
	// "this alias is ours and it goes nowhere" and "this alias is not ours".
	if roomID != "" {
		answer["room_id"] = roomID
	}
	httpx.WriteJSON(w, http.StatusOK, answer)
}
