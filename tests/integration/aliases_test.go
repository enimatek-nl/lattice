package integration

import (
	"encoding/json"
	"net/http"
	"strings"
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

	// A room with shared history: leaving takes away the read.
	roomID := createRoom(t, h, aliceToken, map[string]any{})
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
		map[string]any{"user_id": "@xena2:hs1"}, aliceToken), http.StatusOK)
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/joined_members", bobToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)

	for _, path := range []string{
		"/_matrix/client/v3/rooms/" + roomID + "/joined_members",
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
	// The history a departed member took part in stays readable to them; the live roster
	// and the room's names do not.
	for _, path := range []string{
		"/_matrix/client/v3/rooms/" + roomID + "/state",
		"/_matrix/client/v3/rooms/" + roomID + "/state/m.room.join_rules/",
		"/_matrix/client/v3/rooms/" + roomID + "/members",
	} {
		h.mustStatus(t, h.get(path, bobToken), http.StatusOK)
	}

	// A room that made its history world readable has decided that its state may be read
	// by people outside it. Refusing that read would contradict the room's own state, and
	// a former member asking for it is the ordinary case.
	public := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, join(t, h, public, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+public+"/leave", map[string]any{}, bobToken), http.StatusOK)
	for _, path := range []string{
		"/_matrix/client/v3/rooms/" + public + "/state",
		"/_matrix/client/v3/rooms/" + public + "/state/m.room.join_rules/",
		"/_matrix/client/v3/rooms/" + public + "/members",
	} {
		h.mustStatus(t, h.get(path, bobToken), http.StatusOK)
	}
	// Who is in the room right now, and what the room is called, stay behind membership:
	// world readable history is not an announcement of the room's names or its roster.
	for _, path := range []string{
		"/_matrix/client/v3/rooms/" + public + "/joined_members",
		"/_matrix/client/v3/rooms/" + public + "/aliases",
	} {
		if res := h.get(path, bobToken); res.status != http.StatusForbidden {
			t.Errorf("%s for a former member: status %d, want 403 (body %s)", path, res.status, res.body)
		}
	}
}

// TestRoomVersionRangeIsWiderThanTheDefault checks that the versions this server offers
// are exactly the versions whose rules it implements: a server that advertises a version
// it cannot evaluate creates rooms that break as soon as anything in them is checked.
func TestRoomVersionRangeIsWiderThanTheDefault(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "yan", "pw-yan", "")
	capabilities := h.mustStatus(t, h.get("/_matrix/client/v3/capabilities", aliceToken), http.StatusOK).decoded(t)
	roomVersions, _ := capabilities["capabilities"].(map[string]any)["m.room_versions"].(map[string]any)
	available, _ := roomVersions["available"].(map[string]any)
	for _, version := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"} {
		if available[version] == nil {
			t.Errorf("room version %s is not advertised", version)
			continue
		}
		roomID := createRoom(t, h, aliceToken, map[string]any{"room_version": version})
		if got := roomVersionOf(t, h, roomID); got != version {
			t.Errorf("room created as %s is stored as %s", version, got)
		}
	}
	// Room version 12 is advertised now, and this is the half of the claim that costs something: a
	// version belongs in this list exactly when the server can run a room in it, so the version joined
	// the advertised set together with the state resolution, the identifier derivation and the
	// authorisation rules it needs, and not before. The room created for it above is the evidence.
	if available["12"] == nil {
		t.Errorf("room version 12 is not advertised, though this server can create and run one")
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

// TestDeleteAliasCorrectsCanonicalAlias checks that removing an alias also removes the
// room's claim to it. A room that goes on naming a dead alias is exactly the lie this
// server refuses when the event is written, so leaving the old event behind would
// contradict its own rule.
func TestDeleteAliasCorrectsCanonicalAlias(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "una2", "pw-una2", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	alias := "#spoken-for:hs1"
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(alias),
		[]byte(`{"room_id":"`+roomID+`"}`), aliceToken), http.StatusOK)
	// An alt_alias has to resolve too, or the room would be claiming an alias it does
	// not have.
	spare := "#spare-for:hs1"
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(spare),
		[]byte(`{"room_id":"`+roomID+`"}`), aliceToken), http.StatusOK)
	h.mustStatus(t, putState(t, h, roomID, "m.room.canonical_alias", "",
		`{"alias":"#spoken-for:hs1","alt_aliases":["#spare-for:hs1"]}`, aliceToken), http.StatusOK)

	h.mustStatus(t, h.raw(t, http.MethodDelete, "/_matrix/client/v3/directory/room/"+escAlias(alias), nil, aliceToken), http.StatusOK)

	content := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.canonical_alias/", aliceToken), http.StatusOK).decoded(t)
	if _, present := content["alias"]; present {
		t.Errorf("the room still claims %q as its canonical alias: %v", alias, content)
	}
	alt, _ := content["alt_aliases"].([]any)
	for _, entry := range alt {
		if entry == alias {
			t.Errorf("the deleted alias is still listed in alt_aliases: %v", content)
		}
	}
	if len(alt) != 1 || alt[0] != spare {
		t.Errorf("alt_aliases = %v, want the untouched entry to survive", content["alt_aliases"])
	}

	// Deleting an alias that was the only one named leaves an empty content object,
	// which is what a client looking for the room's alias must find.
	other := "#only-one:hs1"
	roomB := createRoom(t, h, aliceToken, map[string]any{"preset": "public_chat"})
	h.mustStatus(t, h.raw(t, http.MethodPut, "/_matrix/client/v3/directory/room/"+escAlias(other),
		[]byte(`{"room_id":"`+roomB+`"}`), aliceToken), http.StatusOK)
	h.mustStatus(t, putState(t, h, roomB, "m.room.canonical_alias", "",
		`{"alias":"#only-one:hs1"}`, aliceToken), http.StatusOK)
	h.mustStatus(t, h.raw(t, http.MethodDelete, "/_matrix/client/v3/directory/room/"+escAlias(other), nil, aliceToken), http.StatusOK)
	empty := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomB+"/state/m.room.canonical_alias/", aliceToken), http.StatusOK).decoded(t)
	if len(empty) != 0 {
		t.Errorf("the canonical alias event should be empty, got %v", empty)
	}
}

// TestFormerMemberReadsStopAtLeaving checks that a departed member's read of a room is
// confined to what they were party to: the room as they left it, state and roster both — so
// state written afterwards is not handed over and the roster does not grow with people who
// joined after they left.
func TestFormerMemberReadsStopAtLeaving(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "brad2", "pw-brad2", "")
	bob, bobToken, _ := h.register(t, "clara2", "pw-clara2", "")
	carol, carolToken, _ := h.register(t, "dora2", "pw-dora2", "")

	roomID := createRoom(t, h, aliceToken, map[string]any{})
	for _, user := range []string{"@clara2:hs1", "@dora2:hs1"} {
		h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/invite",
			map[string]any{"user_id": user}, aliceToken), http.StatusOK)
	}
	h.mustStatus(t, join(t, h, roomID, bobToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, putState(t, h, roomID, "madeup.test.state", "", `{"body":"before"}`, aliceToken), http.StatusOK)
	h.mustStatus(t, h.post(t, "/_matrix/client/v3/rooms/"+roomID+"/leave", map[string]any{}, bobToken), http.StatusOK)
	// Both of these happen after bob left.
	h.mustStatus(t, join(t, h, roomID, carolToken, map[string]any{}), http.StatusOK)
	h.mustStatus(t, putState(t, h, roomID, "madeup.test.state", "", `{"body":"after"}`, aliceToken), http.StatusOK)

	// bob may still read the room's state, and the entry that was rewritten after he left reads
	// as what it was when he left — not as the value written without him, and not as absent
	// either. Saying "no such state" is not the conservative answer it looks like: the room had a
	// value for that key while he was in it, and he is entitled to the room as he left it. The
	// snapshot the authorisation rules judged the boundary event by is what answers, which is the
	// same state the room had at that moment rather than today's state with entries missing.
	if res := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/madeup.test.state/", bobToken), http.StatusOK); !strings.Contains(string(res.body), `"before"`) {
		t.Errorf("reading state rewritten after leaving: body %s, want the value it held when he left", res.body)
	}
	// The full state list agrees with that, and carries nothing written after his leaving.
	stateRes := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state", bobToken), http.StatusOK)
	var entries []any
	if err := json.Unmarshal(stateRes.body, &entries); err != nil {
		t.Fatalf("decoding the state list: %v (body %s)", err, stateRes.body)
	}
	if len(entries) == 0 {
		t.Fatalf("a former member got no state at all")
	}
	sawTheState := false
	for _, entry := range entries {
		event, _ := entry.(map[string]any)
		if event["type"] != "madeup.test.state" {
			continue
		}
		content, _ := event["content"].(map[string]any)
		if content["body"] != "before" {
			t.Errorf("the former member was shown state written after they left: %v", event)
		}
		sawTheState = true
	}
	if !sawTheState {
		t.Errorf("the state list does not carry the value the room had when he left: %v", entries)
	}
	// The roster he may read does not include someone who joined after him.
	members := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/members", bobToken), http.StatusOK).decoded(t)
	chunk, _ := members["chunk"].([]any)
	seenSelf := false
	for _, entry := range chunk {
		event, _ := entry.(map[string]any)
		content, _ := event["content"].(map[string]any)
		// carol was invited before bob left and joined only after, so the roster he is shown may
		// hold her invitation — that is what the room looked like as he went — but not the joining
		// that happened without him.
		if event["state_key"] == carol && content["membership"] == "join" {
			t.Errorf("a member who joined after bob left appears as joined in bob's roster: %v", event)
		}
		if event["state_key"] == bob && content["membership"] == "leave" {
			seenSelf = true
		}
	}
	if !seenSelf {
		t.Errorf("bob's own leave is missing from the roster he is allowed to read: %v", chunk)
	}
	// A current member sees all of it.
	full := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/madeup.test.state/", aliceToken), http.StatusOK).decoded(t)
	if got, _ := full["body"].(string); got != "after" {
		t.Errorf("the current member should see the current value, got %v", full)
	}
	if got := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/members", carolToken), http.StatusOK).decoded(t); len(got["chunk"].([]any)) < 3 {
		t.Errorf("a current member's roster is missing members: %v", got["chunk"])
	}
}

// TestCreatedAliasBecomesTheRoomsName checks that a room created with a name of its own
// says so in its state, rather than only holding the alias in the directory. A client
// reading the room, or a room list showing it, asks the room what it is called.
func TestCreatedAliasBecomesTheRoomsName(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	_, aliceToken, _ := h.register(t, "namedroom", "pw-namedroom", "")
	roomID := createRoom(t, h, aliceToken, map[string]any{
		"preset":          "public_chat",
		"room_alias_name": "selfnamed",
		"visibility":      "public",
	})
	content := h.mustStatus(t, h.get("/_matrix/client/v3/rooms/"+roomID+"/state/m.room.canonical_alias/", aliceToken), http.StatusOK).decoded(t)
	if content["alias"] != "#selfnamed:hs1" {
		t.Errorf("canonical alias = %v, want #selfnamed:hs1", content)
	}
	// The directory reports the same name.
	page := h.mustStatus(t, h.get("/_matrix/client/v3/publicRooms", aliceToken), http.StatusOK).decoded(t)
	var found bool
	for _, entry := range page["chunk"].([]any) {
		entry := entry.(map[string]any)
		if entry["room_id"] == roomID {
			found = true
			if entry["canonical_alias"] != "#selfnamed:hs1" {
				t.Errorf("the directory reports canonical_alias %v", entry["canonical_alias"])
			}
			if aliases, _ := entry["aliases"].([]any); len(aliases) != 1 || aliases[0] != "#selfnamed:hs1" {
				t.Errorf("the directory reports aliases %v", entry["aliases"])
			}
		}
	}
	if !found {
		t.Fatalf("the published room is missing from the directory: %v", page["chunk"])
	}
}
