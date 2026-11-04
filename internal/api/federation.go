package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/service"
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

	// The two key endpoints a peer asks when it wants to encrypt to somebody on this server:
	// what their devices are, and a one-time key to start a session with one of them. The
	// query form names several users at once, which is what a client encrypting for a room
	// needs; the older per-user form above is answered too, because a peer that only knows it
	// still has to be able to ask.
	// Content a peer's clients want is asked of the server its mxc URI names, and served as a
	// two-part multipart answer: a JSON description, then the bytes. The thumbnail endpoint comes
	// in two spellings - the box in the query, and the older one with the box in the path - and
	// both are answered, because peers are running versions of the specification.
	a.federated("GET /_matrix/federation/v1/media/download/{mediaId}", a.handleFederationMediaDownload)
	a.federated("GET /_matrix/federation/v1/media/thumbnail/{mediaId}", a.handleFederationMediaThumbnail)
	a.federated("GET /_matrix/federation/v1/media/thumbnail/{mediaId}/{width}/{height}", a.handleFederationMediaThumbnail)

	a.federated("POST /_matrix/federation/v1/user/keys/query", a.handleFederationKeyQuery)
	a.federated("POST /_matrix/federation/v1/user/keys/claim", a.handleFederationKeyClaim)

	// The transaction endpoint is the door all live activity comes through: events and
	// ephemeral messages arrive in batches, and the transaction id is what makes a retry
	// mean the same thing the first attempt did.
	a.federated("PUT /_matrix/federation/v1/send/{txnId}", a.handleSendTransaction)

	// A peer that was sent an event citing another event - as an auth event, or as a
	// parent - has to be able to ask for the one it is missing. Without this, a server
	// that judges arriving events can only refuse them: an auth chain it cannot complete
	// is an auth chain it cannot run.
	a.federated("GET /_matrix/federation/v1/event/{eventID}", a.handleFederationEvent)

	// The room behind an event. A peer that was sent an event cannot judge it from the
	// event alone: it needs the state the event was built on, the authority that
	// authorised it, and the prev_events it was never sent. state_ids names the state,
	// state hands one of them over, event_auth hands over an authority chain, and
	// get_missing_events fills a gap in the graph. A server that cannot answer these is
	// refused by peers that ask - the refusal reads as a complaint about the sender,
	// because from the refusing side that is what happened.
	a.federated("GET /_matrix/federation/v1/state_ids/{roomID}", a.handleFederationStateIDs)
	a.federated("GET /_matrix/federation/v1/state/{roomID}/{eventID}", a.handleFederationRoomState)
	a.federated("GET /_matrix/federation/v2/state/{roomID}/{eventID}", a.handleFederationRoomState)
	a.federated("GET /_matrix/federation/v1/event_auth/{roomID}/{eventID}", a.handleFederationEventAuth)
	a.federated("POST /_matrix/federation/v1/get_missing_events/{roomID}", a.handleFederationMissingEvents)
	// Older history, and the event nearest a moment. Backfill is how a server that joined
	// an old room learns what happened before it was here; timestamp_to_event (MSC3030)
	// is how a peer names a window in that history when it already knows roughly when the
	// thing it is looking for happened.
	a.federated("GET /_matrix/federation/v1/backfill/{roomID}", a.handleFederationBackfill)
	a.federated("POST /_matrix/federation/v1/timestamp_to_event/{roomID}", a.handleFederationTimestampToEvent)

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
	// An invite is the one membership a server receives without asking for it, so it has
	// its own endpoint: the inviting server ships the state the invite is authorised by.
	// A knock is a request to join, and the room has to see it: the knocking server signs the
	// knock against a graph it does not hold, so it asks for the shape first. The plain knock
	// endpoint is the same event carried between servers that hold a copy of the room without
	// hosting it - a user of a server already in the room still has to be able to knock.
	a.federated("GET /_matrix/federation/v1/make_knock/{roomID}/{userID}", a.handleFederationMakeKnock)
	a.federated("GET /_matrix/federation/v2/make_knock/{roomID}/{userID}", a.handleFederationMakeKnock)
	a.federated("PUT /_matrix/federation/v1/send_knock/{roomID}/{eventID}", a.handleFederationSendKnock)
	a.federated("PUT /_matrix/federation/v2/send_knock/{roomID}/{eventID}", a.handleFederationSendKnock)
	a.federated("POST /_matrix/federation/v1/knock/{roomID}/{eventID}", a.handleFederationKnock)
	a.federated("POST /_matrix/federation/v2/knock/{roomID}/{eventID}", a.handleFederationKnock)

	a.federated("PUT /_matrix/federation/v1/invite/{roomID}/{eventID}", a.handleFederationInvite)
	a.federated("PUT /_matrix/federation/v2/invite/{roomID}/{eventID}", a.handleFederationInvite)

	// A peer asked about an alias of this server. The alias is how the room is addressed,
	// so the answer has to come from the server the alias names - a client that is told
	// "not hosted here" about an alias it read somewhere has no way to go further.
	a.federated("GET /_matrix/federation/v1/query/directory", a.handleFederationDirectory)
	// ... and about a user of this server. A user ID names the server behind it, so the
	// display name and avatar of that user can only come from here: a member list on
	// another server shows the names of people it has never met by asking this question.
	a.federated("GET /_matrix/federation/v1/query/profile", a.handleFederationProfileQuery)
	// A space is a room, and its children may be anywhere. A peer walking a space that
	// starts here has to be able to see the tree this server holds of it, and a server that
	// cannot answer this question turns somebody else's space into a room with no children.
	a.federated("GET /_matrix/federation/v1/hierarchy/{roomID}", a.handleFederationHierarchy)
	// A room directory says what one server chooses to publish, so it can only be answered
	// by that server - and a client pointed at somebody else's directory has to be able to
	// reach it. v1 is the form every server knows; v3 adds a room's type and a filter that
	// is an object rather than a string.
	// The OpenID exchange is unsigned on purpose: the party asking is usually an identity
	// server working for somebody else's invite, and the credential is the token the user
	// chose to hand over. See service.OpenIDUserInfo.
	a.handle("GET /_matrix/federation/v1/openid/userinfo", a.handleFederationOpenIDUserInfo)
	a.federated("GET /_matrix/federation/v1/publicRooms", a.handleFederationPublicRooms)
	a.federated("GET /_matrix/federation/v3/publicRooms", a.handleFederationPublicRooms)
	a.federated("POST /_matrix/federation/v3/publicRooms", a.handleFederationPublicRooms)
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
	event, err := sendEventBody(r)
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

// handleFederationInvite answers PUT of invite: an invite a peer made for a user this
// server hosts, with the stripped state that authorises it. The answer is the same invite
// signed by this server as well, which is how the inviting server can later show that the
// invitation landed.
//
//	@Summary	Take an invite for a local user
//	@Description	Validates an invite event and the state shipped with it, stores the room it names, applies the membership, and answers with the invite signed by both servers.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	eventID	path	string	true	"Event id of the invite"
//	@Success	200	{object}	matrix.Object	"event, signed by this server too"
//	@Failure	400	{object}	matrix.Error	"The event is not an invite of a local user"
//	@Failure	403	{object}	matrix.Error	"The shipped state does not support the invite"
//	@Router	/_matrix/federation/v1/invite/{roomID}/{eventID} [put]
//	@Router	/_matrix/federation/v2/invite/{roomID}/{eventID} [put]
func (a *App) handleFederationInvite(w http.ResponseWriter, r *http.Request) {
	body, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	answer, err := a.svc.ServeInvite(r.Context(), r.PathValue("roomID"), r.PathValue("eventID"),
		body, federation.Peer(r.Context()))
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
//
// handleFederationMakeKnock answers GET of make_knock: the room version and the shape of the
// knock that would attach to this server's graph.
//
//	@Summary	Ask which events a knock must build on
//	@Description	Serves a peer the room version and the knock-event template whose prev_events and auth_events come from this server's graph.
//	@Tags	federation
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	userID	path	string	true	"User knocking on the room"
//	@Success	200	{object}	matrix.Object	"room_version and event"
//	@Failure	403	{object}	matrix.Error	"The room excludes that server"
//	@Failure	404	{object}	matrix.Error	"No such room"
//	@Router	/_matrix/federation/v1/make_knock/{roomID}/{userID} [get]
//	@Router	/_matrix/federation/v2/make_knock/{roomID}/{userID} [get]
func (a *App) handleFederationMakeKnock(w http.ResponseWriter, r *http.Request) {
	answer, err := a.svc.ServeMakeKnock(r.Context(), r.PathValue("roomID"), r.PathValue("userID"),
		federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationSendKnock answers PUT of send_knock: a knock event a peer signed for a room
// this server hosts.
//
//	@Summary	Take a knock a peer signed
//	@Description	Validates a knock event for a room hosted here, applies it, and relays it to the rest of the room.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	eventID	path	string	true	"Event id of the knock event"
//	@Success	200	{object}	matrix.Object	"empty, and agreed"
//	@Failure	400	{object}	matrix.Error	"The event is not a knock by the sender of itself"
//	@Failure	403	{object}	matrix.Error	"The room's rules refuse the knock"
//	@Router	/_matrix/federation/v1/send_knock/{roomID}/{eventID} [put]
//	@Router	/_matrix/federation/v2/send_knock/{roomID}/{eventID} [put]
func (a *App) handleFederationSendKnock(w http.ResponseWriter, r *http.Request) {
	body, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	answer, err := a.svc.ServeSendKnock(r.Context(), r.PathValue("roomID"), r.PathValue("eventID"), body,
		federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationKnock answers POST of knock: a knock carried between servers that hold a
// copy of the room.
//
//	@Summary	Carry a knock towards the room's server
//	@Description	Accepts a knock of a peer's user, taking it when the room is hosted here and passing it on when it is not.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	roomID	path	string	true	"Room ID"
//	@Param	eventID	path	string	true	"Event id of the knock event"
//	@Success	200	{object}	matrix.Object	"empty, and agreed"
//	@Failure	400	{object}	matrix.Error	"The event is not a knock by the sender of itself"
//	@Router	/_matrix/federation/v1/knock/{roomID}/{eventID} [post]
//	@Router	/_matrix/federation/v2/knock/{roomID}/{eventID} [post]
func (a *App) handleFederationKnock(w http.ResponseWriter, r *http.Request) {
	body, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// MSC3788 ships the state the knock is authorised with beside the event; MSC4311 asks for
	// it as complete events. The room's server keeps them, because a knock about a stranger is
	// only judgemental when the state behind it is here.
	answer, err := a.svc.ServeKnock(r.Context(), r.PathValue("roomID"), r.PathValue("eventID"),
		body, federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

func (a *App) handleFederationSendLeave(w http.ResponseWriter, r *http.Request) {
	event, err := sendEventBody(r)
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
	// The response is signed by this server. This is the older form of the question, and it
	// is the form whose answer a peer is expected to check: the payload is a list of devices
	// somebody claims to have, and the only thing between that claim and a client encrypting
	// to a stranger's key is a signature the asking server can tie to this one.
	if err := a.svc.SignAsServer(r.Context(), body); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleFederationKeyQuery answers POST /_matrix/federation/v1/user/keys/query: the device
// keys and cross-signing keys this server publishes for the users it was asked about.
//
//	@Summary	Device keys of this server's users
//	@Description	Answers a peer with the device keys and the cross-signing keys this server publishes for the users in the request, plus the device list position the answer reaches. Only users of this server are answered.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	body	body	matrix.Object	true	"device_keys: user id to device ids, empty list for all devices"
//	@Success	200	{object}	matrix.Object	"device_keys, master_keys, self_signing_keys, user_signing_keys, stream_id"
//	@Failure	400	{object}	matrix.Error	"The request body is not readable"
//	@Failure	401	{object}	matrix.Error	"The request carries no valid peer signature"
//	@Router	/_matrix/federation/v1/user/keys/query [post]
func (a *App) handleFederationKeyQuery(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DeviceKeys map[string][]string `json:"device_keys"`
	}
	if err := decodeBody(r, &request); err != nil {
		a.writeError(w, r, err)
		return
	}
	answer, err := a.svc.ServeKeyQuery(r.Context(), request.DeviceKeys)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationKeyClaim answers POST /_matrix/federation/v1/user/keys/claim: one one-time
// key per requested device of this server's users, spent as it is handed over.
//
//	@Summary	One one-time key per device
//	@Description	Hands a peer one one-time key for each device of this server's users the request names. A key is spent by the handover, so the second claim for the same device does not receive it again.
//	@Tags	federation
//	@Accept	json
//	@Produce	json
//	@Param	body	body	matrix.Object	true	"one_time_keys: user id to device id to algorithm"
//	@Success	200	{object}	matrix.Object	"one_time_keys"
//	@Failure	400	{object}	matrix.Error	"The request body is not readable"
//	@Failure	401	{object}	matrix.Error	"The request carries no valid peer signature"
//	@Router	/_matrix/federation/v1/user/keys/claim [post]
func (a *App) handleFederationKeyClaim(w http.ResponseWriter, r *http.Request) {
	var request struct {
		OneTimeKeys map[string]map[string]string `json:"one_time_keys"`
	}
	if err := decodeBody(r, &request); err != nil {
		a.writeError(w, r, err)
		return
	}
	answer, err := a.svc.ServeKeyClaim(r.Context(), request.OneTimeKeys)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationMediaDownload answers GET /_matrix/federation/v1/media/download/{mediaId}:
// the content this server hosts, as the two-part multipart answer the specification describes.
//
//	@Summary	Content this server hosts
//	@Description	Serves a peer the content behind a media id this server hosts as multipart/mixed with exactly two parts: a JSON description carrying media_type, media_length and upload_name, then the bytes themselves.
//	@Tags	federation
//	@Produce	multipart/mixed
//	@Param	mediaId	path	string	true	"Media id"
//	@Success	200	{file}	binary	"multipart/mixed: a JSON description and the content"
//	@Failure	401	{object}	matrix.Error	"The request carries no valid peer signature"
//	@Failure	404	{object}	matrix.Error	"No such media"
//	@Router	/_matrix/federation/v1/media/download/{mediaId} [get]
func (a *App) handleFederationMediaDownload(w http.ResponseWriter, r *http.Request) {
	media, err := a.svc.FederationMedia(r.Context(), r.PathValue("mediaId"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeMultipartMedia(w, r, matrix.Object{
		"media_type":   media.ContentType,
		"media_length": len(media.Blob),
		"upload_name":  media.FileName,
	}, media.ContentType, media.Blob)
}

// handleFederationMediaThumbnail answers the federation thumbnail endpoint, whose box is given
// in the query by current servers and in the path by older ones.
//
//	@Summary	A thumbnail of content this server hosts
//	@Description	Scales an image this server hosts to the requested box and serves it to a peer as multipart/mixed with exactly two parts: a JSON description carrying content_type, then the image.
//	@Tags	federation
//	@Produce	multipart/mixed
//	@Param	mediaId	path	string	true	"Media id"
//	@Param	width	path	int	false	"Width of the box (older spelling)"
//	@Param	height	path	int	false	"Height of the box (older spelling)"
//	@Success	200	{file}	binary	"multipart/mixed: a JSON description and the thumbnail"
//	@Failure	401	{object}	matrix.Error	"The request carries no valid peer signature"
//	@Failure	404	{object}	matrix.Error	"No such media, or nothing to thumbnail"
//	@Router	/_matrix/federation/v1/media/thumbnail/{mediaId} [get]
//	@Router	/_matrix/federation/v1/media/thumbnail/{mediaId}/{width}/{height} [get]
func (a *App) handleFederationMediaThumbnail(w http.ResponseWriter, r *http.Request) {
	width, err := queryInt(r, "width", 128)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	height, err := queryInt(r, "height", 128)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// The older spelling puts the box in the path. It wins when it is there, because a peer that
	// sent it named the box twice at worst, and a peer that sent only the query never fills the
	// path.
	if inPath := r.PathValue("width"); inPath != "" {
		if parsed, err := strconv.Atoi(inPath); err == nil && parsed > 0 {
			width = parsed
		}
	}
	if inPath := r.PathValue("height"); inPath != "" {
		if parsed, err := strconv.Atoi(inPath); err == nil && parsed > 0 {
			height = parsed
		}
	}
	data, contentType, err := a.svc.FederationThumbnail(r.Context(), r.PathValue("mediaId"),
		width, height, queryValue(r, "method", "scale"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	a.writeMultipartMedia(w, r, matrix.Object{"content_type": contentType}, contentType, data)
}

// writeMultipartMedia writes the two-part answer the federation media endpoints are specified to
// give: a JSON description of the content, then the content.
//
// Exactly two parts, in that order. Some servers decide what a response is by counting, and an
// answer with a third part in it - a second description, an empty trailer - is an answer a peer
// may throw away rather than parse.
func (a *App) writeMultipartMedia(w http.ResponseWriter, r *http.Request, description matrix.Object, contentType string, body []byte) {
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", "multipart/mixed; boundary="+writer.Boundary())
	encoded, err := json.Marshal(description)
	if err != nil {
		a.writeError(w, r, matrix.ErrInternal(err))
		return
	}
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": []string{"application/json"}})
	if err != nil {
		return
	}
	if _, err := part.Write(encoded); err != nil {
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	part, err = writer.CreatePart(textproto.MIMEHeader{"Content-Type": []string{contentType}})
	if err != nil {
		return
	}
	if _, err := part.Write(body); err != nil {
		return
	}
	_ = writer.Close()
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

// handleFederationProfileQuery answers GET /_matrix/federation/v1/query/profile.
func (a *App) handleFederationProfileQuery(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	answer, err := a.svc.AnswerProfileQuery(r.Context(), query.Get("user_id"), query.Get("field"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationOpenIDUserInfo answers GET /_matrix/federation/v1/openid/userinfo.
func (a *App) handleFederationOpenIDUserInfo(w http.ResponseWriter, r *http.Request) {
	answer, err := a.svc.OpenIDUserInfo(r.Context(), r.URL.Query().Get("access_token"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationPublicRooms answers the federation forms of the room directory: v1 as a
// GET with a filter in the query, v3 as a GET or a POST with a filter object.
func (a *App) handleFederationPublicRooms(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 0)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	filter := queryValue(r, "filter_generic_search_terms", "")
	since := queryValue(r, "since", "")
	if r.Method == http.MethodPost {
		var body struct {
			Limit  int    `json:"limit"`
			Since  string `json:"since"`
			Filter struct {
				GenericSearchTerm string `json:"generic_search_term"`
			} `json:"filter"`
		}
		// A body this server cannot read is not a reason to refuse the page: a peer that
		// sent a filter in a shape we do not know still wants the directory, and answering
		// it unfiltered is a smaller lie than answering it with an error. What is said out
		// loud is that the filter was not applied.
		if err := decodeBody(r, &body); err != nil {
			log.Printf("federation: %s sent a room directory request this server cannot read: %v",
				federation.Peer(r.Context()), err)
		} else {
			if body.Limit > 0 {
				limit = body.Limit
			}
			if body.Since != "" {
				since = body.Since
			}
			filter = body.Filter.GenericSearchTerm
		}
	}
	answer, err := a.svc.ServePublicRooms(r.Context(), federation.Peer(r.Context()), filter, limit, since)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationHierarchy answers GET /_matrix/federation/v1/hierarchy/{roomID}.
func (a *App) handleFederationHierarchy(w http.ResponseWriter, r *http.Request) {
	// The federation endpoint names one parameter and does not paginate: it answers one room and
	// its children, and a walk that wants more asks again.
	result, err := a.svc.ServeHierarchy(r.Context(), r.PathValue("roomID"), federation.Peer(r.Context()),
		service.HierarchyParams{
			SuggestedOnly: queryBool(r, "suggested_only", false),
		})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleFederationStateIDs answers GET /_matrix/federation/v1/state_ids/{roomID}.
func (a *App) handleFederationStateIDs(w http.ResponseWriter, r *http.Request) {
	answer, err := a.svc.ServeStateIDs(r.Context(), r.PathValue("roomID"), federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationRoomState answers GET /_matrix/federation/v1|v2/state/{roomID}/{eventID}.
// Version 1 answers the event and version 2 an object holding it, which is the same
// difference the join handshake has between its two versions.
func (a *App) handleFederationRoomState(w http.ResponseWriter, r *http.Request) {
	answer, err := a.svc.ServeRoomStateEvent(r.Context(), r.PathValue("roomID"),
		r.PathValue("eventID"), federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	if strings.Contains(r.URL.Path, "/federation/v1/") {
		writeJSON(w, http.StatusOK, answer["event"])
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationEventAuth answers GET /_matrix/federation/v1/event_auth/{roomID}/{eventID}.
func (a *App) handleFederationEventAuth(w http.ResponseWriter, r *http.Request) {
	answer, err := a.svc.ServeEventAuth(r.Context(), r.PathValue("roomID"),
		r.PathValue("eventID"), federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationMissingEvents answers POST /_matrix/federation/v1/get_missing_events/{roomID}.
func (a *App) handleFederationMissingEvents(w http.ResponseWriter, r *http.Request) {
	body, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	answer, err := a.svc.ServeMissingEvents(r.Context(), r.PathValue("roomID"),
		federation.Peer(r.Context()), body)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

func (a *App) handleFederationEvent(w http.ResponseWriter, r *http.Request) {
	body, err := a.svc.FederatedEvent(r.Context(), r.PathValue("eventID"), federation.Peer(r.Context()))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// handleFederationBackfill answers GET /_matrix/federation/v1/backfill/{roomID}.
func (a *App) handleFederationBackfill(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			a.writeError(w, r, matrix.ErrInvalidParam(`"limit" must be a whole number`))
			return
		}
		limit = parsed
	}
	answer, err := a.svc.ServeBackfill(r.Context(), r.PathValue("roomID"),
		federation.Peer(r.Context()), limit, r.URL.Query().Get("since"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

// handleFederationTimestampToEvent answers POST
// /_matrix/federation/v1/timestamp_to_event/{roomID}.
func (a *App) handleFederationTimestampToEvent(w http.ResponseWriter, r *http.Request) {
	body, err := federationBody(r)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	answer, err := a.svc.ServeTimestampToEvent(r.Context(), r.PathValue("roomID"),
		federation.Peer(r.Context()), body)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, answer)
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
// eventInBody reads the event a knock endpoint carries. Unlike send_leave, whose body is
// the event, the knock endpoints wrap it: {"event": ...}.
func eventInBody(body matrix.Object) (matrix.Object, error) {
	event, ok := body["event"].(matrix.Object)
	if !ok {
		return nil, matrix.ErrInvalidRequest("the body carries no event")
	}
	return event, nil
}

// sendEventBody reads the event a send_join or send_leave body carries. The older of these
// endpoints put the event at the top of the body; the newer ones wrap it in {"event": ...},
// and servers that were writing this protocol before the wrap arrived keep sending the bare
// form whatever the version says. Both are read, and telling them apart costs nothing: an
// event names its own type, which is not a property an envelope happens to have.
func sendEventBody(r *http.Request) (matrix.Object, error) {
	body, err := federationBody(r)
	if err != nil {
		return nil, err
	}
	if wrapped, ok := body["event"].(matrix.Object); ok && federation.EventField(wrapped, "type") != "" {
		return wrapped, nil
	}
	return body, nil
}

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
