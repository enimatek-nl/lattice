package federation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/enimatek/lattice/internal/rooms"
)

// TestGapRefusalsAreRecognisedAsGaps pins the detection that any hole-closing behaviour depends on.
//
// Both refusals — an event whose predecessors are not held, and an event whose authorising events are
// not held — have to read as a gap rather than a verdict, wrapped as anything a caller can see through,
// and have to name both the event they are about and the events they lack. A refusal that loses the
// distinction cannot be told apart from a bad signature, and a refusal that loses the names can be
// reported but not repaired.
//
// This is also the only thing keeping the plumbing honest while the filling itself is disconnected:
// the mechanism is correct, its connection to `/send` is what T-4218 has to get right.
func TestGapRefusalsAreRecognisedAsGaps(t *testing.T) {
	predecessors := &rooms.MissingPredecessorsError{
		EventID: "$cited", RoomID: "!room:hs.test", Missing: []string{"$a", "$b"},
	}
	auth := &rooms.MissingAuthEventsError{
		EventID: "$cited2", RoomID: "!room:hs.test", Missing: []string{"$create"},
	}
	for _, err := range []error{
		predecessors,
		auth,
		fmt.Errorf("rooms: wrapped: %w", predecessors),
		fmt.Errorf("and then: %w", auth),
	} {
		gap, ok := missingHistory(err)
		if !ok {
			t.Fatalf("%T (%v) was not read as a gap", err, err)
		}
		if gap.CitedEvent() == "" {
			t.Errorf("%T named no event, so no peer could be told which event this gap is about", err)
		}
		if len(gap.MissingEvents()) == 0 {
			t.Errorf("%T named no missing events, so the gap could not be asked for", err)
		}
	}
	if _, ok := missingHistory(errors.New("rooms: the signature does not verify")); ok {
		t.Errorf("a refusal on its merits was read as a gap; the sender would be told to fetch what it must not fetch")
	}
}
