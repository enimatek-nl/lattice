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
	for _, raw := range transaction.PDUs {
		eventID, problem := d.acceptPDU(r, raw)
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
		// Ephemeral units are acknowledged, and this server does nothing with them yet:
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
}

// acceptPDU checks and stores one PDU, returning its event ID and, if it could not be
// taken, the reason the sender is told.
func (d *Deps) acceptPDU(r *http.Request, raw []byte) (eventID, problem string) {
	var envelope struct {
		EventID string `json:"event_id"`
		RoomID  string `json:"room_id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if int64(len(raw)) > d.federationBodyLimit() {
		return envelope.EventID, "the event is too large"
	}
	if origin, _ := r.Context().Value(originContextKey{}).(string); d.roomExcludes(r, envelope.RoomID, origin) {
		// A transaction is answered per event, not as a whole: a room that excludes this server
		// refuses this event, and whatever else the transaction carries is still processed.
		return envelope.EventID, "the room excludes the server that sent this event"
	}
	verify := func(event map[string]any, rules roomversion.Rules) error {
		return events.Verify(rules, event, d.Keyring.LookupForRoom(r.Context(), rules))
	}
	id, err := d.Rooms.AcceptRemoteEvent(r.Context(), raw, verify)
	var missing *rooms.MissingPredecessorsError
	if errors.As(err, &missing) && d.Backfill != nil && envelope.RoomID != "" {
		// The server that just offered this event plainly has the events it descends from. Asking
		// it for those, and re-offering once, is the difference between a room this server can
		// catch up on and a room it will keep refusing for as long as the hole stays. One ask, one
		// retry: a sender that keeps offering unplaceable events is not given an open-ended fetch.
		origin, _ := r.Context().Value(originContextKey{}).(string)
		if _, fillErr := d.Backfill.FillFrom(r.Context(), envelope.RoomID, []string{origin}, missing.Missing); fillErr != nil {
			d.Log.Warn("federation: the events an incoming event descends from could not be fetched",
				"event_id", envelope.EventID, "room_id", envelope.RoomID, "origin", origin, "error", fillErr)
		} else {
			id, err = d.Rooms.AcceptRemoteEvent(r.Context(), raw, verify)
		}
	}
	if err != nil {
		if id == "" {
			id = envelope.EventID
		}
		d.Log.Debug("federation: refused an event",
			"event_id", id, "room_id", envelope.RoomID, "reason", err.Error())
		// The sender is told the reason in the per-PDU result, which is where the
		// specification puts it: a transaction that was delivered is answered, not
		// errored as a whole.
		return id, err.Error()
	}
	return id, ""
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"origin":           d.Cfg.ServerName,
		"origin_server_ts": time.Now().UnixMilli(),
		"pdus":             []json.RawMessage{json.RawMessage(stored.JSON)},
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
