// Package federation implements the Matrix Server-Server API endpoints that
// exist in this milestone: the version endpoint and the server key endpoints.
// Federation traffic (PDUs, EDUs, joins, backfill) arrives in its own milestone.
package federation

import (
	"context"
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
	// Joins answers the join handshake: the template a joining server signs, the state it
	// needs to catch up, and what authorises both.
	Joins JoinAcceptor
	// DB reads events for the federation read endpoints.
	DB *persist.Database
	// ACLs answers whether a room has excluded a homeserver. It is the room service: a room's
	// exclusion is room state, and the endpoints and the event-accepting path have to give the
	// same answer to it.
	ACLs RoomACLs
	// Gaps answers what a peer asks for when an event it was handed cites history it was never
	// given. It is the room service: the answer is a walk over the room's event graph.
	Gaps GapReader
	// Backfill asks another server for the events this room's events cite and this server does
	// not hold. It is what makes a refusal for missing predecessors a step rather than an ending.
	Backfill GapFiller
	// E2EKeys answers another homeserver's question about the device keys of this server's
	// own users. It is the same store the Client-Server API reads, so the two listeners cannot
	// give different answers about who holds which keys.
	E2EKeys DeviceKeys
	// DeviceLists records, for the local users who may care, that a peer says one of its own
	// users has devices worth re-reading.
	DeviceLists DeviceListChanges
	// Invites stores an invite a peer made for one of this server's users, after signing it
	// here as the peer asked.
	Invites InviteAcceptor
}

// DeviceListChanges is the receiving end of a peer's device list announcement.
type DeviceListChanges interface {
	NoteRemoteDeviceListChange(ctx context.Context, userID string)
}

// Register installs the Server-Server and key API routes.
func Register(r *httpserver.Router, d *Deps) {
	// A transaction is authenticated before a single byte of it is processed.
	r.Handle("PUT", "/_matrix/federation/v1/send/{txnId}", d.requireCredential(d.handleSendTransaction))
	r.Handle("GET", "/_matrix/federation/v1/event/{eventId}", d.requireCredential(d.handleGetEvent))

	// The join handshake. A remote user joins by asking a server that is already in the room
	// for the shape a join by them would take, signing the answer, and handing it back; the
	// server that answers is the one that decides whether its copy of the room lets them in.
	// Version 3 of `/make_join` differs from version 1 only by a query parameter this server
	// does not need, so both are answered by the same handler rather than by a stub that
	// claims the newer version is unavailable.
	r.Handle("GET", "/_matrix/federation/v1/make_join/{roomId}/{userId}", d.requireCredential(d.handleMakeJoin))
	r.Handle("GET", "/_matrix/federation/v3/make_join/{roomId}/{userId}", d.requireCredential(d.handleMakeJoin))
	r.Handle("PUT", "/_matrix/federation/v2/send_join/{roomId}/{eventId}", d.requireCredential(d.handleSendJoin))

	// What a room's state is, and what authorises it, in a form a server that has never seen
	// the room can check for itself.
	r.Handle("GET", "/_matrix/federation/v1/state_ids/{roomId}", d.requireCredential(d.handleStateIDs))
	r.Handle("GET", "/_matrix/federation/v1/event_auth/{roomId}/{eventId}", d.requireCredential(d.handleEventAuth))
	r.Handle("POST", "/_matrix/federation/v1/get_missing_events/{roomId}", d.requireCredential(d.handleGetMissingEvents))
	r.Handle("GET", "/_matrix/federation/v1/version", d.handleVersion)
	r.Handle("GET", "/_matrix/key/v2/server", d.handleServerKeys)
	r.Handle("POST", "/_matrix/key/v2/query", d.handleKeyQuery)
	// The device keys of this server's users, for the other homeservers that ask. There is no
	// point answering that question without the store behind it.
	// An invite for one of this server's users, which this server signs as well as the
	// inviting server did. Without the room service behind it there is nothing to store.
	if d.Invites != nil {
		r.Handle("PUT", "/_matrix/federation/v2/invite/{roomId}/{eventId}", d.requireCredential(d.handleInvite))
		r.Handle("PUT", "/_matrix/federation/v1/invite/{roomId}/{eventId}", d.requireCredential(d.handleInvite))
	}
	if d.E2EKeys != nil {
		r.Handle("POST", "/_matrix/federation/v1/user/keys/query", d.requireCredential(d.handleDeviceKeysQuery))
	}
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
