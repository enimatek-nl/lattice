package integration

import (
	"net/http"
	"testing"
)

// escAlias percent-encodes the leading sigil of a room alias. A bare "#" in a URL is a
// fragment, so it never reaches the server; clients send "%23", and a test that does not
// would be testing its own URL handling instead of the endpoint.
func escAlias(alias string) string {
	if len(alias) > 0 && alias[0] == '#' {
		return "%23" + alias[1:]
	}
	return alias
}

// TestRoomAliasLifecycle covers creating an alias for a room, resolving it, listing it,
// and deleting it, including who is allowed to delete.
func TestRoomAliasLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "nora", "pw-nora", "")
	_, bobToken, _ := h.register(t, "otto", "pw-otto", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	// A joined member with no power level may add an alias.
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	alias := "#lounge:hs1"
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(alias),
		[]byte(`{"room_id":"`+roomID+`"}`), bobToken), http.StatusOK)

	resolved := h.mustStatus(t, h.get("/_matrix/client/v3/directory/room/"+escAlias(alias), ""), http.StatusOK).decoded(t)
	if got, _ := resolved["room_id"].(string); got != roomID {
		t.Errorf("alias resolved to %q, want %q", got, roomID)
	}

	listed := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/aliases", aliceToken), http.StatusOK).decoded(t)
	if aliases, ok := listed["aliases"].([]any); !ok || len(aliases) != 1 || aliases[0] != alias {
		t.Errorf("aliases = %v, want [%s]", listed["aliases"], alias)
	}

	// Someone outside the room may not see the alias list: it reveals that a room
	// exists.
	_, outsider, _ := h.register(t, "quinn", "pw-quinn", "")
	if res := h.get("/_matrix/client/v3/rooms/"+roomID+"/aliases", outsider); res.status != http.StatusForbidden {
		t.Errorf("a non-member listing aliases: status %d, want 403 (body %s)", res.status, res.body)
	}

	// The alias creator may delete their own alias even with no power in the room.
	h.mustStatus(t, h.raw(t, http.MethodDelete, "/_matrix/client/v3/directory/room/"+escAlias(alias), nil, bobToken), http.StatusOK)
	if res := h.get("/_matrix/client/v3/directory/room/"+escAlias(alias), ""); res.status != http.StatusNotFound {
		t.Errorf("resolving a deleted alias: status %d, want 404 (body %s)", res.status, res.body)
	}

	// A user who did not create the alias needs the room's kick level to delete it.
	other := "#other:hs1"
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(other),
		[]byte(`{"room_id":"`+roomID+`"}`), aliceToken), http.StatusOK)
	res := h.raw(t, http.MethodDelete, "/_matrix/client/v3/directory/room/"+escAlias(other), nil, bobToken)
	if res.status != http.StatusForbidden {
		t.Fatalf("deleting someone else's alias without power: status %d, want 403 (body %s)", res.status, res.body)
	}
	if code := res.errcode(t); code != "M_FORBIDDEN" {
		t.Errorf("errcode = %q, want M_FORBIDDEN", code)
	}
	h.mustStatus(t, putState(t, h, roomID, "m.room.power_levels", "",
		`{"users":{"@nora:hs1":100,"@otto:hs1":100}}`, aliceToken), http.StatusOK)
	h.mustStatus(t, h.raw(t, http.MethodDelete, "/_matrix/client/v3/directory/room/"+escAlias(other), nil, bobToken), http.StatusOK)

	if res := h.raw(t, http.MethodDelete, "/_matrix/client/v3/directory/room/%23never:hs1", nil, aliceToken); res.status != http.StatusNotFound {
		t.Errorf("deleting an unknown alias: status %d, want 404 (body %s)", res.status, res.body)
	}
}

// TestCreateRoomWithAliasAndJoinByAlias checks that a room created with the alias the
// client asked for can be found and joined by that name.
func TestCreateRoomWithAliasAndJoinByAlias(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "rita", "pw-rita", "")
	_, bobToken, _ := h.register(t, "sue", "pw-sue", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{
		"preset":          "public_chat",
		"room_alias_name": "meeting",
	})
	alias := "#meeting:hs1"
	resolved := h.mustStatus(t, h.get("/_matrix/client/v3/directory/room/"+escAlias(alias), ""), http.StatusOK).decoded(t)
	if got, _ := resolved["room_id"].(string); got != roomID {
		t.Fatalf("the alias resolves to %q, want the created room %q", got, roomID)
	}

	res := h.post(t, "/_matrix/client/v3/join/"+escAlias(alias), map[string]any{}, bobToken)
	h.mustStatus(t, res, http.StatusOK)
	if got, _ := res.decoded(t)["room_id"].(string); got != roomID {
		t.Errorf("joining by alias returned room_id %q, want %q", got, roomID)
	}

	// The same alias cannot be moved under someone's feet.
	second := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	res = h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(alias),
		[]byte(`{"room_id":"`+second+`"}`), aliceToken)
	if res.status != http.StatusConflict {
		t.Errorf("re-pointing an existing alias: status %d, want 409 (body %s)", res.status, res.body)
	}
	// Asking for the same mapping again is not a conflict.
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(alias),
		[]byte(`{"room_id":"`+roomID+`"}`), aliceToken), http.StatusOK)

	// A duplicate alias at creation time is refused rather than silently dropped.
	if res := h.post(t, "/_matrix/client/v3/createRoom",
		map[string]any{"room_alias_name": "meeting"}, aliceToken); res.status != http.StatusConflict {
		t.Errorf("creating a room with a taken alias: status %d, want 409 (body %s)", res.status, res.body)
	}
}

// TestCanonicalAliasValidation checks that a room cannot advertise an alias this server
// cannot back up.
func TestCanonicalAliasValidation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "tina", "pw-tina", "")
	roomA := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	roomB := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	aliasA := "#room-a:hs1"
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(aliasA),
		[]byte(`{"room_id":"`+roomA+`"}`), aliceToken), http.StatusOK)

	// A well formed alias this server does not have.
	res := putState(t, h, roomA, "m.room.canonical_alias", "", `{"alias":"#missing:hs1"}`, aliceToken)
	if res.status != http.StatusBadRequest || res.errcode(t) != "M_BAD_ALIAS" {
		t.Fatalf("canonical alias that does not exist: status %d errcode %q", res.status, res.errcode(t))
	}
	// An alias that exists but belongs to another room.
	res = putState(t, h, roomB, "m.room.canonical_alias", "", `{"alias":"`+aliasA+`"}`, aliceToken)
	if res.status != http.StatusBadRequest || res.errcode(t) != "M_BAD_ALIAS" {
		t.Fatalf("canonical alias pointing at another room: status %d errcode %q", res.status, res.errcode(t))
	}
	// Malformed syntax is the client's error.
	res = putState(t, h, roomA, "m.room.canonical_alias", "", `{"alias":"%bad:hs1"}`, aliceToken)
	if res.status != http.StatusBadRequest || res.errcode(t) != "M_INVALID_PARAM" {
		t.Fatalf("malformed canonical alias: status %d errcode %q", res.status, res.errcode(t))
	}
	// An alt_alias that is not the same room is refused too.
	res = putState(t, h, roomB, "m.room.canonical_alias", "",
		`{"alias":"#room-a:hs1","alt_aliases":["#room-a:hs1"]}`, aliceToken)
	if res.status != http.StatusBadRequest {
		t.Errorf("canonical alias of another room via alt_aliases: status %d, want 400", res.status)
	}

	// The real mapping is accepted.
	h.mustStatus(t, putState(t, h, roomA, "m.room.canonical_alias", "",
		`{"alias":"#room-a:hs1","alt_aliases":[]}`, aliceToken), http.StatusOK)
}

// TestUnicodeRoomAlias keeps identifier handling honest: room alias localparts are
// opaque Unicode, not ASCII-only slugs.
func TestUnicodeRoomAlias(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "uma", "pw-uma", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	alias := "#老虎£яඞ:hs1"
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(alias),
		[]byte(`{"room_id":"`+roomID+`"}`), aliceToken), http.StatusOK)
	resolved := h.mustStatus(t, h.get("/_matrix/client/v3/directory/room/"+escAlias(alias), ""), http.StatusOK).decoded(t)
	if got, _ := resolved["room_id"].(string); got != roomID {
		t.Errorf("unicode alias resolved to %q, want %q", got, roomID)
	}
}

// TestRoomReadsNeedMembership pins that leaving a room takes away the ability to read
// it: the state, the member list and the joined-member view all answer 403 afterwards.
// This is the difference between a room and a transcript anyone with an account can
// scroll through.
func TestRoomReadsNeedMembership(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "will", "pw-will", "")
	_, bobToken, _ := h.register(t, "xena2", "pw-xena2", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/joined_members", bobToken), http.StatusOK)

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)
	for _, path := range []string{
		"/_matrix/client/v3/rooms/" + roomID + "/joined_members",
		"/_matrix/client/v3/rooms/" + roomID + "/members",
		"/_matrix/client/v3/rooms/" + roomID + "/state",
		"/_matrix/client/v3/rooms/" + roomID + "/state/m.room.join_rules/",
		"/_matrix/client/v3/rooms/" + roomID + "/aliases",
	} {
		res := h.get(path, bobToken)
		if res.status != http.StatusForbidden {
			t.Errorf("%s after leaving: status %d, want 403 (body %s)", path, res.status, res.body)
			continue
		}
		if code := res.errcode(t); code != "M_FORBIDDEN" {
			t.Errorf("%s: errcode %q, want M_FORBIDDEN", path, code)
		}
	}
	// The member who stayed can still read everything.
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/members", aliceToken), http.StatusOK)
}

// TestJoinSpellingAndStateFormat covers two shapes clients use that a first
// implementation tends to miss: the /rooms/{roomID}/join spelling of joining, and
// `format=event` on a state read.
func TestJoinSpellingAndStateFormat(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "zack", "pw-zack", "")
	bob, bobToken, _ := h.register(t, "amy", "pw-amy", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/join", map[string]any{}, bobToken), http.StatusOK)

	event := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+bob+"?format=event", aliceToken), http.StatusOK).decoded(t)
	for _, key := range []string{"sender", "type", "state_key", "event_id", "content", "origin_server_ts"} {
		if _, ok := event[key]; !ok {
			t.Errorf("format=event response is missing %q: %v", key, event)
		}
	}
	if event["type"] != "m.room.member" || event["state_key"] != bob {
		t.Errorf("format=event returned the wrong event: %v", event)
	}

	content := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.member/"+bob, aliceToken), http.StatusOK).decoded(t)
	if _, ok := content["membership"]; !ok {
		t.Errorf("the default state response should be the content object: %v", content)
	}
	if _, found := content["event_id"]; found {
		t.Errorf("the default state response leaked event fields: %v", content)
	}
}

// TestRoomVersionRangeIsWiderThanTheDefault checks that the versions this server offers
// cover the range where knock and restricted join rules exist, because a server that can
// only make rooms in the two newest versions cannot host the rooms clients ask for.
func TestRoomVersionRangeIsWiderThanTheDefault(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "yan", "pw-yan", "")
	capabilities := h.mustStatus(t, h.get("/_matrix/client/v3/capabilities", aliceToken), http.StatusOK).decoded(t)
	roomVersions, _ := capabilities["capabilities"].(map[string]any)["m.room_versions"].(map[string]any)
	available, _ := roomVersions["available"].(map[string]any)
	for _, version := range []string{"7", "8", "9", "10", "11"} {
		if available[version] == nil {
			t.Errorf("room version %s is not advertised", version)
			continue
		}
		roomID := createRoom(t, h, aliceToken, map[string]any{"room_version": version})
		if got := roomVersionOf(t, h, roomID); got != version {
			t.Errorf("room created as %s is stored as %s", version, got)
		}
	}
	for _, legacy := range []string{"1", "2", "3", "6"} {
		if available[legacy] != nil {
			t.Errorf("room version %s is advertised; its state resolution is not implemented", legacy)
		}
	}
}

// TestLegacyR0Paths records that the historical r0 prefix is served alongside v3, which
// is what clients that still use it expect.
func TestLegacyR0Paths(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "victor2", "pw-victor2", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})

	h.mustStatus(t, h.get("/_matrix/client/r0/rooms/"+roomID+"/joined_members", aliceToken), http.StatusOK)
	h.mustStatus(t, h.get("/_matrix/client/r0/rooms/"+roomID+"/state/m.room.join_rules/", aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/r0/createRoom", map[string]any{"preset": "public_chat"}, aliceToken), http.StatusOK)
	if res := h.get("/_matrix/client/r0/rooms/"+roomID+"/not_a_real_endpoint", aliceToken); res.status != http.StatusNotFound {
		t.Errorf("unknown r0 endpoint: status %d, want 404 (body %s)", res.status, res.body)
	}
}
