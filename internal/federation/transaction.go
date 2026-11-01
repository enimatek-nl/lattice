package federation

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A transaction is the envelope federation puts around live activity: one PUT per
// transaction, carrying a batch of events and a batch of ephemeral messages, named by
// a transaction id that makes a retry mean the same thing the first attempt did. Two
// properties of that envelope decide most of the code in this file. The first is that
// the id is the unit of idempotence: a peer that cannot tell a retry from a first
// attempt will apply an event twice, and an event applied twice in a room cannot be
// undone. The second is that a transaction is a batch, so its limits are the unit of
// work - too big and a peer spends a request responding to nothing it can act on.

// The specification's limits on one transaction. They are not performance tuning: a
// transaction is acknowledged as a whole and retried as a whole, so a server that
// accepts an oversized one has agreed to re-send, and possibly re-process, everything
// in it because of one bad event at the end.
const (
	MaxPDUsPerTransaction = 50
	MaxEDUsPerTransaction = 100
)

// MaxTransactionBytes bounds the body of a send request before anything in it is
// parsed. The per-PDU size limit and the count limits already bound the work, but the
// reader needs a number to stop at, and 50 x 65536 plus the EDUs around them says what
// a legitimate transaction can be.
const MaxTransactionBytes = 50*MaxPDUBytes + 2<<20

// MaxPDUBytes is the maximum size of a single PDU. Every room version since version 2
// names 65536 bytes, and the limit is not politeness: the size is bounded so that a
// server can verify an event - hash it, sign it, resolve it - with a known amount of
// work. An oversized event also cannot be relayed onward, so accepting one costs every
// server in the room forever.
const MaxPDUBytes = 65536

// MaxTxnIDLength is how long a transaction id may be. The specification leaves the
// format to the sender and only requires uniqueness per origin; the cap keeps a peer
// from naming its way into an unbounded key in storage.
const MaxTxnIDLength = 255

// Transaction is the body of PUT /_matrix/federation/v1/send/{txnId}.
type Transaction struct {
	// TxnID names this transaction for the origin that sent it. It is not part of
	// the body: it travels in the path, and the pair (origin, txnID) is what a
	// receiver remembers an answer under.
	TxnID string
	// Origin is the server name the transaction claims to come from. It has to agree
	// with the signature, which is the only reason to look at it at all.
	Origin string
	// OriginServerTS is when the sender says it started this transaction. It is
	// informational; a sender with a broken clock is a nuisance, not a threat, once
	// the events inside carry their own signed timestamps.
	OriginServerTS int64
	PDUs           []matrix.Object
	EDUs           []matrix.Object
}

// ParseTransaction reads the body of a send request. The checks here are the ones that
// apply to the envelope rather than to any one event inside it: shape, counts, and the
// size of each PDU. An event whose *content* is wrong is not the envelope's business -
// it gets answered per event, so one bad event does not cost the sender the nineteen
// good ones beside it.
func ParseTransaction(txnID string, body matrix.Object) (*Transaction, error) {
	if txnID == "" {
		return nil, matrix.ErrInvalidParam("the transaction id is missing from the path")
	}
	if len(txnID) > MaxTxnIDLength {
		return nil, matrix.ErrInvalidParam("the transaction id is longer than %d characters", MaxTxnIDLength)
	}
	origin, ok := body["origin"].(string)
	if !ok || origin == "" {
		return nil, matrix.ErrBadJSON(fmt.Errorf("the transaction carries no origin server name"))
	}
	ts, err := exactInt(body["origin_server_ts"])
	if err != nil {
		return nil, matrix.ErrBadJSON(fmt.Errorf("the transaction's origin_server_ts %v", err))
	}
	pdus, err := objectList(body["pdus"], "pdus", true)
	if err != nil {
		return nil, err
	}
	edus, err := objectList(body["edus"], "edus", false)
	if err != nil {
		return nil, err
	}
	if len(pdus) > MaxPDUsPerTransaction {
		return nil, matrix.ErrInvalidParam("a transaction carries at most %d PDUs, this one carries %d",
			MaxPDUsPerTransaction, len(pdus))
	}
	if len(edus) > MaxEDUsPerTransaction {
		return nil, matrix.ErrInvalidParam("a transaction carries at most %d EDUs, this one carries %d",
			MaxEDUsPerTransaction, len(edus))
	}
	// The PDU count limit and the per-PDU size limit together bound what one request
	// can make a receiver do: about three megabytes of work, and no more.
	for i, pdu := range pdus {
		encoded, err := matrix.CanonicalJSON(pdu)
		if err != nil {
			return nil, matrix.ErrBadJSON(fmt.Errorf("the %dth PDU cannot be encoded: %v", i+1, err))
		}
		if len(encoded) > MaxPDUBytes {
			return nil, matrix.ErrInvalidParam("a PDU may be %d bytes, the %dth in this transaction is %d",
				MaxPDUBytes, i+1, len(encoded))
		}
	}
	return &Transaction{
		TxnID:          txnID,
		Origin:         origin,
		OriginServerTS: ts,
		PDUs:           pdus,
		EDUs:           edus,
	}, nil
}

// Body renders the transaction as it is sent over the wire.
func (t *Transaction) Body() matrix.Object {
	pdus := make(matrix.Array, 0, len(t.PDUs))
	for _, pdu := range t.PDUs {
		pdus = append(pdus, pdu)
	}
	edus := make(matrix.Array, 0, len(t.EDUs))
	for _, edu := range t.EDUs {
		edus = append(edus, edu)
	}
	body := matrix.Object{
		"origin":           t.Origin,
		"origin_server_ts": t.OriginServerTS,
		"pdus":             pdus,
	}
	// The specification allows edus to be omitted when there are none, and says
	// nothing about an empty array; leaving it out is the reading that cannot be
	// mistaken for "here is an ephemeral message with no type".
	if len(t.EDUs) > 0 {
		body["edus"] = edus
	}
	return body
}

// PDUResult reports what happened to one event of a transaction. The specification
// gives a single optional field: an error string means the event was refused, and its
// absence means the event was handled. There is deliberately no success flag, and no
// per-event error code.
type PDUResult struct {
	Error string `json:"error,omitempty"`
}

// PDUResults is the whole answer: one entry per event the transaction carried.
type PDUResults map[string]PDUResult

// Body renders the results in the shape the sender reads.
func (r PDUResults) Body() matrix.Object {
	pdus := matrix.Object{}
	for eventID, result := range r {
		entry := matrix.Object{}
		if result.Error != "" {
			entry["error"] = result.Error
		}
		pdus[eventID] = entry
	}
	return matrix.Object{"pdus": pdus}
}

// ParsePDUResults reads a peer's answer to a transaction we sent. An event with an
// error entry was refused by the receiver; an event with an empty entry was accepted.
func ParsePDUResults(body matrix.Object) (PDUResults, error) {
	pdus, ok := body["pdus"].(matrix.Object)
	if !ok {
		if asMap, mapOK := body["pdus"].(map[string]any); mapOK {
			pdus = asMap
		} else if body["pdus"] == nil {
			// A peer that answers with no per-event results accepted everything it
			// was sent; older servers do this.
			return PDUResults{}, nil
		} else {
			return nil, fmt.Errorf("the transaction response has no pdus object")
		}
	}
	results := make(PDUResults, len(pdus))
	for eventID, entryAny := range pdus {
		entry, ok := entryAny.(matrix.Object)
		if !ok {
			if asMap, mapOK := entryAny.(map[string]any); mapOK {
				entry = asMap
			} else {
				return nil, fmt.Errorf("the result of %s is not an object", eventID)
			}
		}
		message, _ := entry["error"].(string)
		results[eventID] = PDUResult{Error: message}
	}
	return results, nil
}

// SendTransaction delivers a transaction to a peer and returns the peer's per-event
// answers. The path carries the transaction id, so a retry of the same transaction is
// the same request: same path, same id, same bytes.
func (t *Transport) SendTransaction(ctx context.Context, destination string, txn *Transaction) (PDUResults, error) {
	if txn == nil || txn.TxnID == "" {
		return nil, fmt.Errorf("a transaction needs an id before it can be sent")
	}
	body, err := t.Put(ctx, destination, "/_matrix/federation/v1/send/"+txn.TxnID, txn.Body())
	if err != nil {
		return nil, err
	}
	return ParsePDUResults(body)
}

// Rejections lists what the peer said was wrong with the events it refused. The empty
// answer of a transaction that worked has no entries.
func (r PDUResults) Rejections() []string {
	if len(r) == 0 {
		return nil
	}
	out := make([]string, 0, len(r))
	for eventID, result := range r {
		if result.Error != "" {
			out = append(out, eventID+": "+result.Error)
		}
	}
	return out
}

// EDUType names the kind of an ephemeral message.
func EDUType(edu matrix.Object) string {
	value, _ := edu["edu_type"].(string)
	return value
}

// EDUContent returns the content of an EDU, or nil when it is missing or not an
// object. A sink treats nil as a malformed message rather than an empty one: the
// difference is between "this peer sent nonsense" and "this peer sent nothing".
func EDUContent(edu matrix.Object) matrix.Object {
	content, ok := edu["content"].(matrix.Object)
	if ok {
		return content
	}
	if asMap, mapOK := edu["content"].(map[string]any); mapOK {
		return asMap
	}
	return nil
}

// EventField reads a string field of an event object. It exists because a PDU is
// handled as an object, not as a struct: an event keeps its exact form, and a Go
// struct would decide which fields matter.
func EventField(event matrix.Object, field string) string {
	value, _ := event[field].(string)
	return value
}

// NewTxnID makes a transaction id for this server. Transaction ids only have to be
// unique per origin server, so the id carries a timestamp and enough randomness that
// two servers, or two processes on one server, cannot collide by accident: a collision
// would make a receiver answer a transaction it never saw and drop the one it did.
func NewTxnID(now time.Time) string {
	var random [9]byte
	if _, err := rand.Read(random[:]); err != nil {
		// A machine that cannot produce nine random bytes should not be signing
		// federation traffic either, but a transaction id is no place to fail.
		return fmt.Sprintf("l-%d", now.UnixNano())
	}
	return fmt.Sprintf("l-%d-%s", now.UnixNano(), base64.RawURLEncoding.EncodeToString(random[:]))
}

// objectList reads an array of objects out of a body, accepting the JSON decoding this
// server uses as well as a plain map.
func objectList(value any, field string, required bool) ([]matrix.Object, error) {
	if value == nil {
		if required {
			return nil, matrix.ErrBadJSON(fmt.Errorf("the transaction carries no %s", field))
		}
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, matrix.ErrBadJSON(fmt.Errorf("%s is not an array", field))
	}
	out := make([]matrix.Object, 0, len(list))
	for i, entry := range list {
		object, ok := entry.(matrix.Object)
		if !ok {
			if asMap, mapOK := entry.(map[string]any); mapOK {
				object = asMap
			} else {
				return nil, matrix.ErrBadJSON(fmt.Errorf("the %dth entry of %s is not an object", i+1, field))
			}
		}
		out = append(out, object)
	}
	return out, nil
}

// exactInt reads an integer without letting it pass through a float, because
// origin_server_ts is compared against milliseconds and a value that arrives as
// 1.79e12 is a rounding error away from being wrong.
func exactInt(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("is not a whole number: %s", typed.String())
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("is not a number: %v", value)
	}
}

// ValidTxnID reports whether a peer's transaction id is usable as a key. It is checked
// rather than trusted because the id ends up in a database index and in log lines.
func ValidTxnID(txnID string) bool {
	if txnID == "" || len(txnID) > MaxTxnIDLength {
		return false
	}
	return !strings.ContainsAny(txnID, "\r\n\x00")
}
