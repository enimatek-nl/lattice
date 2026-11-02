package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The server-server API begins with identity: a peer has to know which server it
// is talking to, which key that server signs with, and that the request it
// received came from the server it claims to come from. Everything in this file is
// the ground those three facts are established on, and every later step - sending
// a transaction, accepting a join, pinning an event into a room - is mounted on
// top of it.
//
// All of it is behind -federation. With the flag off, no route below is registered,
// so the paths answer M_UNRECOGNIZED like any other unknown path and the binary is
// the single-server Lattice it was before.

// federated registers a route that only a verified peer may reach. The wrapper
// reads the body, checks the X-Matrix signature over the request as it arrived,
// and puts the peer's server name on the context; handlers read the origin from
// there and never from a body field.
func (a *App) federated(pattern string, handler http.HandlerFunc) {
	a.handle(pattern, a.federation.Authenticate(handler).ServeHTTP)
}

// registerFederationRoutes mounts the server-server routes. It runs only when the
// flag is on.
func (a *App) registerFederationRoutes() {
	// Server keys are published to anybody: a peer that has not yet been able to
	// verify a signature needs the key to verify with, and the answer is signed by
	// the key it describes, so it carries its own proof.
	a.handle("GET /_matrix/key/v2/server", a.handleServerKeys)
	a.handle("GET /_matrix/key/v2/server/{keyID}", a.handleServerKey)
	// A key query makes this server dial out on a stranger's behalf, so it is
	// charged against the same per-address budget as a login attempt. Publishing our
	// own key is not charged: peers fetch it constantly, and the work is local.
	a.anonymousLimited("POST /_matrix/key/v2/query", limitAuth, a.handleKeyQuery)
	a.anonymousLimited("GET /_matrix/key/v2/query/{serverName}", limitAuth, a.handleKeyQueryServer)

	a.handle("GET /_matrix/federation/v1/version", a.handleFederationVersion)
	a.federated("GET /_matrix/federation/v1/user/devices/{userId}", a.handleFederationDevices)

	// The transaction endpoint is the door all live activity comes through: events and
	// ephemeral messages arrive in batches, and the transaction id is what makes a retry
	// mean the same thing the first attempt did.
	a.federated("PUT /_matrix/federation/v1/send/{txnId}", a.handleSendTransaction)

	// A peer that was sent an event citing another event - as an auth event, or as a
	// parent - has to be able to ask for the one it is missing. Without this, a server
	// that judges arriving events can only refuse them: an auth chain it cannot complete
	// is an auth chain it cannot run.
	a.federated("GET /_matrix/federation/v1/event/{eventID}", a.handleFederationEvent)

	// The join handshake. A server joining a room it does not host cannot know which
	// events its join has to build on, and cannot describe a room it has never been
	// handed; make_join asks the first question and send_join answers the second. Both
	// are served for version 1 and version 2, which differ in the shape of the answer.
	a.federated("GET /_matrix/federation/v1/make_join/{roomID}/{userID}", a.handleFederationMakeJoin)
	a.federated("GET /_matrix/federation/v2/make_join/{roomID}/{userID}", a.handleFederationMakeJoin)
	a.federated("PUT /_matrix/federation/v1/send_join/{roomID}/{eventID}", a.handleFederationSendJoin)
	a.federated("PUT /_matrix/federation/v2/send_join/{roomID}/{eventID}", a.handleFederationSendJoin)
	// A leave is the mirror of a join: the room's server hands out a shape, the leaving
	// server signs it, and the room's server decides whether it stands.
	a.federated("GET /_matrix/federation/v1/make_leave/{roomID}/{userID}", a.handleFederationMakeLeave)
	a.federated("GET /_matrix/federation/v2/make_leave/{roomID}/{userID}", a.handleFederationMakeLeave)
	a.federated("PUT /_matrix/federation/v1/send_leave/{roomID}/{eventID}", a.handleFederationSendLeave)
	a.federated("PUT /_matrix/federation/v2/send_leave/{roomID}/{eventID}", a.handleFederationSendLeave)

	// A peer asked about an alias of this server. The alias is how the room is addressed,
	// so the answer has to come from the server the alias names - a client that is told
	// "not hosted here" about an alias it read somewhere has no way to go further.
	a.federated("GET /_matrix/federation/v1/query/directory", a.handleFederationDirectory)
}

// handleFederationMakeJoin answers GET of make_join: the room version, and the shape of
// the join event that would attach to this server's graph.
//
//	@Summary	Ask which events a join must build on
//	@Description	Serves a peer the room version and the join-event template whose prev_events and auth_events come from this server's graph.
//	@Tags	federation
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	userID	path	string	true	"User joining the room"
//	@Success	200	{object}	matrix.Object	"room_version and event"
//	@Failure	403	{object}	matrix.Error	"The room excludes that server"
//	@Failure	404	{object}	matrix.Error	"No such room"
//	@Router	/_matrix/federation/v1/make_join/{roomID}/{userID} [get]
//	@Router	/_matrix/federation/v2/make_join/{roomID}/{userID} [get]
func (a *App) handleFederationMakeJoin(w http.ResponseWriter, r *http.Request) {
	body, err := a.svc.ServeMakeJoin(r.Context(), r.PathValue("roomID"), r.PathValue("userID"),
		federation.Peer(r.Context()), r.URL.Query()["ver"])
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleFederationSendJoin answers PUT of send_join: a join event a peer made for a room
// this server hosts, answered with the room's state.
//
//	@Summary	Take a join and hand back the room's state
//	@Description	Validates a join event for a room hosted here, applies it, and answers with the state - the servers in the room in version 2, the state and its auth chain in version 1.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	eventID	path	string	true	"Event id of the join event"
//	@Success	200	{object}	matrix.Object	"state, and auth_chain or servers_in_room"
//	@Failure	400	{object}	matrix.Error	"The event is not a join for the server's own user"
//	@Failure	403	{object}	matrix.Error	"The room's rules refuse the join"
//	@Router	/_matrix/federation/v1/send_join/{roomID}/{eventID} [put]
//	@Router	/_matrix/federation/v2/send_join/{roomID}/{eventID} [put]
func (a *App) handleFederationSendJoin(w http.ResponseWriter, r *http.Request) {
	event, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	v2 := strings.Contains(r.URL.Path, "/federation/v2/")
	answer, err := a.svc.ServeSendJoin(r.Context(), r.PathValue("roomID"), r.PathValue("eventID"),
		event, federation.Peer(r.Context()), v2, r.URL.Query().Get("omit_members") == "true")
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if !v2 {
		// Version 1 of the endpoint predates the convention of answering with the body
		// alone, and a client that fell back to it expects the two element array.
		writeJSON(w, http.StatusOK, []any{http.StatusOK, answer})
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationMakeLeave answers GET of make_leave: the room version, and the shape
// of the leave event that would attach to this server's graph.
//
//	@Summary	Ask which events a leave must build on
//	@Description	Serves a peer the room version and the leave-event template whose prev_events and auth_events come from this server's graph.
//	@Tags	federation
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	userID	path	string	true	"User leaving the room"
//	@Success	200	{object}	matrix.Object	"room_version and event"
//	@Failure	403	{object}	matrix.Error	"The room excludes that server"
//	@Failure	404	{object}	matrix.Error	"No such room"
//	@Router	/_matrix/federation/v1/make_leave/{roomID}/{userID} [get]
//	@Router	/_matrix/federation/v2/make_leave/{roomID}/{userID} [get]
func (a *App) handleFederationMakeLeave(w http.ResponseWriter, r *http.Request) {
	answer, err := a.svc.ServeMakeLeave(r.Context(), r.PathValue("roomID"), r.PathValue("userID"),
		federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationSendLeave answers PUT of send_leave: a leave event a peer signed for a
// room this server hosts, answered with agreement rather than with state - the leaving
// server already has the room.
//
//	@Summary	Take a leave a peer signed
//	@Description	Validates a leave event for a room hosted here, applies it, and relays it to the rest of the room.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	eventID	path	string	true	"Event id of the leave event"
//	@Success	200	{object}	matrix.Object	"empty, and agreed"
//	@Failure	400	{object}	matrix.Error	"The event is not a leave by the sender of itself"
//	@Failure	403	{object}	matrix.Error	"The room's rules refuse the leave"
//	@Router	/_matrix/federation/v1/send_leave/{roomID}/{eventID} [put]
//	@Router	/_matrix/federation/v2/send_leave/{roomID}/{eventID} [put]
func (a *App) handleFederationSendLeave(w http.ResponseWriter, r *http.Request) {
	event, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	answer, err := a.svc.ServeSendLeave(r.Context(), r.PathValue("roomID"), r.PathValue("eventID"),
		event, federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if strings.Contains(r.URL.Path, "/federation/v1/") {
		writeJSON(w, http.StatusOK, []any{http.StatusOK, answer})
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleServerKeys answers GET /_matrix/key/v2/server.
//
//	@Summary\tThis server's signing key
//	@Description\tPublishes the ed25519 key this homeserver signs federation requests and events with, signed by that key itself. Peers fetch it directly; a notary fetches it, checks it, and re-signs it.
//	@Tags\tfederation
//	@Produce\tjson
//	@Success\t200\t{object}	matrix.Object\t"The signed key document"
//	@Router\t/_matrix/key/v2/server [get]
func (a *App) handleServerKeys(w http.ResponseWriter, r *http.Request) {
	body, err := a.svc.ServerKeysDocumentBody(r.Context())
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeRawJSON(w, http.StatusOK, body)
}

// handleServerKey answers the {keyID} variant. The specification returns the whole
// document for any key id the server knows, so the path parameter is a question
// about which key, not a request for a fragment of the answer.
//
//	@Summary\tThis server's signing key by key ID
//	@Description\tThe same signed document as the unversioned path, for a key id this server publishes. An unknown key id is refused.
//	@Tags\tfederation
//	@Produce\tjson
//	@Param\tkeyID\tpath\tstring\ttrue\t"Key ID of the form ed25519:id"
//	@Success\t200\t{object}	matrix.Object\t"The signed key document"
//	@Failure\t404\t{object}	matrix.Error
//	@Router\t/_matrix/key/v2/server/{keyID} [get]
func (a *App) handleServerKey(w http.ResponseWriter, r *http.Request) {
	keyID := r.PathValue("keyID")
	found, err := a.svc.HasSigningKey(r.Context(), keyID)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if !found {
		a.writeError(w, r, matrix.ErrNotFound("this server does not publish %s", keyID))
		return
	}
	a.handleServerKeys(w, r)
}

// keyQueryBody is the notary request: the servers to look up, each with the keys
// the requester already holds and how long it needs the answer to stay valid. The
// criteria are not ignored - a request that wants keys valid until next week gets
// a fresh fetch rather than a cached document that expires tomorrow.
type keyQueryBody struct {
	ServerKeys map[string]map[string]struct {
		MinimumValidUntilTS int64 `json:"minimum_valid_until_ts"`
	} `json:"server_keys"`
	MinimumValidUntilTS int64 `json:"minimum_valid_until_ts"`
}

// handleKeyQuery answers POST /_matrix/key/v2/query, the notary endpoint.
//
// A notary's value is that the requester does not have to trust the peer whose
// keys it wants: it trusts the notary, and the answer carries both signatures.
// That is only worth anything if the notary verified what it signed, so the
// document is fetched through the same path that checks a peer's self-signature,
// its server_name, and its expiry. Lattice acts as a notary for others; acting as
// a client of someone else's notary is not implemented, and is recorded in
// TODO.md.
//
//	@Summary\tServer keys, notarised
//	@Description\tFetches the signing keys of other servers, verifies them, and re-signs them with this server's key so the requester can rely on this server's word instead of the peer's.
//	@Tags\tfederation
//	@Accept\tjson
//	@Produce\tjson
//	@Param\tbody\tbody\tkeyQueryBody\ttrue\t"Server names to look up"
//	@Success\t200\t{object}	matrix.Object\t"server_keys and failures"
//	@Router\t/_matrix/key/v2/query [post]
func (a *App) handleKeyQuery(w http.ResponseWriter, r *http.Request) {
	var body keyQueryBody
	if err := decodeBody(r, &body); err != nil {
		a.writeError(w, r, err)
		return
	}
	wanted := map[string]int64{}
	for serverName, criteria := range body.ServerKeys {
		until := body.MinimumValidUntilTS
		for _, criterion := range criteria {
			if criterion.MinimumValidUntilTS > until {
				until = criterion.MinimumValidUntilTS
			}
		}
		wanted[serverName] = until
	}
	a.notarise(w, r, wanted)
}

// handleKeyQueryServer answers GET /_matrix/key/v2/query/{serverName}, the same
// service in its single-server spelling.
//
//	@Summary\tServer keys of one server, notarised
//	@Description\tThe notary answer for one server name.
//	@Tags\tfederation
//	@Produce\tjson
//	@Param\tserverName\tpath\tstring\ttrue\t"Server name to look up"
//	@Param\tminimum_valid_until_ts\tquery\tint\tfalse\t"Earliest timestamp the answer must stay valid until"
//	@Success\t200\t{object}	matrix.Object\t"server_keys and failures"
//	@Router\t/_matrix/key/v2/query/{serverName} [get]
func (a *App) handleKeyQueryServer(w http.ResponseWriter, r *http.Request) {
	until, err := a.queryInt(w, r, "minimum_valid_until_ts")
	if err != nil {
		return
	}
	a.notarise(w, r, map[string]int64{r.PathValue("serverName"): until})
}

// notarise fetches, verifies and re-signs, and answers with the array of documents
// plus the map of servers it could not reach. A peer that is unreachable is a
// failure entry, not an error response: a requester asking about five servers can
// still use the four that answered.
func (a *App) notarise(w http.ResponseWriter, r *http.Request, wanted map[string]int64) {
	if a.federation == nil {
		a.writeError(w, r, matrix.ErrUnrecognized("%s", r.URL.Path))
		return
	}
	documents := matrix.Array{}
	failures := matrix.Object{}
	for serverName, until := range wanted {
		if serverName == "" {
			continue
		}
		_, document, err := a.federation.PeerKeys(r.Context(), serverName, until)
		if err != nil {
			failures[serverName] = err.Error()
			continue
		}
		signed, err := a.svc.Notarise(r.Context(), document)
		if err != nil {
			failures[serverName] = err.Error()
			continue
		}
		documents = append(documents, signed)
	}
	writeJSON(w, http.StatusOK, matrix.Object{
		"server_keys": documents,
		"failures":    failures,
	})
}

// handleFederationVersion answers GET /_matrix/federation/v1/version, which is not
// authenticated: an operator comparing what two servers run should be able to ask
// before either side has agreed on a key.
//
// The specification leaves the values free-form and suggests naming the
// implementation rather than claiming compatibility: a version string that reads
// like Synapse's is how a server ends up being sent traffic it cannot handle.
//
//	@Summary\tSoftware running on this server
//	@Description\tThe implementation name and version, for operators comparing two homeservers. Not authenticated.
//	@Tags\tfederation
//	@Produce\tjson
//	@Success\t200\t{object}	matrix.Object\t"server.name and server.version"
//	@Router\t/_matrix/federation/v1/version [get]
func (a *App) handleFederationVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, matrix.Object{
		"server": matrix.Object{"name": "Lattice", "version": a.cfg.Version},
	})
}

// handleFederationDevices answers GET /_matrix/federation/v1/user/devices/{userId},
// which is how a peer learns which devices to send an encrypted message to. The
// device keys are handed over exactly as their owners uploaded them: the signatures
// on them are the user's own, and re-encoding the object breaks them.
//
//	@Summary\tDevice keys of a local user
//	@Description\tServes another homeserver the device keys, cross-signing keys and device list version of a user that lives here.
//	@Tags\tfederation
//	@Produce\tjson
//	@Param\tuserId\tpath\tstring\ttrue\t"User ID local to this server"
//	@Success\t200\t{object}	matrix.Object\t"user_id, devices and stream_id"
//	@Failure\t401\t{object}	matrix.Error\t"The request carries no valid peer signature"
//	@Failure\t404\t{object}	matrix.Error\t"No such user"
//	@Router\t/_matrix/federation/v1/user/devices/{userId} [get]
func (a *App) handleFederationDevices(w http.ResponseWriter, r *http.Request) {
	// The service answers for a user this server does not have, which covers a
	// well-formed ID of another server just as it covers a missing one: the peer
	// asked the wrong place, and M_NOT_FOUND says so without leaking whether the
	// name is in use elsewhere.
	list, err := a.svc.FederatedDevices(r.Context(), r.PathValue("userId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	body := matrix.Object{
		"user_id":   list.UserID,
		"stream_id": list.StreamID,
		"devices":   list.Devices,
	}
	// The cross-signing keys are optional in the response, and left out when the
	// user has none: a peer reads an absent key as "this user has no cross-signing",
	// which is a different statement from an empty object.
	for key, object := range map[string]matrix.Object{
		"master_key":       list.MasterKey,
		"self_signing_key": list.SelfSigningKey,
		"user_signing_key": list.UserSigningKey,
	} {
		if object != nil {
			body[key] = object
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// handleFederationEvent answers GET /_matrix/federation/v1/event/{eventID}: one event,
// in the form its room version defines, to a server that shares the room.
//
//	@Summary\tOne event by id
//	@Description\tServes a peer an event it needs to complete the auth chain or the graph of an event it was sent.
//	@Tags\tfederation
//	@Produce\tjson
//	@Param\teventID\tpath\tstring\ttrue\t"Event id"
//	@Success\t200\t{object}\tmatrix.Object\t"origin and pdus"
//	@Failure\t401\t{object}\tmatrix.Error\t"The request carries no valid peer signature"
//	@Failure\t403\t{object}\tmatrix.Error\t"The server is not part of the room"
//	@Failure\t404\t{object}\tmatrix.Error\t"No such event"
//	@Router\t/_matrix/federation/v1/event/{eventID} [get]
//
// handleFederationDirectory answers GET /_matrix/federation/v1/query/directory: what one
// of this server's aliases points at, and which servers are in the room behind it.
//
//	@Summary	Resolve a room alias
//	@Description	Serves a peer the room id an alias of this server maps to, with the servers that can be contacted for it.
//	@Tags	federation
//	@Produce	json
//	@Param	room_alias	query	string	true	"Alias to resolve"
//	@Success	200	{object}	matrix.Object	"room_id and servers"
//	@Failure	403	{object}	matrix.Error	"The room excludes that server"
//	@Failure	404	{object}	matrix.Error	"No such alias"
//	@Router	/_matrix/federation/v1/query/directory [get]
func (a *App) handleFederationDirectory(w http.ResponseWriter, r *http.Request) {
	body, err := a.svc.DirectoryAnswer(r.Context(), r.URL.Query().Get("room_alias"), federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *App) handleFederationEvent(w http.ResponseWriter, r *http.Request) {
	body, err := a.svc.FederatedEvent(r.Context(), r.PathValue("eventID"), federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleSendTransaction answers PUT /_matrix/federation/v1/send/{txnId}.
//
// The peer is the one the middleware verified, and the service refuses a body whose
// claimed origin disagrees with it. A transaction that has been answered before gets the
// earlier answer rather than a second processing, which is the difference between a
// retry and a duplicate event in a room.
//
//	@Summary	Deliver a federation transaction
//	@Description	Accepts a batch of events and ephemeral messages from a peer, applying the ephemeral ones and queueing the events for the room. A repeated transaction id from the same server is answered with the previous result.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	txnId	path	string	true	"Transaction id, unique per sending server"
//	@Param	body	body	matrix.Object	true	"The transaction: origin, origin_server_ts, pdus and edus"
//	@Success	200	{object}	matrix.Object	"Per-event results"
//	@Failure	400	{object}	matrix.Error	"The transaction is malformed or oversized"
//	@Failure	401	{object}	matrix.Error	"The request is not signed by the origin it claims"
//	@Router	/_matrix/federation/v1/send/{txnId} [put]
func (a *App) handleSendTransaction(w http.ResponseWriter, r *http.Request) {
	body, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	result, err := a.svc.HandleSendTransaction(r.Context(), federation.Peer(r.Context()), r.PathValue("txnId"), body)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// federationBody reads a request body as an object with its numbers kept exactly as
// written. A PDU inside a transaction is stored, hashed and identified by the bytes it
// encodes to, so a body that passes through float64 on the way in becomes an event that
// its own content hash does not cover - the difference is invisible in a log and fatal
// at the first signature check.
func federationBody(r *http.Request) (matrix.Object, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, federation.MaxTransactionBytes))
	if err != nil {
		return nil, matrix.ErrNotJSON("could not read the request body")
	}
	value, err := matrix.DecodeCanonical(raw)
	if err != nil {
		return nil, matrix.ErrNotJSON("the transaction body is not valid JSON: %v", err)
	}
	object, ok := value.(matrix.Object)
	if !ok {
		return nil, matrix.ErrBadJSON(fmt.Errorf("the transaction body is not an object"))
	}
	return object, nil
}

// queryInt reads an optional integer query parameter, answering the request itself
// when the value cannot be one.
func (a *App) queryInt(w http.ResponseWriter, r *http.Request, name string) (int64, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		a.writeError(w, r, matrix.ErrInvalidRequest("%q must be an integer", name))
		return 0, err
	}
	return value, nil
}
