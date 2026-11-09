// Package historyvis answers one question, which three doors ask: may this reader be given this
// event's content.
//
// The answer is not a property of the event alone, nor of the reader alone. It is the room's
// `m.room.history_visibility` **as it stood when the event was written**, read against the
// reader's membership **at that moment** — which is why the answer comes out of the state
// snapshot the event was persisted with rather than out of what the room says today. A room that
// is `world_readable` now was not necessarily so an hour ago, and a member now was not a member
// when the oldest message in the room was sent.
//
// The specification names four values — `world_readable`, `shared`, `invited`, `joined` — and says
// of them: "These options are applied at the point an event is sent. Checks are performed with the
// state of the m.room.history_visibility event when the event in question is added to the DAG.
// This means clients cannot retrospectively choose to show or hide history to new users if the
// setting at that time was more restrictive." That sentence is the whole design of this package.
//
// The three doors are a client reading `/event`, `/messages`, `/relations`, `/threads` or a sync;
// a federation peer being offered an event with its content intact or only redacted; and this
// server's own outbound fan-out. They are the same rule, and they were the same rule already —
// but written inside the room service, where only the room's own doors could reach it. That is
// the reason for this package: a rule that lives in one package and is needed by two is a rule
// that will be quietly reimplemented slightly differently by the second.
package historyvis

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
)

// Service decides visibility from the stored events and their state snapshots.
type Service struct {
	events persist.EventRepo
	state  persist.StateRepo
	groups persist.StateGroupRepo
}

// New binds the decision to a database.
func New(db *persist.Database) *Service {
	return &Service{events: db.Events, state: db.State, groups: db.StateGroups}
}

// WithTx returns the decision bound to an open transaction. A read taken inside a transaction has
// to see the rows the write inside it just made, or a room would answer one thing while it is
// being changed and another immediately after.
func (s *Service) WithTx(tx *gorm.DB) *Service {
	return &Service{
		events: s.events.WithTx(tx),
		state:  s.state.WithTx(tx),
		groups: s.groups.WithTx(tx),
	}
}

// atMoment is what one state snapshot says about the room and about one reader.
type atMoment struct {
	// visibility is the room's setting at that moment. snapshotKnown says whether this server
	// has a snapshot for the event at all: without one, `visibility` means nothing, and the
	// distinction is the difference between a gap in the record and a licence to guess.
	visibility    string
	snapshotKnown bool
	joined        bool
	invited       bool
	// memberInCurrentState is the room read today rather than at the moment: it is what the
	// `shared` answer turns on, for a user as for a peer.
	memberInCurrentState bool
}

// cacheKey is one snapshot asked about one reader. Two readers of the same page share the
// snapshot but not the answer, because the membership the snapshot names is theirs.
type cacheKey struct {
	group int64
	who   string
}

// Decide answers for a set of events at once, sharing one lookup per state group. A page of
// history usually sits in a handful of snapshots, and asking event by event would repeat the
// same question for each of them. The result is keyed by event ID; an event that does not appear
// was refused.
func (s *Service) Decide(ctx context.Context, rows []*persist.Event, userID string) (map[string]bool, error) {
	out := make(map[string]bool, len(rows))
	if userID == "" || len(rows) == 0 {
		return out, nil
	}
	// One snapshot read per group, and one batch of event reads per group.
	byGroup := map[cacheKey]atMoment{}
	for _, row := range rows {
		facts, err := s.factsFor(ctx, row, userID, byGroup)
		if err != nil {
			return nil, err
		}
		// A room's history rules do not hide a user's own membership from them. The leave has
		// to reach the client that left, or that client cannot finish its own story; the invite
		// has to reach the invited user, or the room keeps the fact of inviting them from the
		// person it invited. This is also why the answer cannot be read off the snapshot alone:
		// at the moment of a leave, the snapshot says the reader is gone.
		out[row.EventID] = namesReader(row, userID) || allow(facts)
	}
	return out, nil
}

// MayRead answers the question for one event and one reader.
func (s *Service) MayRead(ctx context.Context, row *persist.Event, userID string) (bool, error) {
	allowed, err := s.Decide(ctx, []*persist.Event{row}, userID)
	if err != nil {
		return false, err
	}
	return allowed[row.EventID], nil
}

// MayReadAsServer answers the same question for a homeserver, which is what decides whether a
// peer is handed the event with its content intact or only in the redacted form. The distinction
// the room draws is between an event that was destroyed and one that this reader was never
// entitled to read: the first is a fact about the room and is stored, the second is worked out
// here, at the moment of serving.
func (s *Service) MayReadAsServer(ctx context.Context, row *persist.Event, serverName string) (bool, error) {
	if row == nil || serverName == "" {
		return false, nil
	}
	if row.Type == "m.room.member" && row.StateKey != nil && userOnServer(*row.StateKey, serverName) {
		// A peer's own user's membership reaches that peer: a server cannot carry out a leave,
		// an invite or a knock on behalf of its resident without being told what the room now
		// says about them.
		return true, nil
	}
	facts, err := s.factsForServer(ctx, row, serverName, map[cacheKey]atMoment{})
	if err != nil {
		return false, err
	}
	switch facts.visibility {
	case "world_readable":
		return true, nil
	case "", "shared":
		// `shared` reads backwards: whoever has a joined user here now may read what came
		// before. A server that joined late is given the older events intact for this reason.
		return facts.memberInCurrentState, nil
	case "joined", "invited":
		if !facts.snapshotKnown {
			// Not knowing who was here when this was written is not a licence to assume the
			// asker was.
			return false, nil
		}
		return facts.joined || (facts.invited && facts.visibility == "invited"), nil
	default:
		// A value outside the four the pinned specification names is answered as the protocol
		// answers an absent entry — `shared`. That is a decision about malformed input rather
		// than a specification answer, and it is not the only defensible one: reading an
		// unknown value as the strictest known fence would also work, and would stop a proposal
		// this server has not read from widening history. What is written here is the reading
		// that follows the documented default, and a version bump that brings in new values
		// should revisit it rather than inherit it.
		return facts.memberInCurrentState, nil
	}
}

// namesReader reports whether an event is the reader's own membership in this room.
func namesReader(row *persist.Event, userID string) bool {
	return row.Type == "m.room.member" && row.StateKey != nil && *row.StateKey == userID
}

// allow is the client-side reading of one snapshot's facts.
func allow(facts atMoment) bool {
	switch facts.visibility {
	case "world_readable":
		return true
	case "", "shared":
		// The reader has a membership in the room's current state — joined, invited, knocked
		// or left. How far a departed member reaches is a separate question, answered by the
		// read horizon where the page is cut; this only decides whether the room is theirs to
		// read at all. Someone who was never in the room has no membership here and is refused.
		return facts.memberInCurrentState
	case "joined", "invited":
		if !facts.snapshotKnown {
			// The room limits history to particular moments and this server cannot say what
			// the moment was. Refuse: a missing snapshot is a gap in the record, not an
			// invitation to read on.
			return false
		}
		return facts.joined || (facts.invited && facts.visibility == "invited")
	default:
		return facts.memberInCurrentState
	}
}

// factsFor reads one event's snapshot for one user, reusing what has already been read for the
// same snapshot in this call.
func (s *Service) factsFor(ctx context.Context, row *persist.Event, userID string, byGroup map[cacheKey]atMoment) (atMoment, error) {
	facts, err := s.factsAt(ctx, row, userID, func(entry persist.StateGroupEntry) bool {
		return entry.Type == "m.room.member" && entry.StateKey == userID
	}, byGroup)
	if err != nil {
		return facts, err
	}
	here, err := s.memberInCurrentState(ctx, row.RoomID, userID)
	if err != nil {
		return facts, err
	}
	facts.memberInCurrentState = here
	return facts, nil
}

// factsAt reads the snapshot an event was persisted with and pulls out of it the room's history
// visibility at that moment and what the snapshot says about the reader.
func (s *Service) factsAt(ctx context.Context, row *persist.Event, who string, isReader func(persist.StateGroupEntry) bool, byGroup map[cacheKey]atMoment) (atMoment, error) {
	groupID, err := s.groups.GroupOf(ctx, row.EventID)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			// No snapshot for this event: the moment is unknown, whatever the room says today.
			return atMoment{}, nil
		}
		return atMoment{}, err
	}
	key := cacheKey{group: groupID, who: who}
	if facts, found := byGroup[key]; found {
		return facts, nil
	}
	entries, err := s.groups.Entries(ctx, groupID)
	if err != nil {
		return atMoment{}, err
	}
	var visibilityEventID string
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		switch {
		case entry.Type == "m.room.history_visibility" && entry.StateKey == "":
			visibilityEventID = entry.EventID
		case isReader(entry):
			ids = append(ids, entry.EventID)
		}
	}
	facts := atMoment{}
	if visibilityEventID == "" {
		// A snapshot holding no visibility event: the room had no such setting at that moment,
		// which the protocol reads as `shared`.
		facts.visibility, facts.snapshotKnown = "shared", true
	} else {
		ids = append(ids, visibilityEventID)
	}
	rows, err := s.events.ByIDs(ctx, ids)
	if err != nil {
		return atMoment{}, err
	}
	seenVisibility := visibilityEventID == ""
	for _, found := range rows {
		if found.EventID == visibilityEventID {
			seenVisibility = true
			facts.visibility = contentString(found, "history_visibility")
			if facts.visibility == "" {
				// The event exists and says nothing usable. That is the same as no setting.
				facts.visibility = "shared"
			}
			continue
		}
		switch contentString(found, "membership") {
		case "join":
			facts.joined = true
		case "invite":
			facts.invited = true
		}
	}
	if !seenVisibility {
		// The snapshot names a visibility event this server does not hold. That is not "the
		// room had no visibility setting", so the moment stays unknown.
		return atMoment{}, nil
	}
	facts.snapshotKnown = true
	byGroup[key] = facts
	return facts, nil
}

// factsForServer is the same read with the peer's users in place of one user, plus what the
// room's current state says about that peer, which is what a `shared` room turns on.
func (s *Service) factsForServer(ctx context.Context, row *persist.Event, serverName string, byGroup map[cacheKey]atMoment) (atMoment, error) {
	facts, err := s.factsAt(ctx, row, serverName, func(entry persist.StateGroupEntry) bool {
		return entry.Type == "m.room.member" && userOnServer(entry.StateKey, serverName)
	}, byGroup)
	if err != nil {
		return facts, err
	}
	joined, err := s.serverHasJoinedUser(ctx, row.RoomID, serverName)
	if err != nil {
		return facts, err
	}
	facts.memberInCurrentState = joined
	return facts, nil
}

// memberInCurrentState reports whether the room's current state holds any membership for this
// user — joined, invited, knocked or left. A user who was never in the room has none.
func (s *Service) memberInCurrentState(ctx context.Context, roomID, userID string) (bool, error) {
	entry, err := s.state.Get(ctx, roomID, "m.room.member", userID)
	if err != nil {
		if errors.Is(err, persist.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return entry != nil, nil
}

// serverHasJoinedUser reports whether the peer has a joined user in the room's current state.
func (s *Service) serverHasJoinedUser(ctx context.Context, roomID, serverName string) (bool, error) {
	entries, err := s.state.ByType(ctx, roomID, "m.room.member")
	if err != nil {
		return false, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if userOnServer(entry.StateKey, serverName) {
			ids = append(ids, entry.EventID)
		}
	}
	if len(ids) == 0 {
		return false, nil
	}
	rows, err := s.events.ByIDs(ctx, ids)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if contentString(row, "membership") == "join" {
			return true, nil
		}
	}
	return false, nil
}

// contentString reads one string out of an event's content, from whichever form of the event this
// server keeps. A redaction does not remove the keys that identify what an event is about — the
// membership of a member event, the visibility of a visibility event — so both forms are read the
// same way.
func contentString(row *persist.Event, key string) string {
	raw := row.JSON
	if row.Redacted && len(row.RedactedJSON) > 0 {
		raw = row.RedactedJSON
	}
	var event struct {
		Content map[string]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return ""
	}
	value, ok := event.Content[key]
	if !ok {
		return ""
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		return ""
	}
	return out
}

// userOnServer asks whether a Matrix user ID names a user on the given homeserver.
// userOnServer asks whether a user ID belongs to a homeserver, by its grammar rather than by
// splitting on a colon. The difference is not cosmetic: a server name may end in a port, and a
// last-colon split compares the port alone against a full server name, so a peer called
// `example.org:8448` holds nothing in every room it is a member of. History visibility turns on
// exactly that question, which is how a whole federation test deployment reads as a room of
// strangers and receives its own history redacted.
func userOnServer(userID, serverName string) bool {
	domain, err := matrixid.UserDomain(userID)
	if err != nil {
		return false
	}
	return domain == serverName
}
