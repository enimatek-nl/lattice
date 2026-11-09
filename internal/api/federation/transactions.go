package federation

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/federation"
	"github.com/enimatek/lattice/internal/httpx"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/matrixjson"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/roomversion"
)

// PDUAcceptor checks an event that arrived over federation and stores it, or says why
// it cannot. It is implemented by [*rooms.Service].
type PDUAcceptor interface {
	AcceptRemoteEvent(ctx context.Context, pdu []byte, verify rooms.VerifyPDU) (string, error)
	// AcceptRemoteBundle is how a single offered event is judged as well as a batch, because the
	// batch is the path that knows what to do with an event it cannot place: it stores it as an
	// outlier, whose state counts and whose absence from a client's timeline is the room's way of
	// saying that a piece is missing. `AcceptRemoteEvent` refuses those events instead, which reads
	// to a peer as a verdict on an event that is merely early.
	AcceptRemoteBundle(ctx context.Context, bundle rooms.RemoteBundle, verify rooms.VerifyPDU) (rooms.BundleOutcome, error)
}

// hole is a piece of history this server has taken and cannot yet place: the event itself, filed
// where it arrived, and the room to look underneath it in.
type hole struct {
	roomID  string
	eventID string
	// citations are the named events this one relies on and this server lacks — the shape of hole
	// that can be asked for by name, as opposed to a run of history whose names are the very thing
	// that is missing.
	citations []string
}

// transactionResponse is the body a transaction is answered with. The specification
// requires the server to answer with this shape even when individual PDUs failed,
// because a transaction that was delivered is not a transaction that failed.
type transactionResponse struct {
	PDUs map[string]transactionResult `json:"pdus"`
}

type transactionResult struct {
	// Error is only present when this PDU could not be handled. An absent error means
	// the PDU was accepted.
	Error string `json:"error,omitempty"`
}

// receivedTransaction is the request body. The field names are the specification's.
type receivedTransaction struct {
	Origin         string            `json:"origin"`
	OriginServerTS int64             `json:"origin_server_ts"`
	PDUs           []json.RawMessage `json:"pdus"`
	EDUs           []receivedEDU     `json:"edus"`
}

type receivedEDU struct {
	Type    string         `json:"edu_type"`
	Content map[string]any `json:"content"`
}

// handleSendTransaction implements PUT /_matrix/federation/v1/send/{txnId}.
//
// The request has already been authenticated by the middleware: the credential names the
// origin server, and the origin in the body must agree with it, or the transaction is
// somebody claiming to be somebody else.
func (d *Deps) handleSendTransaction(w http.ResponseWriter, r *http.Request) {
	txnID := r.PathValue("txnId")
	if txnID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"The transaction has no ID."))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, d.federationBodyLimit()))
	if err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
			"The transaction could not be read."))
		return
	}
	var transaction receivedTransaction
	if err := json.Unmarshal(body, &transaction); err != nil {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeNotJSON,
			"The transaction is not readable JSON."))
		return
	}
	if transaction.Origin == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeBadRequest,
			"The transaction names no origin server."))
		return
	}
	if credential, _ := r.Context().Value(originContextKey{}).(string); transaction.Origin != credential {
		// The signature covers the request, and the request's signature came from this
		// origin. A transaction claiming a different one is a forgery attempt, not a
		// mistake worth a gentler answer.
		d.Log.Warn("federation: a transaction named an origin its credential did not",
			"credential_origin", transaction.Origin, "body_origin", transaction.Origin, "txn_id", txnID)
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"The transaction does not come from the server it claims to."))
		return
	}
	if len(transaction.PDUs) > federation.MaxPDUs {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"A transaction may carry %d PDUs, not %d.", federation.MaxPDUs, len(transaction.PDUs)))
		return
	}
	if len(transaction.EDUs) > federation.MaxEDUs {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.Newf(matrixerr.CodeInvalidParam,
			"A transaction may carry %d EDUs, not %d.", federation.MaxEDUs, len(transaction.EDUs)))
		return
	}

	// A transaction is answered with the answer it was given the first time. Re-running
	// one is not harmless: the same event would be relayed on again, an EDU counted
	// twice.
	seen, err := d.Federation.SeenTransaction(r.Context(), transaction.Origin, txnID, time.Now().UTC())
	if err != nil {
		d.Log.Error("federation: cannot read the transaction log", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	if seen != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(seen.Response)
		return
	}

	response := transactionResponse{PDUs: map[string]transactionResult{}}
	holes := make([]hole, 0, len(transaction.PDUs))
	for _, raw := range transaction.PDUs {
		eventID, problem, missing := d.acceptPDU(r, raw)
		if missing != nil {
			holes = append(holes, *missing)
		}
		switch {
		case problem != "":
			key := eventID
			if key == "" {
				// Without an ID the sender cannot be told which event this was about,
				// so it is reported by position; an unnamed event is not something the
				// sender can correct any other way.
				key = fmt.Sprintf("unnamed-%d", len(response.PDUs))
			}
			response.PDUs[key] = transactionResult{Error: problem}
		default:
			response.PDUs[eventID] = transactionResult{}
		}
	}
	for _, edu := range transaction.EDUs {
		if edu.Type == deviceListUpdateEDU {
			d.noteDeviceListUpdate(r.Context(), transaction.Origin, edu.Content)
			continue
		}
		if edu.Type == directToDeviceEDU {
			d.noteDirectToDevice(r.Context(), transaction.Origin, edu.Content)
			continue
		}
		// Ephemeral units are acknowledged, and this server does nothing with the others yet:
		// presence, typing and receipts over federation are their own tasks. Saying so
		// in the log keeps "we received it" from quietly meaning "we acted on it".
		d.Log.Debug("federation: received an ephemeral unit this server does not use",
			"edu_type", edu.Type, "origin", transaction.Origin)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		d.Log.Error("federation: cannot encode the transaction answer", "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"The server encountered an internal error."))
		return
	}
	failed := 0
	for _, result := range response.PDUs {
		if result.Error != "" {
			failed++
		}
	}
	if err := d.Federation.RecordTransaction(r.Context(), &persist.FederationTransaction{
		Origin:      transaction.Origin,
		TxnID:       txnID,
		Response:    encoded,
		ReceivedAt:  time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(transactionRetention),
		EventCount:  len(transaction.PDUs) - failed,
		FailedCount: failed,
	}); err != nil {
		d.Log.Error("federation: cannot record the transaction", "error", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
	// Answered. Only now does this server go and repair what the answer left open — see
	// (*Deps).fillHoles for why the order is the whole design.
	d.fillHoles(r.Context(), transaction.Origin, holes)
}

// deviceListUpdateEDU is the ephemeral unit type that announces a device list change. It is the
// name the specification gives it.
const deviceListUpdateEDU = "m.device_list_update"

// noteDeviceListUpdate acts on a peer telling this server that one of the peer's users has
// devices worth re-reading.
//
// The one thing checked is whose users the peer may speak for: the announcement must name a user
// on the server that sent it. A server that announces changes for somebody else's users is not
// believed, because the alternative is letting any peer push a user into every local client's key
// tracking. This is not a signature question — the transaction as a whole is authenticated, and
// the key document inside is verified by the client that eventually reads it — it is a question of
// whose statement this is.
//
// What arrives is a prompt, not an answer to store: the local users who share a room with that user
// are told their key list is worth re-reading, and the keys themselves are fetched from the user's
// own server when somebody actually asks. Keeping a copy here would be a second place for the same
// signed fact to go stale.
func (d *Deps) noteDeviceListUpdate(ctx context.Context, origin string, content map[string]any) {
	userID, _ := content["user_id"].(string)
	deviceID, _ := content["device_id"].(string)
	if d.DeviceLists == nil || userID == "" {
		d.Log.Debug("federation: a device list update was acknowledged but not used",
			"origin", origin, "user_id", userID, "device_id", deviceID,
			"reason", "this server is not answering device list questions")
		return
	}
	domain, err := matrixid.UserDomain(userID)
	if err != nil || !matrixid.IsLocal(domain, origin) {
		d.Log.Warn("federation: a server announced a device list change for a user it does not carry",
			"origin", origin, "user_id", userID, "device_id", deviceID)
		return
	}
	d.DeviceLists.NoteRemoteDeviceListChange(ctx, userID)
	d.Log.Debug("federation: a peer announced a device list change",
		"origin", origin, "user_id", userID, "device_id", deviceID)
}

// acceptPDU checks and stores one PDU, returning its event ID and, if it could not be
// taken, the reason the sender is told.
func (d *Deps) acceptPDU(r *http.Request, raw []byte) (eventID, problem string, missing *hole) {
	var envelope struct {
		EventID string `json:"event_id"`
		RoomID  string `json:"room_id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if int64(len(raw)) > d.federationBodyLimit() {
		return envelope.EventID, "the event is too large", nil
	}
	if origin, _ := r.Context().Value(originContextKey{}).(string); d.roomExcludes(r, envelope.RoomID, origin) {
		// A transaction is answered per event, not as a whole: a room that excludes this server
		// refuses this event, and whatever else the transaction carries is still processed.
		return envelope.EventID, "the room excludes the server that sent this event", nil
	}
	verify := func(event map[string]any, rules roomversion.Rules) error {
		return events.Verify(rules, event, d.Keyring.LookupForRoom(r.Context(), rules))
	}
	// One event is offered to the room service exactly as a batch is, because the batch is the path
	// that knows what to do with an event it cannot place.
	//
	// The alternative — which is what this handler used to do — is to refuse an event whose
	// predecessors have not arrived yet, and then to go and fetch them and try again before
	// answering. Fetching is right and is kept; answering the peer with "not stored here" first is
	// not. A refusal is a verdict, and the sender is entitled to treat it as one: try another
	// server, drop the event, or never offer it again. Meanwhile the event may be perfectly good and
	// merely early, and a room whose two copies differ by one event is a room neither copy can
	// resolve its way out of.
	//
	// So the event is taken. An event with a hole beneath it becomes an outlier: its state counts,
	// it is excluded from a client's timeline until the hole closes, and its absence there is the
	// honest signal — the `limited` flag on a sync, a client paginating — that this server holds a
	// room it cannot yet narrate. Closing the hole then happens after the peer has been answered, in
	// fillHoles, and not one request earlier: closing a gap synchronously is what let a client believe
	// it had a continuous history that this server had assembled half a second before it said so.
	id, err := d.Rooms.AcceptRemoteEvent(r.Context(), raw, verify)
	if err == nil {
		return id, "", nil
	}
	// Only one kind of refusal is not a refusal, and it is the narrowest one: an event whose own
	// predecessors have not arrived yet. The event may be perfectly good and merely early, so this
	// server takes it — as an outlier, whose state counts and whose absence from a client's timeline
	// is the room's way of saying a piece is missing — and repairs the hole after the peer has been
	// answered. See (*Deps).fillHoles for why the order is the design.
	//
	// Every other refusal is kept exactly as the room service gave it. An event this server cannot
	// authorise is not an event it has accepted; a message in a room it has never heard of is not a
	// room it has agreed to join; and a hole it could close by fetching a named event is a different
	// ask, with a different endpoint, and is not quietly relabelled here.
	var predecessors *rooms.MissingPredecessorsError
	if !errors.As(err, &predecessors) || d.Rooms == nil {
		if id == "" {
			id = envelope.EventID
		}
		return id, err.Error(), nil
	}
	stored, problem, missing := d.takeEventWithHole(r, raw, envelope, verify, predecessors)
	if problem != "" {
		return stored, problem, nil
	}
	return stored, "", missing
}

// takeEventWithHole stores an event this server declined only because its predecessors are absent,
// and reports the hole it left.
//
// The second door is the room service's own batch path, which is the one that knows how to keep an
// event it cannot place: the single-event path refuses, and a refusal is a verdict that the sender is
// entitled to act on — try another server, drop the event, never offer it again.
func (d *Deps) takeEventWithHole(r *http.Request, raw []byte, envelope struct {
	EventID string `json:"event_id"`
	RoomID  string `json:"room_id"`
}, verify rooms.VerifyPDU, predecessors *rooms.MissingPredecessorsError) (string, string, *hole) {
	outcome, err := d.Rooms.AcceptRemoteBundle(r.Context(),
		rooms.RemoteBundle{PDUs: [][]byte{raw}, RoomID: envelope.RoomID}, verify)
	if err != nil {
		return envelope.EventID, err.Error(), nil
	}
	// The event's name comes back from the room service rather than out of the bytes, which is the
	// only correct source: from room version 2 a PDU carries no `event_id`, and an event that is not
	// named in the per-event answer is an event the sender cannot be told anything about.
	id, reason, refused := verdictFor(outcome, envelope.EventID)
	if refused {
		return id, reason, nil
	}
	if len(outcome.Accepted) == 0 && len(outcome.Outliers) == 0 {
		// Nothing stored, nothing refused. Saying so beats answering as though the event were safe.
		return id, "this server did not store the event", nil
	}
	if len(outcome.Outliers) == 0 {
		// Placed outright, which happens when the predecessors arrived in the moment between the
		// two doors. No hole, nothing to repair.
		return id, "", nil
	}
	name := outcome.Outliers[0]
	citations := outcome.Holes[name]
	if len(citations) == 0 {
		citations = predecessors.Missing
	}
	if envelope.RoomID == "" {
		return id, "", nil
	}
	return id, "", &hole{roomID: envelope.RoomID, eventID: name, citations: citations}
}

// verdictFor reads a single-event bundle's answer: the event's name as this server computes it, and
// the reason it was not taken if it was not.
func verdictFor(outcome rooms.BundleOutcome, fallback string) (string, string, bool) {
	for refusedID, reason := range outcome.Refused {
		if refusedID != "" {
			fallback = refusedID
		}
		return fallback, reason, true
	}
	if len(outcome.Accepted) > 0 && outcome.Accepted[0] != "" {
		return outcome.Accepted[0], "", false
	}
	return fallback, "", false
}

// transactionRetention is how long a processed transaction stays remembered. A sender
// retrying after a long outage gets a fresh answer rather than a stale one, and the
// alternative — remembering forever — is a table nobody cleans.
const transactionRetention = 24 * time.Hour

// federationBodyLimit is the cap on a federation request body.
func (d *Deps) federationBodyLimit() int64 {
	if d.Cfg != nil && d.Cfg.HTTP.FederationMaxBodyBytes > 0 {
		return d.Cfg.HTTP.FederationMaxBodyBytes
	}
	return 20 << 20
}

// originContextKey carries the origin server the request credential was verified
// against, so a handler can check that the body agrees with the signature.
type originContextKey struct{}

// requireCredential wraps a federation endpoint in the Server-Server API's request
// authentication.
//
// Nothing here is optional. An unauthenticated federation request is a stranger
// asserting that something happened in a room, and the safety of a federated room rests
// on that assertion being signed by the server that made it.
func (d *Deps) requireCredential(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, _, err := federation.VerifyRequest(r, d.Cfg.ServerName, func(origin, keyID string) (ed25519.PublicKey, error) {
			return d.Keyring.Lookup(r.Context(), origin, keyID)
		})
		if err != nil {
			// The reason goes to the log; the caller gets the code the Server-Server
			// API defines. A refusal that explains which check failed tells an attacker
			// what to try next.
			d.Log.Warn("federation: a request was refused at the credential check",
				"remote", r.RemoteAddr, "path", r.URL.Path, "error", err)
			matrixerr.Write(w, http.StatusUnauthorized, matrixerr.New(matrixerr.CodeUnauthorized,
				"The request is not signed by a server this server trusts."))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), originContextKey{}, auth.Origin)))
	}
}

// handleGetEvent implements GET /_matrix/federation/v1/event/{eventId}.
//
// A server asks for an event it was cited as a predecessor or an auth event but does not
// have. Handing it out to anybody who asks would leak the history of a private room;
// asking is authenticated, and the answer is limited to servers that have somebody
// joined in the room the event belongs to.
//
// A soft-failed event is returned. That is not an oversight: the specification says so,
// because the event is part of the room's history and the asking server may already have
// events that cite it. What this server withholds is the relay to its own clients, not
// the truth to its peers.
func (d *Deps) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("eventId")
	if eventID == "" {
		matrixerr.Write(w, http.StatusBadRequest, matrixerr.New(matrixerr.CodeMissingParams,
			"The event ID is missing."))
		return
	}
	stored, err := d.DB.Events.ByID(r.Context(), eventID)
	if err != nil || stored == nil {
		matrixerr.Write(w, http.StatusNotFound, matrixerr.Newf(matrixerr.CodeNotFound,
			"Event %q is not known to this server.", eventID))
		return
	}
	origin, _ := r.Context().Value(originContextKey{}).(string)
	if d.roomExcludes(r, stored.RoomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"The requesting server is excluded from this room by the room's access control."))
		return
	}
	if !d.serverIsInRoom(r, stored.RoomID, origin) {
		matrixerr.Write(w, http.StatusForbidden, matrixerr.New(matrixerr.CodeForbidden,
			"This server does not know you to be in the room that event belongs to."))
		return
	}
	// A peer asking for one event by name is asking for history, so it gets the same answer it
	// would get from any other history read: the room's own redactions honoured, and the body
	// withheld when the room's history visibility says this server's users could not have read
	// the event when it was written. Serving `stored.JSON` unconditionally handed out both.
	rules, err := d.rulesForRoom(r.Context(), stored.RoomID)
	if err != nil {
		var protocol *matrixerr.Error
		if errors.As(err, &protocol) && protocol.Code == matrixerr.CodeIncompatibleRoomVersion {
			matrixerr.Write(w, http.StatusForbidden, protocol)
			return
		}
		d.Log.Warn("federation: the room of an event request could not be read",
			"room_id", stored.RoomID, "event_id", eventID, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"This server could not read that room."))
		return
	}
	raw, err := d.pduForPeer(r.Context(), stored, origin, rules)
	if err != nil {
		d.Log.Warn("federation: an event could not be put into the form a peer may be given",
			"room_id", stored.RoomID, "event_id", eventID, "origin", origin, "error", err)
		matrixerr.Write(w, http.StatusInternalServerError, matrixerr.New(matrixerr.CodeUnknown,
			"This server could not serve that event."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"origin":           d.Cfg.ServerName,
		"origin_server_ts": time.Now().UnixMilli(),
		"pdus":             []json.RawMessage{json.RawMessage(raw)},
	})
}

// roomExcludes asks whether a room has thrown out the server making a request. A read endpoint
// that is not on the specification's protected list is still asked, because the reason a room
// excludes a server is that it does not want that server holding the room's events.
func (d *Deps) roomExcludes(r *http.Request, roomID, origin string) bool {
	if d.ACLs == nil || roomID == "" || origin == "" {
		return false
	}
	denied, err := d.ACLs.DeniesServer(r.Context(), roomID, origin)
	if err != nil {
		d.Log.Warn("federation: a room's server ACL could not be read, so the request is not refused on it",
			"room_id", roomID, "origin", origin, "error", err)
		return false
	}
	if denied {
		d.Log.Info("federation: a server the room excludes was refused",
			"room_id", roomID, "origin", origin, "path", r.URL.Path)
	}
	return denied
}

// serverIsInRoom reports whether the requesting homeserver has a joined user in the room.
func (d *Deps) serverIsInRoom(r *http.Request, roomID, origin string) bool {
	if origin == "" || roomID == "" {
		return false
	}
	members, err := d.DB.State.CurrentMembers(r.Context(), roomID)
	if err != nil {
		d.Log.Warn("federation: cannot read a room's members to answer an event request",
			"room_id", roomID, "error", err)
		return false
	}
	for _, member := range members {
		if domain, err := matrixid.UserDomain(member); err == nil && domain == origin {
			return true
		}
	}
	return false
}

// encodeCanonical answers with a signed object in the form its signature was made over.
//
// A signed document is not "equivalent JSON": it is the exact bytes, in the exact order,
// with the exact escapes the signer canonicalised. Re-encoding it on the way out — with
// an encoder that appends a newline, escapes HTML, or renders a number another way —
// hands the peer a document whose own signature does not check.
func encodeCanonical(w http.ResponseWriter, status int, object any) error {
	body, err := matrixjson.Canonical(object)
	if err != nil {
		return err
	}
	httpx.WriteRawJSON(w, status, body)
	return nil
}

// missingHistory decides whether an event-acceptance failure was a gap or a verdict.
//
// The distinction is the whole difference between a room this server can catch up on and a room it
// should not accept events from. A verdict — bad signature, failed authorisation, a state rule
// broken — is about the event, and asking anybody about it would be asking the same question and
// getting the same answer forever. A gap is about *this server's copy* of the room: the event may
// be perfectly good and the missing pieces are on the machine that just sent it. Conflating the
// two is how a homeserver quietly loses history, because the log line for a gap looks like the log
// line for a refusal, and the operator has no way to know that one of them was recoverable by
// making a request.
func missingHistory(err error) (rooms.MissingEventsError, bool) {
	var gap rooms.MissingEventsError
	if err == nil {
		return nil, false
	}
	if errors.As(err, &gap) {
		return gap, true
	}
	return nil, false
}
