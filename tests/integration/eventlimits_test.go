package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Two ceilings that are properties of the event or the request rather than of the door it came
// through, and both are worth pinning to a number rather than to a feeling: a server that guesses
// where the limit is will disagree with every other server about what a room may contain.

// TestAnEventOverTheSizeLimitIsRefused finds, by bisection, where this server stops accepting an
// event, and then asks whether that place is the Server-Server API's MAX_PDU_SIZE. Bisection rather
// than a hand-picked oversized body matters: a refusal that fires far too early looks exactly like a
// correct refusal unless something just under the limit is shown to get through.
func TestAnEventOverTheSizeLimitIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, alice, _ := h.register(t, "host", "pw-host", "")
	roomID := createRoom(t, h, alice, map[string]any{"name": "sized"})

	// acceptedBodyOf reports whether an m.room.message whose body is n bytes is accepted. Each
	// attempt uses its own transaction ID so that idempotency cannot answer for it.
	acceptedBodyOf := func(n int) (int, string) {
		body := strings.Repeat("x", n)
		res := h.put(t, "/_matrix/client/v3/rooms/"+roomID+"/send/m.room.message/size-"+fmt.Sprint(n),
			map[string]any{"msgtype": "m.text", "body": body}, alice)
		return res.status, res.errcode(t)
	}

	// A small event and an enormous one bracket the search. If either endpoint behaves unexpectedly
	// the ceiling this server holds is not a size ceiling at all, which is said rather than bisected
	// past.
	if status, code := acceptedBodyOf(64); status != http.StatusOK {
		t.Fatalf("a 64 byte message body was refused with %d %s; the server is not measuring size", status, code)
	}
	status, code := acceptedBodyOf(200000)
	if status != http.StatusRequestEntityTooLarge || code != "M_TOO_LARGE" {
		t.Fatalf("a 200 KB message body answered %d %s, want 413 M_TOO_LARGE", status, code)
	}

	// The largest body still accepted. The event around it — type, room ID, sender, depth,
	// references, signatures and hashes — costs a few hundred bytes, so the body ceiling sits just
	// under the 65,536-byte event ceiling.
	lo, hi := 64, 200000 // lo accepted, hi refused
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if status, _ := acceptedBodyOf(mid); status == http.StatusOK {
			lo = mid
		} else {
			hi = mid
		}
	}
	if lo < 64000 || lo > 65536 {
		t.Errorf("the largest accepted body is %d bytes, which does not sit inside the 65,536-byte "+
			"MAX_PDU_SIZE it should be just under", lo)
	}
	if status, code := acceptedBodyOf(hi); status != http.StatusRequestEntityTooLarge || code != "M_TOO_LARGE" {
		t.Errorf("the first refused body (%d bytes) answered %d %s, want 413 M_TOO_LARGE", hi, status, code)
	}

	// The same rule on the state door, which stores through the same funnel.
	oversize := strings.Repeat("x", 200000)
	res := h.raw(t, http.MethodPut, "/_matrix/client/v3/rooms/"+roomID+"/state/big.state/",
		[]byte(`{"body":"`+oversize+`"}`), alice)
	if res.status != http.StatusRequestEntityTooLarge {
		t.Errorf("sending an oversized state event: status %d %s, want 413 M_TOO_LARGE",
			res.status, res.errcode(t))
	}

	// A refused event is not a happened event: everything the room holds is inside the ceiling.
	room := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/messages?dir=b&limit=100", alice),
		http.StatusOK).decoded(t)
	chunk, _ := room["chunk"].([]any)
	if len(chunk) == 0 {
		t.Fatalf("the room holds no events at all, so the timeline check proves nothing")
	}
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		body, _ := content["body"].(string)
		if len(body) > lo {
			t.Errorf("the room's timeline holds an event with a %d byte body, over the ceiling of %d "+
				"this server just refused", len(body), lo)
		}
	}
}

// TestABodyThatIsNotUTF8IsNotJSON covers the other ceiling: what counts as JSON at all. Go's decoder
// replaces an invalid byte with U+FFFD and carries on, so without this a stray byte becomes part of
// a stored name, index and history — with a 200 for the sender's trouble.
func TestABodyThatIsNotUTF8IsNotJSON(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// The byte inside the string is the case the rule is about: the text claims to be JSON, and the
	// decoder would quietly turn the invalid byte into U+FFFD and register the account with a
	// password nobody typed. The trailing byte covers the same text read to its end.
	within := append([]byte(`{"username":"nope","password":"a`), 0x81)
	within = append(within, []byte(`b","auth":{"type":"m.login.dummy"}}`)...)
	trailing := append([]byte(`{"username":"nope","password":"pw","auth":{"type":"m.login.dummy"}}`), 0x81)
	for _, tc := range []struct {
		where string
		body  []byte
	}{
		{"inside a JSON string", within},
		{"after the JSON object", trailing},
	} {
		res := h.raw(t, http.MethodPost, "/_matrix/client/v3/register", tc.body, "")
		if res.status != http.StatusBadRequest {
			t.Fatalf("register with non-UTF-8 %s: status %d, want 400 (body %s)",
				tc.where, res.status, res.body)
		}
		var answer struct {
			ErrCode string `json:"errcode"`
		}
		if err := json.Unmarshal(res.body, &answer); err != nil {
			t.Fatalf("the error answer is unreadable: %s", res.body)
		}
		if answer.ErrCode != "M_NOT_JSON" {
			t.Errorf("register with non-UTF-8 %s answered %s, want M_NOT_JSON (body %s)",
				tc.where, answer.ErrCode, res.body)
		}
	}

	// Valid UTF-8 of any width is ordinary JSON: this rule is not an argument against non-ASCII.
	userID, token, _ := h.register(t, "unicode", "pw-unicode", "")
	res := h.put(t, "/_matrix/client/v3/profile/"+userID+"/displayname",
		map[string]any{"displayname": "Ünïcödé — 名前"}, token)
	if res.status != http.StatusOK {
		t.Fatalf("setting a non-ASCII display name: status %d (body %s)", res.status, res.body)
	}
	read := h.mustStatus(t, h.get("/_matrix/client/v3/profile/"+userID+"/displayname", ""),
		http.StatusOK).decoded(t)
	if name, _ := read["displayname"].(string); name != "Ünïcödé — 名前" {
		t.Errorf("the display name reads back %q, want it unchanged", name)
	}
}
