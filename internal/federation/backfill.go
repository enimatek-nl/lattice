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
	"strconv"

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
	// ForwardExtremities says where this server's knowledge of a room ends. A peer asked for a
	// hole has to be told both edges of it, and this is the lower one: not "the beginning", and
	// not a guess at how far back the copy goes, but the events this server holds that nothing
	// here cites as their successor.
	ForwardExtremities(ctx context.Context, roomID string) ([]string, error)
	// NameRemotePDUs gives a batch of events their identifiers under the room's version rules.
	// A fetcher cannot compute those itself — from room version 2 they are not in the bytes — and
	// a fetcher that keys received events by a field that is not there quietly stores nothing.
	NameRemotePDUs(ctx context.Context, roomID string, pdus [][]byte) ([]string, error)
}

// FillOutcome says how much of a room's history arrived.
type FillOutcome struct {
	// Fetched are the events a remote server handed over, whether this server kept them or not.
	Fetched []string
	// Stored are the fetched events this server accepted.
	Stored []string
	// Refused maps a fetched event to the reason this server would not store it.
	Refused map[string]string
	// Unresolved names the fetched events this server stored but could not place, because what
	// *they* cite is still missing. It is neither accepted nor refused: it is the next thing to
	// ask about, and the reason a fetch is a loop rather than a call.
	Unresolved []string
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
	return b.store(ctx, roomID, fetched, outcome)
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

// GetMissingEvents asks a peer for the events in a room between two points of its history.
//
// This is the endpoint the Server-Server specification provides for a hole, and it is the one that
// fits the shape of the problem: the asking server names what it holds at both ends — `earliest`
// is where its own knowledge stops, `latest` is the event that cited something absent — and the
// peer walks its graph between those two and hands back what lies in between. A server reduced to
// asking for events one at a time by ID has to know the IDs first, which is exactly the thing it
// lacks; a hole in the middle of a chain is then unaskable, because the names of what is missing
// only become known once the missing events arrive.
//
// The limit is the caller's, not the peer's, and a peer is entitled to answer with less: the walk
// is bounded at both ends precisely so that answering cannot be made to do unbounded work.
func (c *Client) GetMissingEvents(ctx context.Context, destination, roomID string, earliest, latest []string, limit int) ([]json.RawMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	body, err := json.Marshal(map[string]any{
		"earliest_events": earliest,
		"latest_events":   latest,
		"limit":           limit,
	})
	if err != nil {
		return nil, fmt.Errorf("federation: cannot make the request for a room's missing events: %w", err)
	}
	target := "/_matrix/federation/v1/get_missing_events/" + url.PathEscape(roomID)
	response, err := c.Do(ctx, destination, http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("federation: %s answered the request for missing events with %d", destination, response.StatusCode)
	}
	var answer struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return nil, fmt.Errorf("federation: %s answered the request for missing events with something unreadable: %v", destination, err)
	}
	return answer.Events, nil
}

// FillBehind closes the hole underneath an event this server was just handed.
//
// The shape of the ask is what makes this different from [Backfiller.FillFrom]. An event arrives
// citing predecessors this server does not hold, and the server that offered it plainly has them —
// but their IDs are inside the events that are missing, so asking for them by name asks for
// nothing. This server instead says "I hold everything up to my frontier, and I was just given
// this event: what lies between?", which is a question the peer can answer from its own graph, and
// one round trip closes a hole of any length.
//
// One pass, oldest-first storage, and the outliers re-linked: an event that was parked because it
// could not be placed becomes readable history the moment its predecessors arrive, and that
// relinking is the difference between a room that catches up and a room whose events sit in a
// drawer forever, correct and unseen.
func (b *Backfiller) FillBehind(ctx context.Context, roomID string, destinations []string, newest string) (FillOutcome, error) {
	outcome := FillOutcome{Refused: map[string]string{}}
	if b.Client == nil || b.Store == nil {
		return outcome, errors.New("federation: backfill is not wired up")
	}
	if newest == "" || len(destinations) == 0 {
		return outcome, nil
	}
	earliest, err := b.Store.ForwardExtremities(ctx, roomID)
	if err != nil {
		return outcome, err
	}
	limit := b.MaxEvents
	if limit <= 0 {
		limit = 100
	}
	// Fetched events can be storable and still unplaceable: an event whose own citations reach
	// further than this one answer did. That is not a failure to report and stop on, because the
	// events that just arrived say exactly what is still missing, by name — and unlike the hole this
	// call set out to close, those names are known, so they can be asked for directly. One follow-up
	// pass, and only one: a room that never stops producing new holes is a room to stop fetching, not
	// a room to fetch forever, and the next incoming event will start the loop again anyway.
	follow := func(filled FillOutcome) FillOutcome {
		if len(filled.Unresolved) == 0 {
			return filled
		}
		b.Log.Info("federation: a filled hole left events this server still cannot place",
			slog.String("room_id", roomID), slog.Int("unresolved", len(filled.Unresolved)))
		next, err := b.FillFrom(ctx, roomID, destinations, filled.Unresolved)
		if err != nil {
			b.Log.Warn("federation: the events behind a filled hole could not be fetched",
				slog.String("room_id", roomID), slog.Any("error", err))
			return filled
		}
		filled.Fetched = append(filled.Fetched, next.Fetched...)
		filled.Stored = append(filled.Stored, next.Stored...)
		filled.Linked += next.Linked
		for id, reason := range next.Refused {
			filled.Refused[id] = reason
		}
		return filled
	}

	var lastErr error
	for _, destination := range destinations {
		events, err := b.Client.GetMissingEvents(ctx, destination, roomID, earliest, []string{newest}, limit)
		if err != nil {
			lastErr = err
			continue
		}
		if len(events) == 0 {
			// An empty answer is an answer: the peer walked between the two points and found
			// nothing this server lacked. Trying the next server would be asking the same
			// question of somebody else, which is worth doing once and not worth logging as a
			// failure.
			return outcome, nil
		}
		// Named before being filed. The events a peer sends for a hole carry no `event_id` — from
		// room version 2 the identifier is a hash of the event, which the room's version rules
		// compute and nothing else can — so this is not a lookup of a field but an ask of the one
		// component that knows the room. Keying by the field instead drops the whole answer and
		// counts it as zero events received, which is how a peer that answered everything gets
		// logged as a peer that answered nothing.
		bodies := make([][]byte, len(events))
		for index, raw := range events {
			bodies[index] = raw
		}
		named, err := b.Store.NameRemotePDUs(ctx, roomID, bodies)
		if err != nil {
			return outcome, err
		}
		byID := make(map[string][]byte, len(events))
		for index, raw := range bodies {
			id := ""
			if index < len(named) {
				id = named[index]
			}
			if id == "" {
				// Only an event that cannot be named in this room is dropped: it is not an event
				// of this room, and storing it would be filing a stranger's event where a client
				// would later read it as history.
				b.Log.Warn("federation: a server sent an event that is not part of this room",
					slog.String("room_id", roomID))
				continue
			}
			byID[id] = raw
			outcome.Fetched = append(outcome.Fetched, id)
		}
		filled, err := b.store(ctx, roomID, byID, outcome)
		if err != nil {
			return filled, err
		}
		return follow(filled), nil
	}
	if lastErr != nil {
		return outcome, lastErr
	}
	return outcome, nil
}

// store places a fetched set of events in this server's room, parents before children, and reports
// what happened. It is shared by the two ways a hole gets closed so that both leave the room in the
// same shape: an event stored out of order is a position in the history that no later reader can
// reconstruct.
func (b *Backfiller) store(ctx context.Context, roomID string, fetched map[string][]byte, outcome FillOutcome) (FillOutcome, error) {
	ordered := parentsFirst(fetched)
	pdus := make([][]byte, 0, len(ordered))
	pdus = append(pdus, ordered...)
	bundle := rooms.RemoteBundle{PDUs: pdus, RoomID: roomID}
	stored, err := b.Store.AcceptRemoteBundle(ctx, bundle, pduVerifier(ctx, b.Keyring, b.Name, b.LocalKeys))
	if err != nil {
		return outcome, err
	}
	outcome.Stored = append(outcome.Stored, stored.Accepted...)
	outcome.Unresolved = append(outcome.Unresolved, stored.Unresolved...)
	for eventID, reason := range stored.Refused {
		outcome.Refused[eventID] = reason
	}
	linked, err := b.Store.ResolveOutliers(ctx, roomID)
	if err != nil {
		return outcome, err
	}
	outcome.Linked = linked
	// `unresolved` is logged because it is the number that explains a hole that stays a hole. An
	// event can arrive, be well made, be signed by somebody we trust, and still be something this
	// server cannot place, because *its* citations reach further than the fetch did. Counted here,
	// that reads as "the peer gave us three events and we could use none of them yet", which is a
	// step — the next fetch has three more names to ask about. Uncounted, it reads exactly like
	// "the peer gave us nothing", and the operator stops looking.
	b.Log.Info("federation: a room's history was filled in",
		slog.String("room_id", roomID), slog.Int("fetched", len(outcome.Fetched)),
		slog.Int("stored", len(stored.Accepted)), slog.Int("linked", linked),
		slog.Int("refused", len(stored.Refused)), slog.Int("unresolved", len(stored.Unresolved)))
	return outcome, nil
}

// eventIDOf reads the identifier out of an event without deciding whether the event is any good:
// the ID is the name the rest of this package files things under, and a bad event still has one.
func eventIDOf(raw json.RawMessage) string {
	var envelope struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return envelope.EventID
}

// GetBackfill asks a peer for the history of a room before an event this server holds.
//
// This is the ask that `/get_missing_events` cannot make. That endpoint walks between two points the
// asker names, and it needs both: a server whose room runs out behind it has nothing to name below its
// oldest event, which is the whole reason it is asking. So it names the one event it does have and asks
// what came before, and the peer walks its own graph back from there.
//
// A server that does not answer this is a server whose clients are told, politely and by implication,
// that a room began on the day this server first heard of it.
func (c *Client) GetBackfill(ctx context.Context, destination, roomID, since string, limit int) ([]json.RawMessage, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	target := "/_matrix/federation/v1/backfill/" + url.PathEscape(roomID) +
		// `origin` is deliberately absent: this server's identity travels in the request's signature,
		// which is what the peer checks it against, and a query parameter saying who is asking is a
		// claim rather than evidence.
		"?v=1&source=" + url.QueryEscape(since) +
		"&limit=" + strconv.Itoa(limit)
	response, err := c.Do(ctx, destination, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("federation: %s answered the request for history with %d", destination, response.StatusCode)
	}
	var answer struct {
		PDUs []json.RawMessage `json:"pdus"`
	}
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return nil, fmt.Errorf("federation: %s answered the request for history with something unreadable: %v", destination, err)
	}
	return answer.PDUs, nil
}

// BackfillBefore fills the history of a room behind an event this server holds, and reports what it
// stored. It is [Backfiller.FillBehind]'s counterpart for a room that runs out rather than opens: no
// hole, no citing event, no names to ask for — only a floor, and a client waiting above it.
func (b *Backfiller) BackfillBefore(ctx context.Context, roomID string, destinations []string, oldest string) (FillOutcome, error) {
	outcome := FillOutcome{Refused: map[string]string{}}
	if b.Client == nil || b.Store == nil || oldest == "" || len(destinations) == 0 {
		return outcome, nil
	}
	limit := b.MaxEvents
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var lastErr error
	for _, destination := range destinations {
		events, err := b.Client.GetBackfill(ctx, destination, roomID, oldest, limit)
		if err != nil {
			lastErr = err
			continue
		}
		if len(events) == 0 {
			// The peer walked back from this event and reached the beginning of the room. That is an
			// answer, and the kind that ends the asking.
			return outcome, nil
		}
		bodies := make([][]byte, len(events))
		for index, raw := range events {
			bodies[index] = raw
		}
		named, err := b.Store.NameRemotePDUs(ctx, roomID, bodies)
		if err != nil {
			return outcome, err
		}
		byID := make(map[string][]byte, len(events))
		for index, raw := range bodies {
			if index < len(named) && named[index] != "" {
				byID[named[index]] = raw
				outcome.Fetched = append(outcome.Fetched, named[index])
			}
		}
		return b.store(ctx, roomID, byID, outcome)
	}
	return outcome, lastErr
}

// RoomHistory serves a reader who has run out of history. It is the room service's history backfiller:
// the client API's `/messages` has to be able to tell "this is where the room began" apart from "this
// is where this server stopped being told", and the only way to tell them apart is to ask somebody who
// was there earlier.
//
// It sits here rather than in the rooms service because the two things it needs live on either side of
// the federation boundary: which servers are in a room is a fact about this server's database, and how
// to ask one of them for events is a fact about signing a request and putting up with the answer.
type RoomHistory struct {
	Backfill *Backfiller
	// Servers lists the servers that are in a room, which is who may be asked. A room with nobody
	// else in it has nobody to ask, and that is a different answer from asking a peer who had nothing
	// to give: the first means this server is the whole room, the second means the room really is out
	// of events.
	Servers RoomServers
	// Name is this server's own name, which is excluded from the list of servers to ask. Asking for
	// history from the server that ran short of it is a round trip to a mirror.
	Name string
}

// RoomServers is the ability RoomHistory needs from the room service.
type RoomServers interface {
	ServersInRoom(ctx context.Context, roomID string) ([]string, error)
}

// FetchHistoryBefore asks a server in this room for the history behind an event and reports how many
// events this server did not hold and now does.
//
// The count is the useful part, and it is not the number of events the peer answered with. A peer that
// replies with events this server already has has not extended the room by one event, and a reader that
// went back for a second look after such an answer would be reading the same page twice and calling it
// progress.
//
// Nobody being available to ask is reported as zero rather than an error: a room whose members are all
// local has no peer to consult, and the history this server holds is then genuinely the history there
// is.
func (h *RoomHistory) FetchHistoryBefore(ctx context.Context, roomID, eventID string) (int, error) {
	if h == nil || h.Backfill == nil || h.Servers == nil || eventID == "" {
		return 0, nil
	}
	servers, err := h.Servers.ServersInRoom(ctx, roomID)
	if err != nil {
		return 0, err
	}
	peers := make([]string, 0, len(servers))
	for _, server := range servers {
		if server == "" || server == h.Name {
			continue
		}
		peers = append(peers, server)
	}
	if len(peers) == 0 {
		return 0, nil
	}
	// `/backfill` is the ask, not `/get_missing_events`: there is no hole here to name a floor by,
	// because the events below this one were never sent to this server at all.
	outcome, err := h.Backfill.BackfillBefore(ctx, roomID, peers, eventID)
	if err != nil {
		return 0, err
	}
	return len(outcome.Stored), nil
}
