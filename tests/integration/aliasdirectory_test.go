package integration

import (
	"net/http"
	"testing"

	"github.com/enimatek/lattice/internal/matrixerr"
)

// TestAliasTargetAnswersAFederationQuery is the local half of remote alias resolution: the answer a peer
// gets from `GET /_matrix/federation/v1/query/directory` is built here, and it is the answer that decides
// whether somebody else's user can join this room by the name a human would type.
//
// The `servers` list is checked because it is the part a joining server acts on. A room ID with nowhere to
// take it is a worse answer than no answer, and this server's own name has to be in the list even when it
// is the only entry — an alias this server publishes is a room this server holds.
func TestAliasTargetAnswersAFederationQuery(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, token, _ := h.register(t, "aliasowner", "pw-aliasowner", "")

	roomID := createRoom(t, h, token, map[string]any{"room_alias_name": "front-door"})
	alias := "#front-door:hs1"

	gotRoomID, servers, err := h.app.Rooms.AliasTarget(t.Context(), alias)
	if err != nil {
		t.Fatalf("resolving %q: %v", alias, err)
	}
	if gotRoomID != roomID {
		t.Errorf("the alias %q names %s, want %s", alias, gotRoomID, roomID)
	}
	var found bool
	for _, server := range servers {
		if server == "hs1" {
			found = true
		}
	}
	if !found {
		t.Errorf("the servers that could take a join of %s are %v, which does not name this server", roomID, servers)
	}

	// And the question an unknown alias gets is "no such alias", not an empty room ID. A peer that
	// receives a room ID of "" would join the empty string, and a peer that receives a not-found goes
	// and asks somebody else, which is the behaviour an alias is supposed to have.
	if _, _, err := h.app.Rooms.AliasTarget(t.Context(), "#no-such-door:hs1"); err == nil {
		t.Error("an unknown alias resolved; it should be refused")
	} else if failure, ok := matrixerr.As(err); !ok || failure.Code != matrixerr.CodeNotFound {
		t.Errorf("an unknown alias failed with %v, want M_NOT_FOUND", err)
	}
}

// TestFederationAliasQueryRequiresASignature keeps the alias query routed and behind a signature. The
// status matters more than it looks: 404 would mean this server does not answer the question at all, which
// is what made every remote alias unjoinable, and 200 without a signature would let any visitor read the
// room IDs behind this server's private aliases.
func TestFederationAliasQueryRequiresASignature(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	res := h.federationGet(t, "/_matrix/federation/v1/query/directory?room_alias=%23front-door%3Ahs1")
	if res.status == http.StatusNotFound {
		t.Fatalf("this server does not answer an alias query at all; a remote user cannot join by alias")
	}
	if res.status != http.StatusUnauthorized && res.status != http.StatusForbidden {
		t.Fatalf("an unsigned alias query returned %d (%s); it must be signed", res.status, res.body)
	}
}
