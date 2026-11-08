package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"

	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Backfiller closes the holes in a room this server holds only partly.
//
// A room that was joined rather than created here arrives with holes by construction: the join
// cites the room's forward extremities, and the state a resident server hands over does not
// include them. Those events are real and their state counts, but this server cannot say where in
// the history they sit, so it keeps them out of what clients read until the gap under them is
// filled. Filling it means asking a server that was in the room before this one was.
//
// The ask is bounded and repeated on purpose. A room's history is unbounded and may be long; a
// server that is asked to catch up on one takes a slice of it, from the end nearest what it already
// holds, and stops when its budget runs out. What it does not fetch stays a gap, and remains
// askable: a gap is not a verdict.
type Backfiller struct {
	Client  *Client
	Keyring *Keyring
	// LocalKeys verifies events this server itself signed; a room's history contains some of
	// them once this server has spoken in it.
	LocalKeys LocalKeyLookup
	Name      string
	Store     GapStore
	Log       *slog.Logger
	// MaxEvents bounds how many events one call fetches. Zero means the default.
	MaxEvents int
	// MaxPasses bounds how many rounds of asking one call makes, each round learning from what
	// the last one brought. Zero means the default.
	MaxPasses int
}

// GapStore is what the backfiller needs from the room service: the holes, what is already held, a
// way to store what is fetched, and a way to say so once an event's predecessors are here.
type GapStore interface {
	AcceptRemoteBundle(ctx context.Context, bundle rooms.RemoteBundle, verify rooms.VerifyPDU) (rooms.BundleOutcome, error)
	MissingPredecessors(ctx context.Context, roomID string, limit int) ([]string, error)
	ResolveOutliers(ctx context.Context, roomID string) (int, error)
	// HoldsEvents says which of the given events this server already stores. A fetcher that
	// does not ask this re-fetches a history it has, and a chain that is partly held should not
	// be fetched at all.
	HoldsEvents(ctx context.Context, eventIDs []string) (map[string]bool, error)
}

// FillOutcome says how much of a room's history arrived.
type FillOutcome struct {
	// Fetched are the events a remote server handed over, whether this server kept them or not.
	Fetched []string
	// Stored are the fetched events this server accepted.
	Stored []string
	// Refused maps a fetched event to the reason this server would not store it.
	Refused map[string]string
	// Linked counts the events that stopped being outliers: their predecessors are here now, so
	// this server knows where they sit and clients can read them.
	Linked int
}

// Fill asks the named servers, in order, for what this room's events cite and this server does not
// hold.
//
// The events are asked for newest-first, which is where a joined room's hole usually is: the
// extremities a join was made against. Each event fetched is followed to its own predecessors, so
// one call walks backwards through the history until the budget runs out or the chain joins up
// with something this server holds.
func (b *Backfiller) Fill(ctx context.Context, roomID string, destinations []string) (FillOutcome, error) {
	return b.fill(ctx, roomID, destinations, nil)
}

// FillFrom catches up on a named set of events rather than on every hole in the room. It is what
// an incoming event asks for: a server offers an event, this server says the events it descends
// from are not here, and the server that offered it is the one to ask. Asking for exactly those,
// and following them backwards, is what keeps a single unexpected event from turning into a survey
// of a room this server may hold only partly.
func (b *Backfiller) FillFrom(ctx context.Context, roomID string, destinations, seeds []string) (FillOutcome, error) {
	return b.fill(ctx, roomID, destinations, seeds)
}

func (b *Backfiller) fill(ctx context.Context, roomID string, destinations, seeds []string) (FillOutcome, error) {
	outcome := FillOutcome{Refused: map[string]string{}}
	if b.Client == nil || b.Store == nil {
		return outcome, errors.New("federation: backfill is not wired up")
	}
	if len(destinations) == 0 {
		return outcome, nil
	}
	budget, passes := b.MaxEvents, b.MaxPasses
	if budget <= 0 {
		budget = 50
	}
	if passes <= 0 {
		passes = 2
	}
	fetched := map[string][]byte{}
	// held caches what this server stores, so the walk stops where its history begins instead of
	// re-asking for events it already has.
	held := map[string]bool{}
	// first holds the events this call was asked for by name. A later pass learns from what the
	// first brought, and asks about the holes that opened behind it.
	first := seeds
	for pass := 0; pass < passes; pass++ {
		seeds := first
		first = nil
		if len(seeds) == 0 {
			var err error
			seeds, err = b.Store.MissingPredecessors(ctx, roomID, budget)
			if err != nil {
				return outcome, err
			}
		}
		queue := make([]string, 0, len(seeds))
		for _, id := range seeds {
			if _, done := fetched[id]; !done {
				queue = append(queue, id)
			}
		}
		if len(queue) == 0 {
			break
		}
		// Walk backwards from the holes. Each event fetched names the events before it, and
		// those are fetched too unless this server already holds them — which is what makes the
		// walk stop at the edge of what this server has rather than running to the room's birth.
		for len(queue) > 0 && len(fetched) < budget {
			eventID := queue[0]
			queue = queue[1:]
			if _, done := fetched[eventID]; done {
				continue
			}
			body, err := b.fetch(ctx, destinations, eventID)
			if err != nil {
				b.Log.Warn("federation: a server could not supply a missing event",
					slog.String("event_id", eventID), slog.String("room_id", roomID),
					slog.Any("error", err))
				continue
			}
			fetched[eventID] = body
			outcome.Fetched = append(outcome.Fetched, eventID)
			unknown := make([]string, 0, 4)
			for _, prev := range predecessorIDs(body) {
				if _, done := fetched[prev]; done || held[prev] {
					continue
				}
				unknown = append(unknown, prev)
			}
			if len(unknown) == 0 {
				continue
			}
			stores, err := b.Store.HoldsEvents(ctx, unknown)
			if err != nil {
				return outcome, err
			}
			for id, holds := range stores {
				held[id] = holds
			}
			for _, prev := range unknown {
				if held[prev] {
					continue
				}
				queue = append(queue, prev)
			}
		}
	}
	if len(fetched) == 0 {
		return outcome, nil
	}

	// Store them parents first. This server gives an event a position in its own ordering when
	// it stores it, and a history fetched newest-first would be read by clients in the order it
	// arrived rather than the order it happened in. The chain this call fetched is a small
	// graph, and its ordering is in the events themselves.
	ordered := parentsFirst(fetched)
	bundle := rooms.RemoteBundle{PDUs: ordered, RoomID: roomID}
	stored, err := b.Store.AcceptRemoteBundle(ctx, bundle, pduVerifier(ctx, b.Keyring, b.Name, b.LocalKeys))
	if err != nil {
		return outcome, err
	}
	outcome.Stored = append(outcome.Stored, stored.Accepted...)
	for eventID, reason := range stored.Refused {
		outcome.Refused[eventID] = reason
	}
	linked, err := b.Store.ResolveOutliers(ctx, roomID)
	if err != nil {
		return outcome, err
	}
	outcome.Linked = linked
	b.Log.Info("federation: a room's history was filled in",
		slog.String("room_id", roomID), slog.Int("fetched", len(outcome.Fetched)),
		slog.Int("stored", len(stored.Accepted)), slog.Int("linked", linked),
		slog.Int("refused", len(stored.Refused)))
	return outcome, nil
}

// fetch asks the named servers, in order, for one event by ID.
func (b *Backfiller) fetch(ctx context.Context, destinations []string, eventID string) ([]byte, error) {
	target := "/_matrix/federation/v1/event/" + url.PathEscape(eventID)
	var lastErr error
	for _, destination := range destinations {
		response, err := b.Client.Do(ctx, destination, http.MethodGet, target, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if response.StatusCode == http.StatusNotFound {
			lastErr = fmt.Errorf("%s does not hold %s", destination, eventID)
			continue
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			lastErr = fmt.Errorf("%s answered the missing event with %d", destination, response.StatusCode)
			continue
		}
		var answer struct {
			PDUs []json.RawMessage `json:"pdus"`
		}
		if err := json.Unmarshal(response.Body, &answer); err != nil {
			return nil, fmt.Errorf("%s answered the missing event with something unreadable: %v", destination, err)
		}
		if len(answer.PDUs) == 0 {
			return nil, fmt.Errorf("%s answered the missing event with no event", destination)
		}
		// The bytes as they arrived: an event is the bytes its origin signed, and re-encoding it
		// here would be judging it by how this server would have written it.
		return answer.PDUs[0], nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no server was asked for %s", eventID)
}

// predecessorIDs reads the events an event cites as coming before it, from its bytes.
func predecessorIDs(body []byte) []string {
	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		return nil
	}
	list, _ := event["prev_events"].([]any)
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if id, ok := entry.(string); ok && id != "" {
			out = append(out, id)
		}
	}
	return out
}

// parentsFirst orders fetched events so that an event comes after the events it cites.
//
// Cycles cannot occur between events that genuinely cite one another, but a malformed or hostile
// set can contain one, and a fetcher that hung on it would be a way to stop a server catching up
// on a room. Anything left over at the end is appended, so it is at least stored.
func parentsFirst(fetched map[string][]byte) [][]byte {
	type node struct {
		body  []byte
		pends []string
	}
	nodes := make(map[string]*node, len(fetched))
	for id, body := range fetched {
		nodes[id] = &node{body: body, pends: predecessorIDs(body)}
	}
	// Oldest first among the events that are ready. Two events that do not cite each other have
	// no order between them, and a timestamp is the only thing left to decide by.
	ready := func() []string {
		out := make([]string, 0, len(nodes))
		for id, entry := range nodes {
			unmet := 0
			for _, prev := range entry.pends {
				if _, waiting := nodes[prev]; waiting {
					unmet++
				}
			}
			if unmet == 0 {
				out = append(out, id)
			}
		}
		sort.Slice(out, func(i, j int) bool {
			left, right := nodes[out[i]], nodes[out[j]]
			if a, b := stampOf(left.body), stampOf(right.body); a != b {
				return a < b
			}
			return out[i] < out[j]
		})
		return out
	}
	out := make([][]byte, 0, len(fetched))
	for len(nodes) > 0 {
		order := ready()
		if len(order) == 0 {
			for _, entry := range nodes {
				out = append(out, entry.body)
			}
			break
		}
		for _, id := range order {
			out = append(out, nodes[id].body)
			delete(nodes, id)
		}
	}
	return out
}

// stampOf reads the origin server's timestamp of an event, for ordering what has to be ordered.
func stampOf(body []byte) int64 {
	var event map[string]any
	if err := json.Unmarshal(body, &event); err != nil {
		return 0
	}
	switch stamp := event["origin_server_ts"].(type) {
	case int64:
		return stamp
	case float64:
		return int64(stamp)
	case json.Number:
		value, _ := stamp.Int64()
		return value
	}
	return 0
}

// pduVerifier is the check a fetched or handed-over event has to pass: its own room version, its
// ID being the hash its content makes, and its origin's signature. Another server's keys come
// from the keyring; this server's own are not fetched over the network.
func pduVerifier(ctx context.Context, keyring *Keyring, name string, local LocalKeyLookup) rooms.VerifyPDU {
	return func(event map[string]any, rules roomversion.Rules) error {
		if origin, _ := event["origin"].(string); origin == name && local != nil {
			return events.Verify(rules, event, local.VerifyLocal)
		}
		if keyring == nil {
			return errors.New("federation: no keyring to check another server's signature with")
		}
		return events.Verify(rules, event, keyring.LookupForRoom(ctx, rules))
	}
}
