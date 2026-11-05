// Package federation implements the Matrix Server-Server API endpoints that
// exist in this milestone: the version endpoint and the server key endpoints.
// Federation traffic (PDUs, EDUs, joins, backfill) arrives in its own milestone.
package federation

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/config"
	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/httpserver"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/keys"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/persist"
)

// Deps are the services the Server-Server API needs.
type Deps struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Keys    *keys.Service
	Version string
	// Keyring resolves the signing keys of other homeservers, and is used to
	// authenticate every incoming request as well as the events inside them.
	Keyring *federation.Keyring
	// Federation stores what has been received and what is still owed.
	Federation persist.FederationRepo
	// Rooms accepts events that arrived over federation.
	Rooms PDUAcceptor
	// DB reads events for the federation read endpoints.
	DB *persist.Database
}

// Register installs the Server-Server and key API routes.
func Register(r *httpserver.Router, d *Deps) {
	// A transaction is authenticated before a single byte of it is processed.
	r.Handle("PUT", "/_matrix/federation/v1/send/{txnId}", d.requireCredential(d.handleSendTransaction))
	r.Handle("GET", "/_matrix/federation/v1/event/{eventId}", d.requireCredential(d.handleGetEvent))
	r.Handle("GET", "/_matrix/federation/v1/version", d.handleVersion)
	r.Handle("GET", "/_matrix/key/v2/server", d.handleServerKeys)
	r.Handle("POST", "/_matrix/key/v2/query", d.handleKeyQuery)
}

// serverResponse is the shape of a Server-Server API response body.
type serverResponse struct {
	Server struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"server"`
}

// handleVersion implements GET /_matrix/federation/v1/version.
func (d *Deps) handleVersion(w http.ResponseWriter, r *http.Request) {
	var res serverResponse
	res.Server.Name = "lattice"
	res.Server.Version = d.Version
	httpx.WriteJSON(w, http.StatusOK, res)
}

// handleServerKeys implements GET /_matrix/key/v2/server: the signed verification
// key document for this server.
func (d *Deps) handleServerKeys(w http.ResponseWriter, r *http.Request) {
	doc, err := d.Keys.ServerKeyDocument(time.Now())
	if err != nil {
		d.Log.Error("server key document failed", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	// The body has to be the canonical form the signature was made over; see
	// encodeCanonical. Complement verifies this document over the bytes it received,
	// which is what any receiver that does not re-serialise effectively does.
	if err := encodeCanonical(w, http.StatusOK, doc); err != nil {
		d.Log.Error("server key document could not be encoded", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
}

// keyQueryRequest is the POST /_matrix/key/v2/query body.
type keyQueryRequest struct {
	ServerKeys map[string]map[string]struct {
		MinimumValidUntilTS int64 `json:"minimum_valid_until_ts"`
	} `json:"server_keys"`
}

// handleKeyQuery implements POST /_matrix/key/v2/query.
//
// This server answers queries about itself only. Notarising and caching remote
// keys is part of the federation milestone; unanswered servers are simply omitted
// from the response, which the specification permits.
func (d *Deps) handleKeyQuery(w http.ResponseWriter, r *http.Request) {
	var req keyQueryRequest
	if err := httpx.DecodeJSON(r, 1<<20, &req); err != nil {
		matrixerr.WriteError(w, http.StatusBadRequest, err)
		return
	}
	var docs []map[string]any
	var earliest time.Time
	askedForSelf := false
	for server, keyMap := range req.ServerKeys {
		if server != d.Cfg.ServerName {
			continue
		}
		askedForSelf = true
		for _, query := range keyMap {
			if query.MinimumValidUntilTS <= 0 {
				continue
			}
			until := time.UnixMilli(query.MinimumValidUntilTS)
			if earliest.IsZero() || until.After(earliest) {
				earliest = until
			}
		}
	}
	if askedForSelf {
		doc, err := d.Keys.ServerKeyDocument(earliest)
		if err != nil {
			d.Log.Error("key query failed", "error", err)
			matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
				"The server encountered an internal error."))
			return
		}
		docs = append(docs, doc)
	}
	// Same reason as the key endpoint: the documents inside this response are signed, so
	// the response is encoded in the form their signatures were made over.
	if err := encodeCanonical(w, http.StatusOK, map[string]any{"server_keys": docs}); err != nil {
		d.Log.Error("key query could not be encoded", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
}
