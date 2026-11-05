package unit

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/roomversion"
)

// A refusal is a decision, and a decision that is forgotten is worse than no decision:
// the same bad event can be offered again, and an event that cites it gets judged as
// though nothing had been settled. These tests cover the memory itself and the rule that
// depends on it.

func TestRejectionsAreRememberedByEventID(t *testing.T) {
	t.Parallel()
	db := openRejectionStore(t)
	ctx := context.Background()

	if err := db.Events.RecordRejection(ctx, &persist.EventRejection{
		EventID: "$forged:remote.example", RoomID: "!room:remote.example",
		Reason: "the event failed its signature and hash checks: the origin signature does not check",
	}); err != nil {
		t.Fatalf("recording a refusal: %v", err)
	}

	reasons, err := db.Events.Rejections(ctx, []string{"$forged:remote.example", "$never:seen.example"})
	if err != nil {
		t.Fatalf("reading refusals back: %v", err)
	}
	if !strings.Contains(reasons["$forged:remote.example"], "signature") {
		t.Errorf("the reason was not kept: %q", reasons["$forged:remote.example"])
	}
	if _, seen := reasons["$never:seen.example"]; seen {
		t.Errorf("an event that was never refused is reported as refused; a gap and a verdict are not the same answer")
	}

	// A second refusal of the same event does not rewrite the first verdict with a
	// vaguer one.
	if err := db.Events.RecordRejection(ctx, &persist.EventRejection{
		EventID: "$forged:remote.example", RoomID: "!room:remote.example", Reason: "again",
	}); err != nil {
		t.Fatalf("recording the same refusal twice: %v", err)
	}
	reasons, _ = db.Events.Rejections(ctx, []string{"$forged:remote.example"})
	if !strings.Contains(reasons["$forged:remote.example"], "signature") {
		t.Errorf("the first verdict was overwritten by the second: %q", reasons["$forged:remote.example"])
	}

	// An event ID this server holds is not a refusal, and the caller is expected to have
	// checked that; a record for it would turn a stranger's claim into a verdict against
	// a good event.
	if err := db.Events.RecordRejection(ctx, &persist.EventRejection{EventID: "", RoomID: "!room:remote.example"}); err != nil {
		t.Fatalf("an empty event ID should be ignored, got %v", err)
	}
	if count, err := countRows(db, "event_rejections"); err != nil || count != 1 {
		t.Errorf("event_rejections has %d rows, want 1 (%v)", count, err)
	}
}

// An auth event that is absent can mean two opposite things: nobody has told this server
// about it yet, or this server was told and refused it. The authorisation rules have to
// tell those apart, because the first is a gap someone could still fill and the second
// makes the event citing it invalid.
func TestAuthorisationNamesARefusedAuthEventRatherThanAGap(t *testing.T) {
	t.Parallel()
	rules := roomversion.MustGet("11")
	state := baseState(t, "11", "public", defaultPL())

	// The power levels event was refused, so it is not stored here. The event that cites
	// it must be refused for that reason — not reported as "cannot be resolved", which is
	// the answer for an event nobody has mentioned.
	state.Reject("$pl:example.org")
	event := mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
		map[string]any{"body": "hi"})
	authorise(rules, event, state)

	err := eventauth.Check(rules, event, state, eventauth.Opts{})
	failure, ok := eventauth.AsFailure(err)
	if !ok {
		t.Fatalf("an event citing a refused auth event was not refused by a rule: %v", err)
	}
	if failure.Rule != "2.3" {
		t.Errorf("refused under rule %s (%s), want 2.3: the distinction between a gap and a verdict is the point",
			failure.Rule, failure.Reason)
	}
	if !strings.Contains(failure.Reason, "rejected on receipt") {
		t.Errorf("the refusal does not say the auth event was refused: %s", failure.Reason)
	}

	// The same event with an auth event that was never mentioned is still a gap.
	state2 := baseState(t, "11", "public", defaultPL())
	event2 := mkEvent("m.room.message", "$new:example.org", "@alice:example.org",
		map[string]any{"body": "hi"},
		map[string]any{"auth_events": []any{"$create:example.org", "$pl:example.org", "$never:example.org"}})
	if err := eventauth.Check(rules, event2, state2, eventauth.Opts{}); err == nil {
		t.Errorf("an event citing an unresolvable auth event was allowed")
	} else if failure, ok := eventauth.AsFailure(err); ok && failure.Rule == "2.3" {
		t.Errorf("an event nobody mentioned was reported as refused: %s", failure.Reason)
	}
}

func countRows(db *persist.Database, table string) (int64, error) {
	var count int64
	err := db.DB().Table(table).Count(&count).Error
	return count, err
}

func openRejectionStore(t *testing.T) *persist.Database {
	t.Helper()
	db, err := persist.Open(filepath.Join(t.TempDir(), "rejections.db"), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
