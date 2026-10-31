package compat

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

// TestKeyBackup covers the server-side room key backup: the version documents,
// storing sessions at each of the three granularities, reading them back
// unchanged, and the errors a client relies on. Lattice never sees the encryption
// keys, so what a client stores is exactly what it gets back.
func TestKeyBackup(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	// An account that never set up a backup is answered with a Matrix 404, not
	// with an empty document: a client decides whether to offer to create one.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/room_keys/version", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Fatalf("backup version before any backup: got %d %s, want 404", resp.Status, resp.Raw)
	}

	first := server.MustDo(http.MethodPost, "/_matrix/client/v3/room_keys/version", alice.Token,
		map[string]any{
			"algorithm": "m.megolm_backup.v1.curve25519-aes-sha2",
			"auth_data": map[string]any{"public_key": "aW52YWxpZCBwdWJsaWMga2V5", "signatures": map[string]any{}},
		}, http.StatusOK)
	if version, _ := first.Body["version"].(string); version != "1" {
		t.Fatalf("first backup version = %v (%s), want \"1\"", first.Body["version"], first.Raw)
	}
	// A second setup counts up rather than replacing the first, which is how a
	// client can restore an old backup while building a new one.
	second := server.MustDo(http.MethodPost, "/_matrix/client/v3/room_keys/version", alice.Token,
		map[string]any{
			"algorithm": "m.megolm_backup.v1.curve25519-aes-sha2",
			"auth_data": map[string]any{"public_key": "bmV3ZXIga2V5", "signatures": map[string]any{}},
		}, http.StatusOK)
	if version, _ := second.Body["version"].(string); version != "2" {
		t.Errorf("second backup version = %v (%s), want \"2\"", second.Body["version"], second.Raw)
	}

	latest := server.MustDo(http.MethodGet, "/_matrix/client/v3/room_keys/version", alice.Token, nil, http.StatusOK)
	if version, _ := latest.Body["version"].(string); version != "2" {
		t.Errorf("the latest backup version = %v, want \"2\" (%s)", latest.Body["version"], latest.Raw)
	}
	if latest.String("algorithm") != "m.megolm_backup.v1.curve25519-aes-sha2" {
		t.Errorf("backup algorithm = %q", latest.String("algorithm"))
	}
	authData, _ := latest.Body["auth_data"].(map[string]any)
	if authData["public_key"] != "bmV3ZXIga2V5" {
		t.Errorf("backup auth_data = %v", latest.Body["auth_data"])
	}
	if count, _ := latest.Body["count"].(float64); count != 0 {
		t.Errorf("an empty backup reports %v keys", latest.Body["count"])
	}

	// Writing into a superseded backup would store keys nobody will read.
	stale := server.Do(http.MethodPut, "/_matrix/client/v3/room_keys/keys?version=1", alice.Token,
		map[string]any{"rooms": map[string]any{}})
	if stale.Status != http.StatusBadRequest || stale.ErrCode() != "M_WRONG_KEY_VERSION" {
		t.Errorf("writing to a superseded backup: got %d %s", stale.Status, stale.Raw)
	}

	// The whole-account form stores sessions of several rooms at once. The
	// session data is an opaque blob, so it comes back byte for byte.
	rooms := map[string]any{
		"!first:test.server": map[string]any{
			"first_known_index": 3,
			"sessions": map[string]any{
				"session-one": map[string]any{
					"first_known_index": 3,
					"session_data":      map[string]any{"ciphertext": "AQID", "mac": "aWFtcm90YWV4dHJhc2VjcmV0"},
				},
				"session-two": map[string]any{
					"first_known_index": 9,
					"session_data":      map[string]any{"ciphertext": "BAsLDw"},
				},
			},
		},
		"!second:test.server": map[string]any{
			"first_known_index": 0,
			"sessions": map[string]any{
				"session-three": map[string]any{"session_data": map[string]any{"ciphertext": "CQoLDA"}},
			},
		},
	}
	stored := server.MustDo(http.MethodPut, "/_matrix/client/v3/room_keys/keys?version=2", alice.Token,
		map[string]any{"rooms": rooms}, http.StatusOK)
	if count, _ := stored.Body["key_count"].(float64); count != 3 {
		t.Fatalf("storing the sessions reported %v keys (%s)", stored.Body["key_count"], stored.Raw)
	}

	all := server.MustDo(http.MethodGet, "/_matrix/client/v3/room_keys/keys?version=2", alice.Token, nil, http.StatusOK)
	body, _ := all.Body["rooms"].(map[string]any)
	if len(body) != 2 {
		t.Fatalf("the backup holds %d rooms (%s), want 2", len(body), all.Raw)
	}
	firstRoom, _ := body["!first:test.server"].(map[string]any)
	if index, _ := firstRoom["first_known_index"].(float64); index != 3 {
		t.Errorf("the first room reports first_known_index %v, want 3", firstRoom["first_known_index"])
	}
	sessions, _ := firstRoom["sessions"].(map[string]any)
	if len(sessions) != 2 {
		t.Errorf("the first room holds %d sessions, want 2", len(sessions))
	}
	sessionOne, _ := sessions["session-one"].(map[string]any)
	data, _ := sessionOne["session_data"].(map[string]any)
	if data["ciphertext"] != "AQID" || data["mac"] != "aWFtcm90YWV4dHJhc2VjcmV0" {
		t.Errorf("the stored session came back changed: %v", sessionOne)
	}
	if index, _ := sessionOne["first_known_index"].(float64); index != 3 {
		t.Errorf("session-one first_known_index = %v, want 3", sessionOne["first_known_index"])
	}

	// A room filter narrows the read without a round trip per room.
	filtered := server.MustDo(http.MethodGet, "/_matrix/client/v3/room_keys/keys?version=2&rooms=%21second%3Atest.server",
		alice.Token, nil, http.StatusOK)
	filteredRooms, _ := filtered.Body["rooms"].(map[string]any)
	if _, ok := filteredRooms["!first:test.server"]; ok {
		t.Errorf("the rooms filter was ignored: %s", filtered.Raw)
	}
	if _, ok := filteredRooms["!second:test.server"]; !ok {
		t.Errorf("the rooms filter dropped the room it asked for: %s", filtered.Raw)
	}

	// The room form and the single session form.
	room := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!first:test.server")+"?version=2",
		alice.Token, nil, http.StatusOK)
	roomSessions, _ := room.Body["sessions"].(map[string]any)
	if len(roomSessions) != 2 {
		t.Errorf("the room form returned %d sessions (%s)", len(roomSessions), room.Raw)
	}
	missing := server.Do(http.MethodGet,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!nosuchroom:test.server")+"?version=2",
		alice.Token, nil)
	if missing.Status != http.StatusNotFound {
		t.Errorf("an unknown room in a backup: got %d %s, want 404", missing.Status, missing.Raw)
	}

	single := server.MustDo(http.MethodPut,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!first:test.server")+"/session-four?version=2",
		alice.Token, map[string]any{"first_known_index": 42, "session_data": map[string]any{"ciphertext": "HA8P"}},
		http.StatusOK)
	if len(single.Body) != 0 {
		t.Errorf("storing one session returned a body: %s", single.Raw)
	}
	fetched := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!first:test.server")+"/session-four?version=2",
		alice.Token, nil, http.StatusOK)
	if index, _ := fetched.Body["first_known_index"].(float64); index != 42 {
		t.Errorf("the single session reports first_known_index %v, want 42 (%s)", fetched.Body["first_known_index"], fetched.Raw)
	}
	sessionData, _ := fetched.Body["session_data"].(map[string]any)
	if sessionData["ciphertext"] != "HA8P" {
		t.Errorf("the single session came back as %v", fetched.Body["session_data"])
	}
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!first:test.server")+"/nosuchsession?version=2",
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("an unknown session: got %d %s, want 404", resp.Status, resp.Raw)
	}

	// Deleting one session leaves the rest of the backup alone.
	server.MustDo(http.MethodDelete,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!first:test.server")+"/session-four?version=2",
		alice.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!first:test.server")+"/session-four?version=2",
		alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("the deleted session is still there: %d %s", resp.Status, resp.Raw)
	}
	after := server.MustDo(http.MethodGet, "/_matrix/client/v3/room_keys/keys?version=2", alice.Token, nil, http.StatusOK)
	afterRooms, _ := after.Body["rooms"].(map[string]any)
	afterFirst, _ := afterRooms["!first:test.server"].(map[string]any)
	afterSessions, _ := afterFirst["sessions"].(map[string]any)
	if len(afterSessions) != 2 {
		t.Errorf("deleting one session left %d sessions in the room, want 2 (%s)", len(afterSessions), after.Raw)
	}

	// The deprecated version-scoped forms still work: updating the auth data of a
	// version keeps its sessions, and deleting a version takes them with it.
	server.MustDo(http.MethodPut, "/_matrix/client/v3/room_keys/version/2", alice.Token,
		map[string]any{
			"algorithm": "m.megolm_backup.v1.curve25519-aes-sha2",
			"auth_data": map[string]any{"public_key": "cmVwbGFjZWQga2V5"},
		}, http.StatusOK)
	updated := server.MustDo(http.MethodGet, "/_matrix/client/v3/room_keys/version/2", alice.Token, nil, http.StatusOK)
	updatedAuth, _ := updated.Body["auth_data"].(map[string]any)
	if updatedAuth["public_key"] != "cmVwbGFjZWQga2V5" {
		t.Errorf("the auth data after an update = %v", updated.Body["auth_data"])
	}
	if count, _ := updated.Body["count"].(float64); count != 3 {
		t.Errorf("the updated backup reports %v keys, want 3", updated.Body["count"])
	}

	server.MustDo(http.MethodDelete, "/_matrix/client/v3/room_keys/version/1", alice.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/room_keys/version/1", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("the deleted version is still readable: %d %s", resp.Status, resp.Raw)
	}
	server.MustDo(http.MethodDelete, "/_matrix/client/v3/room_keys/version/2", alice.Token, nil, http.StatusOK)
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/room_keys/keys?version=2", alice.Token, nil); resp.Status != http.StatusNotFound {
		t.Errorf("keys of a deleted version are still readable: %d %s", resp.Status, resp.Raw)
	}

	// A version that is not a number is a client error, and no endpoint of the
	// group works without a token.
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/room_keys/keys?version=lots", alice.Token, nil); resp.Status != http.StatusBadRequest {
		t.Errorf("a non-numeric version: got %d %s, want 400", resp.Status, resp.Raw)
	}
	if resp := server.Do(http.MethodGet, "/_matrix/client/v3/room_keys/version", "", nil); resp.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated backup version: got %d, want 401", resp.Status)
	}
}

// TestKeyBackupSessionsAreOpaque checks that a session blob survives untouched,
// which is the one promise a key backup makes: the server cannot read it and must
// not change it.
func TestKeyBackupSessionsAreOpaque(t *testing.T) {
	server := NewServer(t)
	alice := server.Register("alice", "wonderland")

	server.MustDo(http.MethodPost, "/_matrix/client/v3/room_keys/version", alice.Token,
		map[string]any{"algorithm": "m.megolm_backup.v1.curve25519-aes-sha2", "auth_data": map[string]any{}},
		http.StatusOK)

	// Odd keys, unusual field order, a nested object and a string that looks like
	// JSON: none of it is interpreted.
	const blob = `{"zz":1,"aa":"quote\"inside","nested":{"list":[1,2,3]},"weird key":"\u00e9"}`
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		t.Fatalf("the test blob is not valid JSON: %v", err)
	}
	server.MustDo(http.MethodPut,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!opaque:test.server")+"/s1?version=1",
		alice.Token, map[string]any{"first_known_index": 7, "session_data": raw}, http.StatusOK)

	got := server.MustDo(http.MethodGet,
		"/_matrix/client/v3/room_keys/keys/"+url.QueryEscape("!opaque:test.server")+"/s1?version=1",
		alice.Token, nil, http.StatusOK)
	data, _ := got.Body["session_data"].(map[string]any)
	if data["weird key"] != "é" {
		t.Errorf("the stored blob lost a key: %s", got.Raw)
	}
	nested, _ := data["nested"].(map[string]any)
	list, _ := nested["list"].([]any)
	if len(list) != 3 {
		t.Errorf("the stored blob lost its list: %s", got.Raw)
	}
	if _, ok := data["zz"]; !ok {
		t.Errorf("the stored blob lost a field the server does not know: %s", got.Raw)
	}
}
