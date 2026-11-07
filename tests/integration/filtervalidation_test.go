package integration

import (
	"net/http"
	"net/url"
	"testing"
)

// TestAFullOfTyposFilterIsRefused stores filters the way a client with a typo in it would write
// them. Every one of these used to be answered 200: the filter was kept, and every later /sync
// through it returned fewer events than the client meant to ask for, with nothing to show what had
// been thrown away.
func TestAFullOfTyposFilterIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	alice, token, _ := h.register(t, "filtermaker", "pw-filter", "")

	for _, bad := range []struct {
		what string
		body string
	}{
		{"presence is not an object", `{"presence":"not_an_object"}`},
		{"room is not an object", `{"room":"not_an_object"}`},
		{"timeline is not an object", `{"room":{"timeline":"not_an_object"}}`},
		{"state is not an object", `{"room":{"state":"not_an_object"}}`},
		{"ephemeral is not an object", `{"room":{"ephemeral":"not_an_object"}}`},
		{"account_data is not an object", `{"room":{"account_data":"not_an_object"}}`},
		{"rooms is not a list", `{"room":{"timeline":{"rooms":"not_a_list"}}}`},
		{"not_rooms is not a list", `{"room":{"timeline":{"not_rooms":"not_a_list"}}}`},
		{"senders is not a list", `{"room":{"timeline":{"senders":"not_a_list"}}}`},
		{"not_senders is not a list", `{"room":{"timeline":{"not_senders":5}}}`},
		{"types is not a list", `{"room":{"timeline":{"types":"not_a_list"}}}`},
		{"not_types is not a list", `{"room":{"timeline":{"not_types":{}}}}`},
		{"types holds numbers", `{"room":{"timeline":{"types":[1]}}}`},
		{"rooms holds a string that is not a room ID", `{"room":{"timeline":{"rooms":["not_a_room_id"]}}}`},
		{"senders holds a string that is not a user ID", `{"room":{"timeline":{"senders":["not_a_sender_id"]}}}`},
		{"limit is not a number", `{"room":{"timeline":{"limit":"10"}}}`},
		{"lazy_load_members is not a boolean", `{"room":{"state":{"lazy_load_members":"true"}}}`},
		{"lazy_load_options is not an object", `{"room":{"state":{"lazy_load_options":true}}}`},
		{"include_leave is not a boolean", `{"room":{"include_leave":"yes"}}`},
		{"event_format is neither of its two values", `{"event_format":"pretty"}`},
		{"the document is not an object at all", `["room","timeline"]`},
	} {
		res := h.raw(t, http.MethodPost, "/_matrix/client/v3/user/"+alice+"/filter",
			[]byte(bad.body), token)
		if res.status < 400 || res.status >= 500 {
			t.Errorf("storing a filter where %s: status %d, want 4XX (body %s)",
				bad.what, res.status, res.body)
		}
		if code := res.errcode(t); code != "M_INVALID_PARAM" {
			t.Errorf("a filter where %s answered %s, want M_INVALID_PARAM (body %s)",
				bad.what, code, res.body)
		}
	}

	// A filter that says what it means is stored and read back, including namespaced names this
	// server has not read: an unknown field is left alone, so this server is never the reason a
	// client cannot try a newer spec.
	body := `{"event_format":"client","org.matrix.msc3301.threaded":true,` +
		`"room":{"timeline":{"limit":5,"rooms":["!arbitrary:hs1"],"senders":["` + alice + `"]},` +
		`"state":{"lazy_load_members":true,"lazy_load_options":{"throwaway":true}}}}`
	created := h.mustStatus(t, h.raw(t, http.MethodPost,
		"/_matrix/client/v3/user/"+alice+"/filter", []byte(body), token), http.StatusOK)
	filterID, _ := created.decoded(t)["filter_id"].(string)
	if filterID == "" {
		t.Fatalf("the filter was accepted without an ID: %s", created.body)
	}
	read := h.mustStatus(t, h.get("/_matrix/client/v3/user/"+alice+"/filter/"+filterID, token),
		http.StatusOK)
	if string(read.body) != body {
		t.Errorf("the filter reads back as %s, want exactly what was sent", read.body)
	}
}

// TestAnInlineSyncFilterIsCheckedToo covers the other door a filter arrives at: given inline to
// /sync. A client that is told its filter is wrong once and not again has been trained to distrust
// the answer it did get.
func TestAnInlineSyncFilterIsCheckedToo(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "syncer", "pw-syncer", "")

	// The filter is a query parameter, so it goes through the encoder a client would use.
	bad := h.get("/_matrix/client/v3/sync?filter="+
		url.QueryEscape(`{"room":{"timeline":{"senders":["not_a_sender_id"]}}}`), token)
	if bad.status < 400 || bad.status >= 500 {
		t.Fatalf("a sync with an inline filter holding a non-user ID: status %d, want 4XX (body %s)",
			bad.status, bad.body)
	}
	if code := bad.errcode(t); code != "M_INVALID_PARAM" {
		t.Errorf("answer was %s, want M_INVALID_PARAM (body %s)", code, bad.body)
	}

	// The same filter spelled properly syncs.
	good := h.get("/_matrix/client/v3/sync?filter="+
		url.QueryEscape(`{"room":{"timeline":{"limit":0}}}`), token)
	if good.status != http.StatusOK {
		t.Fatalf("a sync with a well-formed inline filter: status %d (body %s)", good.status, good.body)
	}
}
