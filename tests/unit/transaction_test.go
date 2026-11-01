package unit

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The transaction is the envelope federation puts around live activity, and these rules
// are the ones the envelope is judged by: shape, counts, size, and the id that makes a
// retry mean the same thing twice. They are checked here, offline, because a limit that
// is wrong in both directions is invisible until a real peer is either overwhelmed or
// refused.

func decode(t *testing.T, raw string) matrix.Object {
	t.Helper()
	value, err := matrix.DecodeCanonical([]byte(raw))
	if err != nil {
		t.Fatalf("decoding: %v (%s)", err, raw)
	}
	object, ok := value.(matrix.Object)
	if !ok {
		t.Fatalf("the body is not an object: %s", raw)
	}
	return object
}

func pduList(count int, seed string) matrix.Array {
	pdus := make(matrix.Array, 0, count)
	for i := 0; i < count; i++ {
		pdus = append(pdus, matrix.Object{
			"event_id":         seed + "-" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			"room_id":          "!room:example.org",
			"type":             "m.room.message",
			"sender":           "@someone:example.org",
			"origin_server_ts": json.Number("1700000000000"),
			"content":          matrix.Object{"body": "small"},
		})
	}
	return pdus
}

// TestTransactionLimits pins the envelope's own rules: what the specification counts as
// too big, and what it counts as the wrong shape.
func TestTransactionLimits(t *testing.T) {
	cases := []struct {
		name     string
		txnID    string
		body     matrix.Object
		wantCode matrix.ErrorCode
	}{
		{
			name:  "no transaction id",
			txnID: "",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"), "pdus": matrix.Array{},
			},
			wantCode: matrix.ErrCodeInvalidParam,
		},
		{
			name:  "transaction id too long",
			txnID: strings.Repeat("x", federation.MaxTxnIDLength+1),
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"), "pdus": matrix.Array{},
			},
			wantCode: matrix.ErrCodeInvalidParam,
		},
		{
			name:  "no origin",
			txnID: "1",
			body: matrix.Object{
				"origin_server_ts": json.Number("1"), "pdus": matrix.Array{},
			},
			wantCode: matrix.ErrCodeBadJSON,
		},
		{
			name:  "timestamp is not a number",
			txnID: "1",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": "yesterday", "pdus": matrix.Array{},
			},
			wantCode: matrix.ErrCodeBadJSON,
		},
		{
			name:  "no pdus at all",
			txnID: "1",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"),
			},
			wantCode: matrix.ErrCodeBadJSON,
		},
		{
			name:  "pdus is not an array",
			txnID: "1",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"), "pdus": matrix.Object{},
			},
			wantCode: matrix.ErrCodeBadJSON,
		},
		{
			name:  "too many pdus",
			txnID: "1",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"),
				"pdus": pduList(federation.MaxPDUsPerTransaction+1, "$one"),
			},
			wantCode: matrix.ErrCodeInvalidParam,
		},
		{
			name:  "too many edus",
			txnID: "1",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"), "pdus": matrix.Array{},
				"edus": func() matrix.Array {
					edus := make(matrix.Array, federation.MaxEDUsPerTransaction+1)
					for i := range edus {
						edus[i] = matrix.Object{"edu_type": "m.typing", "content": matrix.Object{}}
					}
					return edus
				}(),
			},
			wantCode: matrix.ErrCodeInvalidParam,
		},
		{
			name:  "an oversized pdu",
			txnID: "1",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"),
				"pdus": matrix.Array{matrix.Object{
					"event_id": "$big:example.org",
					"room_id":  "!room:example.org",
					"type":     "m.room.message",
					"sender":   "@someone:example.org",
					"content":  matrix.Object{"body": strings.Repeat("x", federation.MaxPDUBytes)},
				}},
			},
			wantCode: matrix.ErrCodeInvalidParam,
		},
		{
			name:  "a transaction at both limits is accepted",
			txnID: "1",
			body: matrix.Object{
				"origin": "example.org", "origin_server_ts": json.Number("1"),
				"pdus": pduList(federation.MaxPDUsPerTransaction, "$limit"),
				"edus": func() matrix.Array {
					edus := make(matrix.Array, federation.MaxEDUsPerTransaction)
					for i := range edus {
						edus[i] = matrix.Object{"edu_type": "m.typing", "content": matrix.Object{}}
					}
					return edus
				}(),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			txn, err := federation.ParseTransaction(tc.txnID, tc.body)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("a transaction at the limits was refused: %v", err)
				}
				if len(txn.PDUs) != federation.MaxPDUsPerTransaction || len(txn.EDUs) != federation.MaxEDUsPerTransaction {
					t.Errorf("the parsed transaction holds %d PDUs and %d EDUs", len(txn.PDUs), len(txn.EDUs))
				}
				return
			}
			if err == nil {
				t.Fatalf("a transaction that must be refused was accepted: %+v", txn)
			}
			failure, ok := err.(*matrix.Error)
			if !ok {
				t.Fatalf("the refusal is %T, want a matrix error", err)
			}
			if failure.Code != tc.wantCode {
				t.Errorf("refused with %s, want %s (%s)", failure.Code, tc.wantCode, failure.Message)
			}
		})
	}
}

// TestTransactionBodyShape checks what this server sends: the fields the specification
// requires, and no empty edus array where there is nothing ephemeral to send.
func TestTransactionBodyShape(t *testing.T) {
	txn := &federation.Transaction{
		TxnID:          "7",
		Origin:         "example.org",
		OriginServerTS: 1700000000000,
		PDUs:           []matrix.Object{{"event_id": "$a:example.org"}},
	}
	body := txn.Body()
	if body["origin"] != "example.org" {
		t.Errorf("the body names %v as its origin", body["origin"])
	}
	if _, present := body["edus"]; present {
		t.Error("the body carries an edus array while there are no EDUs to send")
	}
	txn.EDUs = []matrix.Object{{"edu_type": "m.typing", "content": matrix.Object{}}}
	withEDUs := txn.Body()
	edus, ok := withEDUs["edus"].(matrix.Array)
	if !ok || len(edus) != 1 {
		t.Errorf("the body carries %v as its edus", withEDUs["edus"])
	}
	// The timestamp has to survive the round trip exactly: it is milliseconds, and a
	// value that goes through a float is a value that can be wrong by a fraction.
	again, err := federation.ParseTransaction("7", withEDUs)
	if err != nil {
		t.Fatalf("this server's own transaction was refused: %v", err)
	}
	if again.OriginServerTS != 1700000000000 {
		t.Errorf("the timestamp came back as %d", again.OriginServerTS)
	}
}

// TestTransactionIDsDoNotCollide pins the two properties a transaction id needs: it is
// unique enough that two servers, or two restarts of one server, do not name different
// work the same thing, and it stays short and free of control characters.
func TestTransactionIDsDoNotCollide(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		id := federation.NewTxnID(now)
		if seen[id] {
			t.Fatalf("two transactions were given the id %s", id)
		}
		seen[id] = true
		if !federation.ValidTxnID(id) {
			t.Fatalf("the generated id %q is not accepted as valid", id)
		}
	}
	for _, bad := range []string{"", strings.Repeat("x", 300), "one\r\ntwo", "nul\x00"} {
		if federation.ValidTxnID(bad) {
			t.Errorf("%q was accepted as a transaction id", bad)
		}
	}
}

// TestPDUResultsRoundTrip checks the per-event answer both ways: what this server sends,
// and what it reads from a peer. An event with no error entry was handled; an event with
// one was refused, and the difference decides whether a peer keeps the event.
func TestPDUResultsRoundTrip(t *testing.T) {
	results := federation.PDUResults{
		"$good:example.org":   {},
		"$bad:example.org":    {Error: "you are not allowed to send here"},
		"$forked:example.org": {Error: "the event conflicts with the room"},
	}
	body := results.Body()
	pdus, ok := body["pdus"].(matrix.Object)
	if !ok || len(pdus) != 3 {
		t.Fatalf("the answer carries %v", body["pdus"])
	}
	if entry, ok := pdus["$good:example.org"].(matrix.Object); !ok || len(entry) != 0 {
		t.Errorf("the accepted event is answered with %v, which claims a problem", pdus["$good:example.org"])
	}
	again, err := federation.ParsePDUResults(decode(t, string(mustEncode(t, body))))
	if err != nil {
		t.Fatalf("reading the answer back: %v", err)
	}
	if len(again) != 3 {
		t.Fatalf("the answer names %d events, want 3", len(again))
	}
	if again["$good:example.org"].Error != "" {
		t.Error("an accepted event read back as refused")
	}
	if again["$bad:example.org"].Error == "" {
		t.Error("a refused event read back as accepted")
	}
	// A peer that answers with no per-event results at all accepted everything: the
	// absence of the field is not a protocol violation, and treating it as one would
	// break the oldest servers in the federation.
	quiet, err := federation.ParsePDUResults(matrix.Object{})
	if err != nil || len(quiet) != 0 {
		t.Errorf("a peer with no per-event results produced %v, %v", quiet, err)
	}
	broken, err := federation.ParsePDUResults(matrix.Object{"pdus": "not an object"})
	if err == nil {
		t.Errorf("a malformed result map was read as %v", broken)
	}
}

// TestEDUHelpers checks the small accessors every EDU sink is built on, including the
// one distinction that matters in a sink: a missing content object is a malformed
// message, not an empty one.
func TestEDUHelpers(t *testing.T) {
	edu := matrix.Object{"edu_type": "m.receipt", "content": matrix.Object{"!r:example.org": matrix.Object{}}}
	if federation.EDUType(edu) != "m.receipt" {
		t.Errorf("the EDU type reads as %q", federation.EDUType(edu))
	}
	if federation.EDUContent(edu) == nil {
		t.Error("the content of a well formed EDU reads as missing")
	}
	if federation.EDUType(matrix.Object{"content": matrix.Object{}}) != "" {
		t.Error("an EDU with no type reported a type")
	}
	for _, broken := range []matrix.Object{
		{"edu_type": "m.typing"},
		{"edu_type": "m.typing", "content": "a string"},
		{"edu_type": "m.typing", "content": json.Number("4")},
	} {
		if federation.EDUContent(broken) != nil {
			t.Errorf("%v produced content, which a sink would then read as an empty message", broken)
		}
	}
}

func mustEncode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	return string(encoded)
}
