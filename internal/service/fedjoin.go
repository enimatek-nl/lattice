package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/enimatek-nl/lattice/internal/authcheck"
	"github.com/enimatek-nl/lattice/internal/eventcrypto"
	"github.com/enimatek-nl/lattice/internal/federation"
	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/power"
	"gorm.io/gorm"
)

// A join across servers is three steps, and the middle one is the reason the other two
// exist:
//
//   - Ask a server that is already in the room which events the join has to build on
//     (make_join). Only a server holding the room's graph can answer that, and the
//     answer decides the event's identity.
//   - Make the event here, with our own signature. The joining server never hands its
//     signing key to anyone, which is why the event is built locally and sent ready.
//   - Hand it over (send_join) and receive the room's state in exchange. This is the
//     only way a server ever learns the state of a room it does not host, and without
//     it a "join" is a claim nobody can act on: a server that has joined a room it
//     cannot describe is not in it.
//
// Both directions are in this file, because the two halves have to agree on the same
// shape and a test that exercises one side needs the other.

// JoinFederatedRoom joins a local user to a room hosted elsewhere, using the servers
// given and then the server the room id names. It returns the join event this server
// made.
//
// A server that cannot answer is passed over rather than believed: the room may be
// reachable through any of the servers in it, and the specification's join is explicit
// that the list of servers is a list of attempts.
func (s *Service) JoinFederatedRoom(ctx context.Context, roomID, userID string, via []string) (*persist.Event, error) {
	// The account is asked about before the transport: a session this server cannot act
	// for is the reason the join cannot happen, and blaming the federation flag would send
	// the operator to the wrong flag.
	if problem := s.joinAccountProblem(ctx, userID); problem != nil {
		return nil, problem
	}
	transport := s.federationTransport()
	if transport == nil {
		return nil, matrix.ErrNotFound("room %s is not hosted by this server and federation is off", roomID)
	}
	// A room in a version this server does not speak is a different answer from a room
	// nobody would talk about, and it is the one the client can act on: the version is
	// named, so the operator knows what to add rather than hunting for a missing room.
	// A room this server is in but does not host can still finish the join of its own user: it
	// holds the graph and the state, so it can build the event and name the witness it can see.
	// What it cannot do is decide the admission alone and write the join into its own copy: the
	// membership of the other rooms a restricted rule reads may be a stale one here, and a server
	// that answered "joined" from that copy has promised a membership the room has not agreed to.
	// The answer comes from a server the room counts, which is what makes the refusal the client
	// can act on reachable when the admission is not there.
	var incompatible error
	// A server that answered is a server that looked. Its refusal - banned, invited nowhere,
	// admitted by no rule the room has - is the reason the join did not happen, and the client
	// can only act on a reason it is given: "no server in the list could complete the join"
	// sends somebody who was banned to look for a room that is right there.
	var refused *matrix.Error
	for _, server := range joinCandidates(roomID, via) {
		if server == s.serverName {
			// Asking for a template from ourselves would be pointless, but the attempt is
			// not: this server is one of the servers the room listens on, so it can build
			// the join from its own copy of the room and offer it to the peers that hold it.
			event, err := s.joinThroughThisServer(ctx, transport, roomID, userID)
			if err == nil {
				return event, nil
			}
			log.Printf("federation: joining %s as a server already in it failed: %v", roomID, err)
			var peerErr *matrix.Error
			if errors.As(err, &peerErr) && peerErr.Code != "" && peerErr.Code != matrix.ErrCodeNotFound {
				refused = peerErr
			}
			continue
		}
		event, err := s.joinViaServer(ctx, transport, roomID, userID, server)
		if err == nil {
			return event, nil
		}
		log.Printf("federation: joining %s through %s failed: %v", roomID, server, err)
		var peerErr *matrix.Error
		if errors.As(err, &peerErr) && peerErr.Code != "" && peerErr.Code != matrix.ErrCodeNotFound {
			refused = peerErr
		}
		if matrix.HasCode(err, matrix.ErrCodeIncompatibleRoomVer) {
			incompatible = err
			if version, _ := roomVersionOfError(err); version != "" {
				log.Printf("federation: %s says %s is room version %s; this server speaks %s",
					server, roomID, version, strings.Join(s.roomVersions, ", "))
			}
		}
	}
	if incompatible != nil {
		return nil, incompatible
	}
	if refused != nil {
		return nil, refused
	}
	return nil, matrix.ErrNotFound("no server in the list could complete the join of %s for %s", roomID, userID)
}

// joinCandidates names the servers a join is asked of. The client named the servers it wanted
// tried, and a server that quietly adds the room's own server answers a question the client asked
// somebody else: fail-over between servers is only meaningful if the list is the list. With no
// servers named at all, the room's own server is the obvious place to ask.
func joinCandidates(roomID string, via []string) []string {
	if len(via) == 0 {
		return joinServers(roomID, nil)
	}
	return joinServers("", via)
}

func (s *Service) joinViaServer(ctx context.Context, transport *federation.Transport, roomID, userID, server string) (*persist.Event, error) {
	if matrix.DomainOf(roomID) == s.serverName {
		// Only a room this server hosts is joined without asking anyone. A room id names
		// the server that made the room, and holding a copy of it - which is what a past
		// join, a ban or an invite leaves behind - does not make this server authoritative:
		// a user who was thrown out of a remote room and invited back has to go through the
		// room's own server again, and refusing because "we have seen this room before" is
		// refusing the second half of every federated leave-and-rejoin.
		return nil, fmt.Errorf("this server hosts room %s itself", roomID)
	}
	proposal, version, err := s.requestMakeJoin(ctx, transport, roomID, userID, server)
	if err != nil {
		return nil, err
	}
	if !s.roomVersionIsSupported(version) {
		return nil, fmt.Errorf("the room is version %s, which this server cannot speak", version)
	}
	// The proposal is a shape, not an authorisation: the sender, the state key and the
	// room are this server's to state, whatever a peer put in the template it sent back.
	proto, _ := proposal["event"].(matrix.Object)
	if proto == nil {
		return nil, fmt.Errorf("%s answered make_join with no event template", server)
	}
	depth := numberField(proto, "depth")
	prevIDs := idsOfPDUList(proto, "prev_events")
	authIDs := idsOfPDUList(proto, "auth_events")

	content := matrix.Object{
		"membership": matrix.MembershipJoin,
	}
	if name := s.displayName(ctx, userID); name != "" {
		content["display_name"] = name
	}
	if avatar := s.avatarURL(ctx, userID); avatar != "" {
		content["avatar_url"] = avatar
	}
	// A room that admits people through another room is entered on somebody's word, and the
	// room's server named that somebody in the template. The claim has to travel inside the
	// signed event: it is the only way the room's server can see who vouched for the joiner,
	// and the joiner's own server cannot invent one - that is how a stranger becomes a member
	// of a room that never let them in.
	if template := objectOf(proto, "content"); template != nil {
		if authorised, _ := template["join_authorised_via_users_server"].(string); authorised != "" {
			content["join_authorised_via_users_server"] = authorised
		}
	}
	key := userID
	draft := &EventDraft{
		RoomID:   roomID,
		Type:     matrix.TypeMember,
		StateKey: &key,
		Sender:   userID,
		Content:  matrix.Encode(content),
	}
	// The join is judged before it is made. A room that has banned this user, or that
	// wants an invite this user does not have, says so here rather than after an event
	// has been signed and shipped.
	if problem := s.joinAllowed(ctx, roomID, userID, prevIDs, authIDs, version); problem != "" {
		return nil, fmt.Errorf("%s", problem)
	}
	packet, err := s.buildRemotePDU(draft, version, s.NowMS(), depth, prevIDs, authIDs,
		objectOf(proto, "unsigned"))
	if err != nil {
		return nil, err
	}
	if err := s.SignAndIdentify(ctx, packet); err != nil {
		return nil, err
	}
	state, authChain, err := s.sendJoin(ctx, transport, server, roomID, packet)
	if err != nil {
		return nil, err
	}
	if err := s.storeJoinedRoom(ctx, roomID, version, packet, state, authChain); err != nil {
		return nil, err
	}
	return s.db.EventRepo.Get(packet.eventID)
}

// joinThroughThisServer completes the join of one of this server's own users in a room this server
// is in but does not host. The shape of the join does not have to be asked for: this server holds a
// copy of the graph and the state, and is one of the servers the room counts as listening. What is
// still the room's own decision is whether the signed join is taken, so that part is asked - of a
// server in the room rather than of the one its id happens to name. A server that is in a room and
// refuses its own user because it would have to ask a peer turns every room into a single point of
// failure at the server its id names.
func (s *Service) joinThroughThisServer(ctx context.Context, transport *federation.Transport, roomID, userID string) (*persist.Event, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s, so it cannot finish a join there", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	version := RoomVersionOf(room)
	if !s.roomVersionIsSupported(version) {
		return nil, fmt.Errorf("the room is version %s, which this server cannot speak", version)
	}
	// A room that admits people through another room is entered on the word of a member who may
	// invite, and this server can say whose word that is for the members it holds state for. The
	// name goes in the template and so in the signed event; the server asked to take the join
	// still decides whether it believes it.
	authorised := s.joinAuthoriser(ctx, roomID, userID)
	prevIDs, authIDs, depth, unsigned, err := s.joinShapeHere(ctx, roomID, userID, version, authorised)
	if err != nil {
		return nil, err
	}
	content := matrix.Object{"membership": matrix.MembershipJoin}
	if name := s.displayName(ctx, userID); name != "" {
		content["display_name"] = name
	}
	if avatar := s.avatarURL(ctx, userID); avatar != "" {
		content["avatar_url"] = avatar
	}
	if authorised != "" {
		content["join_authorised_via_users_server"] = authorised
	}
	key := userID
	draft := &EventDraft{
		RoomID:   roomID,
		Type:     matrix.TypeMember,
		StateKey: &key,
		Sender:   userID,
		Content:  matrix.Encode(content),
	}
	if problem := s.joinAllowed(ctx, roomID, userID, prevIDs, authIDs, version); problem != "" {
		return nil, fmt.Errorf("%s", problem)
	}
	packet, err := s.buildRemotePDU(draft, version, s.NowMS(), depth, prevIDs, authIDs, unsigned)
	if err != nil {
		return nil, err
	}
	if err := s.SignAndIdentify(ctx, packet); err != nil {
		return nil, err
	}
	var lastErr error
	for _, server := range s.roomPeers(ctx, roomID) {
		state, authChain, err := s.sendJoin(ctx, transport, server, roomID, packet)
		if err != nil {
			lastErr = err
			log.Printf("federation: offering the join of %s for %s to %s failed: %v", roomID, userID, server, err)
			continue
		}
		if err := s.storeJoinedRoom(ctx, roomID, version, packet, state, authChain); err != nil {
			return nil, err
		}
		return s.db.EventRepo.Get(packet.eventID)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no server in the room would take the join")
	}
	return nil, lastErr
}

// roomPeers are the servers this server can deal with for a room it holds: the servers its members
// live on and the server the room id names. This server is not among them - a server does not need
// to telephone itself to be told about a room it is already in.
func (s *Service) roomPeers(ctx context.Context, roomID string) []string {
	servers, err := s.destinationsOfRoomIgnoringACL(ctx, roomID)
	if err != nil {
		servers = nil
	}
	out := make([]string, 0, len(servers)+1)
	if origin := matrix.DomainOf(roomID); origin != s.serverName {
		out = append(out, origin)
	}
	for _, server := range servers {
		if server == s.serverName {
			continue
		}
		known := false
		for _, seen := range out {
			if seen == server {
				known = true
				break
			}
		}
		if !known {
			out = append(out, server)
		}
	}
	return out
}

// requestMakeJoin asks a server for the join template of a room. Version 2 of the
// endpoint is asked for first because it is the one that can omit the member list; a
// server that only knows version 1 answers that instead, and the answer is the same
// shape.
// requestMakeJoin asks a server for the join event template - the room version and the
// prev_events and auth_events that attach to the room's graph.
//
// Version 1 is asked for first. Version 2 exists so a server can be told which room
// versions the joining server speaks, and version 1 carries the same ver= parameters
// anyway; asking for the newer endpoint first costs a round trip to every server that
// has not implemented it, and some deployments treat the unrecognised request as an
// error rather than a miss.
func (s *Service) requestMakeJoin(ctx context.Context, transport *federation.Transport, roomID, userID, server string) (matrix.Object, string, error) {
	escapedRoom := url.PathEscape(roomID)
	escapedUser := url.PathEscape(userID)
	for _, version := range []string{"v1", "v2"} {
		query := url.Values{}
		for _, supported := range s.roomVersions {
			query.Add("ver", supported)
		}
		path := fmt.Sprintf("/_matrix/federation/%s/make_join/%s/%s", version, escapedRoom, escapedUser)
		body, err := transport.Get(ctx, server, path, query)
		if err != nil {
			if isUnrecognized(err) && version == "v1" {
				continue
			}
			return nil, "", err
		}
		roomVersion, _ := body["room_version"].(string)
		if roomVersion == "" {
			return nil, "", fmt.Errorf("%s answered make_join with no room version", server)
		}
		return body, roomVersion, nil
	}
	return nil, "", fmt.Errorf("%s serves no make_join this server understands", server)
}

// sendJoin hands the signed join event to the server that proposed it and takes back
// the room's state. Version 2 is asked for first: it answers with the servers in the
// room as well as the state, and it is the version that can leave the member list out.
func (s *Service) sendJoin(ctx context.Context, transport *federation.Transport, server, roomID string, packet *pdu) ([]matrix.Object, []matrix.Object, error) {
	for _, version := range []string{"v2", "v1"} {
		path := fmt.Sprintf("/_matrix/federation/%s/send_join/%s/%s",
			version, url.PathEscape(roomID), url.PathEscape(packet.eventID))
		body, err := transport.Put(ctx, server, path, packet.object)
		if err != nil {
			if isUnrecognized(err) && version == "v2" {
				continue
			}
			return nil, nil, err
		}
		return pduArray(body, "state"), pduArray(body, "auth_chain", "auth_events"), nil
	}
	return nil, nil, fmt.Errorf("%s serves no send_join this server understands", server)
}

// storeJoinedRoom writes the room this join reached: the room row, the state the other
// server handed over, and this server's own join event.
//
// The state events arrive with a stream position because this server needs one for
// every event, but they are marked as not belonging to the timeline: they are another
// server's history, and a client reading the room must not see a rename from a year ago
// arrive as the newest thing in it.
func (s *Service) storeJoinedRoom(ctx context.Context, roomID, version string, packet *pdu, state, authChain []matrix.Object) error {
	// The room row exists before its state is written, because the state's side effects -
	// the join rule, the creator - are written onto it.
	if err := s.db.RoomRepo.Create(&persist.Room{
		ID:          roomID,
		RoomVersion: version,
		JoinRule:    matrix.JoinRulesInvite,
	}); err != nil && !persist.IsDuplicate(err) {
		return matrix.ErrInternal(err)
	}
	if room, err := s.db.RoomRepo.Get(roomID); err == nil && RoomVersionOf(room) != version {
		// The join handshake is the moment this server learns the room's version from the
		// server that hosts it, and that outranks whatever was guessed when the invite
		// arrived. A room kept under the wrong version is hashed and signed wrongly from then
		// on, and every event this server makes in it is refused by the servers that have the
		// version right - which reads as a signature problem, not as a wrong row.
		log.Printf("federation: %s is room version %s, not %s as this server had recorded", roomID, version, RoomVersionOf(room))
		room.RoomVersion = version
		if err := s.db.RoomRepo.Save(room); err != nil {
			return matrix.ErrInternal(err)
		}
	}
	for _, event := range append(append([]matrix.Object{}, state...), authChain...) {
		id := federation.EventField(event, "event_id")
		if id == "" {
			derived, err := eventcrypto.EventID(event, version)
			if err != nil {
				continue
			}
			id = derived
		}
		if _, err := s.db.EventRepo.Get(id); err == nil {
			continue
		}
		judged, problem := s.judgeIncomingPDU(ctx, event, version)
		if problem != "" {
			// A state event this server cannot verify is left out rather than obeyed.
			// The join still stands: the specification is explicit that an event whose
			// signature cannot be checked does not stop a room being joined, because a
			// server that cannot fetch a key would otherwise be locked out of every room
			// that uses one.
			log.Printf("federation: the state of %s carries %s, which this server cannot verify: %s",
				roomID, id, problem)
			continue
		}
		if err := s.storeIncomingPDU(ctx, judged, id, "", true); err != nil {
			return err
		}
	}
	// The join event itself does belong to the timeline: it is the moment this server
	// joined, and it happened here.
	return s.storeIncomingPDU(ctx, packet.object, packet.eventID, "", false)
}

// buildRemotePDU assembles an event whose place in the graph was decided somewhere
// else. BuildPDU picks prev_events and auth_events from this server's own graph, which
// is the right answer for a room hosted here and the wrong one for a room lived on
// another server: the join has to attach to the graph the other server has, or the two
// histories never meet.
func (s *Service) buildRemotePDU(draft *EventDraft, version string, originServerTS, depth int64, prevIDs, authIDs []string, unsigned matrix.Object) (*pdu, error) {
	object := matrix.Object{
		"type":             draft.Type,
		"room_id":          draft.RoomID,
		"sender":           draft.Sender,
		"origin_server_ts": originServerTS,
		"content":          matrix.MustParseObject(draft.Content),
		"depth":            depth,
		"prev_events":      pduIDList(prevIDs),
		"auth_events":      pduIDList(authIDs),
	}
	// What the room's own server annotated the template with travels with it. "unsigned"
	// is not signed and not hashed, so carrying it is not this server vouching for a peer's
	// words - it is the room saying what the membership was before, which only the server
	// holding the room is in a position to know.
	if len(unsigned) > 0 {
		object["unsigned"] = unsigned
	}
	if !usesDerivedEventIDs(version) {
		object["origin"] = s.serverName
		object["event_id"] = matrix.NewEventID()
	}
	if draft.StateKey != nil {
		object["state_key"] = *draft.StateKey
	}
	return &pdu{object: object, version: version, prevIDs: prevIDs, authIDs: authIDs}, nil
}

// containsServer reports whether a list of server names already names a server.
func containsServer(servers []string, server string) bool {
	for _, name := range servers {
		if name == server {
			return true
		}
	}
	return false
}

// roomVersionOfError returns the room version a peer named in a refusal, which the
// specification carries for M_INCOMPATIBLE_ROOM_VERSION, and the status the refusal
// arrived with.
func roomVersionOfError(err error) (string, int) {
	var merr *matrix.Error
	if !errors.As(err, &merr) {
		return "", 0
	}
	version, _ := merr.Extra["room_version"].(string)
	return version, merr.Status
}

// joinServers orders the servers a join may be attempted through: the ones the client
// named, in order, and then the server the room id itself names. The room id's server is
// last rather than first on purpose - it is the server that made the room, which is also
// the one most likely to have been deleted or to be refusing this server.
func joinServers(roomID string, via []string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(server string) {
		server = strings.TrimSpace(server)
		if server == "" || seen[server] {
			return
		}
		seen[server] = true
		out = append(out, server)
	}
	for _, server := range via {
		add(server)
	}
	add(matrix.DomainOf(roomID))
	return out
}

// joinAllowed is the local half of the join decision: the rules this server can read
// without the other server's help. The banned user is the case that matters, because a
// ban the joining server ignores is a ban the room's own servers have to enforce after
// the fact, with an event already in the graph.
func (s *Service) joinAllowed(ctx context.Context, roomID, userID string, prevIDs, authIDs []string, version string) string {
	members, err := s.db.MembershipRepo.Get(roomID, userID)
	if err == nil && members.Membership == matrix.MembershipBan {
		return fmt.Sprintf("you are banned from %s", roomID)
	}
	if _, err := s.db.RoomRepo.Get(roomID); err != nil {
		// This server holds no copy of the room at all, so it holds no rule either. The
		// room's own answer arrives with the join, and the server that answered make_join
		// is the one that decides. A room this server has never heard of must not be
		// pre-judged by the default it would fall back to: reading "there is no join rule
		// here" as "the rule is invite" turns the first join of every remote room into a
		// refusal, which is what it did.
		return ""
	}
	rule, err := s.db.StateRepo.JoinRule(roomID)
	if err != nil || rule == "" {
		// Nothing local to read: the state comes with the join, and the server that
		// answered make_join is the one that decides.
		return ""
	}
	if rule == matrix.JoinRulesInvite {
		if members == nil || members.Membership != matrix.MembershipInvite {
			if !s.IsAdmin(ctx, userID) {
				return fmt.Sprintf("room %s needs an invite to join", roomID)
			}
		}
	}
	return ""
}

// roomVersionIsSupported reports whether a room version is one this server can speak.
func (s *Service) roomVersionIsSupported(version string) bool {
	for _, supported := range s.roomVersions {
		if supported == version {
			return true
		}
	}
	return false
}

// pduArray reads one or more arrays of PDUs out of a response body.
func pduArray(body matrix.Object, fields ...string) []matrix.Object {
	var out []matrix.Object
	for _, field := range fields {
		list, _ := body[field].([]any)
		for _, entry := range list {
			if event, ok := entry.(matrix.Object); ok {
				out = append(out, event)
			}
		}
	}
	return out
}

// isUnrecognized reports whether a peer answered that it does not serve the endpoint
// asked for, which is the difference between "try the older version" and "give up".
func isUnrecognized(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	return strings.Contains(text, "M_UNRECOGNIZED") || strings.Contains(text, "status 404") ||
		strings.Contains(text, "404")
}

// joinShapeHere is the join template this server would hand a peer for a room it holds: where a
// new event attaches to the graph, which state authorises it, and the membership the join
// replaces. The server answering make_join and a server that is in a room but does not host it
// and finishes the join itself need the same shape, and two copies of that arithmetic drifting
// apart is a join one server signs and every other one refuses.
func (s *Service) joinShapeHere(ctx context.Context, roomID, userID, version, authorised string) ([]string, []string, int64, matrix.Object, error) {
	var (
		prevIDs  []string
		authIDs  []string
		depth    int64
		unsigned matrix.Object
	)
	if err := s.db.Tx(func(tx *gorm.DB) error {
		events := persist.NewEventRepo(tx)
		leaves, err := events.ForwardExtremities(roomID, maxPrevEvents)
		if err != nil {
			return matrix.ErrInternal(err)
		}
		prevIDs = leaves
		depth, err = depthAfter(events, roomID, prevIDs)
		if err != nil {
			return err
		}
		key := userID
		// The membership the room holds for this user is what the join will replace, and
		// the replacement is only legible next to what came before it: a client shown a
		// join cannot tell an accepted invitation from a knock that was let in. The room's
		// server is the one that knows, so it puts that in the shape it hands over - in
		// "unsigned", which is not signed and is not part of what the joining server hashes.
		if previous, err := persist.NewStateRepo(tx).Get(roomID, matrix.TypeMember, userID); err == nil {
			unsigned = matrix.Object{}
			var before matrix.Object
			if err := matrix.DecodeContent(matrix.RawMessage(previous.Content), &before); err == nil && len(before) > 0 {
				unsigned["prev_content"] = before
			}
			if previous.Sender != "" {
				unsigned["prev_sender"] = previous.Sender
			}
			if len(unsigned) == 0 {
				unsigned = nil
			}
		} else if !persist.IsNotFound(err) {
			return matrix.ErrInternal(err)
		}
		// The membership is part of the draft because the membership decides which state
		// the auth selection names: a join has to cite the room's join rule, and a draft
		// without content reads as a membership change of no kind, which quietly answers
		// make_join with an auth set the joining server's own rules then refuse.
		// The witness belongs in the auth events. A join that names the user who vouched for it
		// cites that user's membership as part of what authorises it, and a chain built as though
		// the claim were not there is a chain the room's own rules refuse the next time they are
		// read - which is not at make_join, but later, when the join is cited as authority for
		// something else.
		draft := matrix.Object{"membership": matrix.MembershipJoin}
		if authorised != "" {
			draft["join_authorised_via_users_server"] = authorised
		}
		authIDs, err = selectAuthEvents(persist.NewStateRepo(tx), &EventDraft{
			RoomID:   roomID,
			Type:     matrix.TypeMember,
			StateKey: &key,
			Sender:   userID,
			Content:  matrix.Encode(draft),
		}, version)
		return err
	}); err != nil {
		return nil, nil, 0, nil, err
	}
	return prevIDs, authIDs, depth, unsigned, nil
}

// ServeMakeJoin answers a peer's make_join for a room this server hosts: the room
// version, and the shape of the join event that would attach to this server's graph.
//
// What is answered is a template, not an authorisation - the event comes back signed by
// the joining server, and it is the send_join that decides whether the room takes it. But
// the prev_events named here are what the join will cite, so a server that answers with
// a stale graph is the reason two servers then hold two histories that have to be
// resolved later.
func (s *Service) ServeMakeJoin(ctx context.Context, roomID, userID, peer string, versions []string) (matrix.Object, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	if acl, err := s.roomServerACL(ctx, roomID); err != nil {
		return nil, err
	} else if !federation.ServerAllowed(acl, peer) {
		return nil, matrix.ErrForbidden("this room excludes %s", peer)
	}
	version := RoomVersionOf(room)
	if len(versions) > 0 {
		agreed := false
		for _, wanted := range versions {
			if wanted == version {
				agreed = true
			}
		}
		if !agreed {
			// The specification names this refusal M_INCOMPATIBLE_ROOM_VERSION and carries the
			// room's version with it, because "no version in common" is only useful once the
			// other side knows which version was missing.
			err := matrix.NewError(matrix.ErrCodeIncompatibleRoomVer, http.StatusBadRequest,
				fmt.Sprintf("this room is version %s and %s speaks none of the versions offered", version, peer))
			err.Extra = matrix.Object{"room_version": version}
			return nil, err
		}
	}
	// A room joined through another room is joined on the word of somebody who is already in it
	// and may invite. The room's own server is the only place that can say whose word that is,
	// and a joining server left without a name cannot make the join at all: refusing at send_join
	// would be refusing after the other server had already signed an event and spent its attempt,
	// with nothing to show for failing over to another server.
	authorised := s.joinAuthoriser(ctx, roomID, userID)
	prevIDs, authIDs, depth, unsigned, err := s.joinShapeHere(ctx, roomID, userID, version, authorised)
	if err != nil {
		return nil, err
	}
	template := matrix.Object{
		"room_id":          roomID,
		"type":             matrix.TypeMember,
		"sender":           userID,
		"state_key":        userID,
		"origin_server_ts": s.NowMS(),
		"depth":            depth,
		"prev_events":      pduIDList(prevIDs),
		"auth_events":      pduIDList(authIDs),
		// The membership is part of the shape. A template without content is not a
		// join waiting to be signed but a shape a peer cannot finish: gomatrixserverlib
		// marshals the content member of its proto event and fails on an empty one, so
		// an answer that leaves it out cannot be signed at all.
		"content": func() matrix.Object {
			content := matrix.Object{"membership": matrix.MembershipJoin}
			if authorised != "" {
				content["join_authorised_via_users_server"] = authorised
			}
			return content
		}(),
	}
	if unsigned != nil {
		template["unsigned"] = unsigned
	}
	return matrix.Object{"room_version": version, "event": template}, nil
}

// joinAuthoriser names a user this server can vouch for to authorise a restricted join: one of its
// own users, joined to the room, able to send invites in it, and joined to one of the rooms whose
// membership the room admits people through. Any such user will do - the room has said that
// membership there is what admits people, and the level is what makes a member's word count.
func (s *Service) joinAuthoriser(ctx context.Context, roomID, joiningUser string) string {
	allowed := s.roomsThatAdmit(ctx, roomID)
	if len(allowed) == 0 {
		return ""
	}
	levels := s.powerLevels(ctx, roomID)
	creator := ""
	if room, err := s.db.RoomRepo.Get(roomID); err == nil {
		creator = room.Creator
	}
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return ""
	}
	for _, member := range members {
		user := member.UserID
		if user == "" || user == joiningUser || matrix.DomainOf(user) != s.serverName {
			continue
		}
		if power.Level(levels, user, creator) < levels.Invite {
			continue
		}
		rooms, err := s.db.MembershipRepo.RoomsFor(user, matrix.MembershipJoin)
		if err != nil {
			continue
		}
		for _, id := range rooms {
			for _, want := range allowed {
				if id == want && s.canReadMembershipOf(want) {
					return user
				}
			}
		}
	}
	// Nobody else qualifies, so the joiner may vouch for themself. The rule admits people who
	// belong to one of the rooms on its allow list, and "I belong to one of them" is a claim the
	// room can check against its own list of members. A witness is named in preference to this,
	// because "who let this person in" is better answered by the member who invited them than by
	// the joiner; but a server that names nobody at all is refused by the room version's rules,
	// which read a join with no authoriser as a join nobody let through.
	if admits, err := s.db.MembershipRepo.RoomsFor(joiningUser, matrix.MembershipJoin); err == nil {
		for _, id := range admits {
			for _, want := range allowed {
				if id == want && s.canReadMembershipOf(want) {
					return joiningUser
				}
			}
		}
	}
	return ""
}

// roomsThatAdmit lists the rooms whose membership admits outsiders into a room, for the join rules
// that work that way. An empty answer means the room does not admit people through other rooms.
func (s *Service) roomsThatAdmit(ctx context.Context, roomID string) []string {
	raw, err := s.db.StateRepo.Content(roomID, matrix.TypeJoinRules, "")
	if err != nil {
		return nil
	}
	var content matrix.JoinRulesContent
	if err := matrix.DecodeContent(raw, &content); err != nil {
		return nil
	}
	if content.JoinRule != matrix.JoinRulesRestricted && content.JoinRule != matrix.JoinRulesKnockRestricted {
		return nil
	}
	var rooms []string
	for _, rule := range content.Allow {
		rooms = append(rooms, rule.RoomID)
	}
	return rooms
}

// ServeSendJoin takes a join event a peer made for a room this server hosts, decides
// whether the room accepts it, and answers with the room's state - which is what makes
// the join real: the joining server cannot describe a room it has never been told about,
// and a server that cannot describe a room is not in it.
//
// The event's shape is checked before anything else, because the endpoint has exactly one
// job: a membership event of the right kind, for the sender itself, under the id the path
// names. Everything else is a misuse of the endpoint and is answered 400 - including the
// join for a different user, which is how one server gets to speak as another's user.
func (s *Service) ServeSendJoin(ctx context.Context, roomID, eventID string, event matrix.Object, peer string, v2, omitMembers bool) (matrix.Object, error) {
	room, err := s.db.RoomRepo.Get(roomID)
	if err != nil {
		if persist.IsNotFound(err) {
			return nil, matrix.ErrNotFound("this server holds no room %s", roomID)
		}
		return nil, matrix.ErrInternal(err)
	}
	version := RoomVersionOf(room)
	sender, shape := membershipForEndpoint(event, eventID, roomID, "send_join", matrix.MembershipJoin)
	if shape != nil {
		return nil, shape
	}
	if usesDerivedEventIDs(version) {
		derived, err := eventcrypto.EventID(event, version)
		if err != nil {
			return nil, matrix.ErrInvalidRequest("the event cannot be named: %v", err)
		}
		if derived != eventID {
			return nil, matrix.ErrInvalidRequest("the path names %s and the event hashes to %s", eventID, derived)
		}
		event = withEventID(event, eventID)
	}

	judged, problem := s.judgeIncomingPDU(ctx, event, version)
	if problem != "" {
		return nil, matrix.ErrUnauthorized("the event sent for %s is not one this server can accept: %s", roomID, problem)
	}
	if problem := s.joinIsAuthorised(roomID, judged, version); problem != "" {
		return nil, matrix.ErrForbidden("%s", problem)
	}
	if problem := s.restrictedJoinIsAdmitted(ctx, roomID, judged, peer); problem != "" {
		return nil, matrix.ErrForbidden("%s", problem)
	}
	// What the answer describes is the room as it was before this join, so both the state
	// and the list of servers in the room are read before the join is applied. Answering
	// from the state afterwards hands the joining server a description of the join it just
	// asked about, and a server list that counts the joiner as already resident.
	stateBefore, err := s.roomStateForTransport(ctx, roomID, version)
	if err != nil {
		return nil, err
	}
	serversBefore, err := s.destinationsOfRoomIgnoringACL(ctx, roomID)
	if err != nil {
		return nil, err
	}
	// This server is in the room - it hosts it - and says so. The list is how the joining
	// server learns who else it can ask, and leaving out the server it is talking to
	// contradicts the connection the answer came down.
	servers := append([]string{s.serverName}, serversBefore...)
	if err := s.storeIncomingPDU(ctx, judged, eventID, "", false); err != nil {
		return nil, err
	}
	// The join has to be carried to the rest of the room. Answering the joining server alone
	// leaves the room's other servers with a member they have never heard of: they leave the
	// new server out of their fan-out, their member lists disagree, and the room is left with
	// copies of itself that do not agree about who is in it.
	if stored, err := s.db.EventRepo.Get(eventID); err == nil {
		s.queueEventForPeersExcept(ctx, stored, version, peer)
	} else {
		log.Printf("federation: the join of %s in %s was accepted but cannot be read back to relay it: %v",
			federation.EventField(judged, "state_key"), roomID, err)
	}
	state, err := s.roomStateForTransport(ctx, roomID, version)
	if err != nil {
		return nil, err
	}
	if !v2 {
		chain, err := s.authChainForRoom(roomID, version)
		if err != nil {
			return nil, err
		}
		return matrix.Object{"state": state, "auth_chain": chain}, nil
	}
	answer := matrix.Object{
		"origin":          s.serverName,
		"state":           state,
		"servers_in_room": servers,
	}
	// A server that asked for partial state is saying the room's member list is not what
	// it came for: in a room of tens of thousands of users that list is most of the state
	// and none of what authorises the join. The flag answers "were memberships left out",
	// which is true whenever partial state was asked for - even in a small room whose every
	// member happens to be a name that stays - and a receiver that read "no" would believe
	// it holds the room's whole membership list, the one conclusion a partial answer cannot
	// license.
	if omitMembers {
		answer["state"] = s.partialStateOf(ctx, roomID, stateBefore, sender)
		answer["members_omitted"] = true
	}
	return answer, nil
}

// partialStateOf trims a room's state to what a server that asked for partial state is
// given: every event that is not a membership, the joining user's membership as the room
// already held it, and - only when the room has nothing else to show itself by - the
// memberships of the room's heroes.
//
// The heroes are the names a client shows for a room that has no name and no canonical
// alias, which is exactly when they are kept: a titled room is described by its title, and
// holding on to five memberships anyway is sending state nobody asked for.
func (s *Service) partialStateOf(ctx context.Context, roomID string, state []any, joiner string) []any {
	keep := map[string]bool{joiner: true}
	named := false
	for _, typ := range []string{matrix.TypeName, matrix.TypeCanonicalAlias} {
		if _, err := s.db.StateRepo.Get(roomID, typ, ""); err == nil {
			named = true
			break
		}
	}
	if !named {
		for hero := range s.roomHeroes(ctx, roomID) {
			keep[hero] = true
		}
	}
	trimmed, _ := withoutOtherMemberships(state, keep)
	return trimmed
}

// joinIsAuthorised reads the room's rules against this server's own state of the room,
// which is the only state a room's host can enforce about a stranger's join. The rules
// are read without the auth-event-set rule: the peer chose those events against the state
// it had when it asked, and this server's state may have moved since.
func (s *Service) joinIsAuthorised(roomID string, event matrix.Object, version string) string {
	graph := authcheck.NewGraph(version)
	complete := map[string]bool{}
	state, err := s.readCurrentState(roomID, graph, complete)
	if err != nil {
		return err.Error()
	}
	ev := pduEvent(event)
	graph.Add(ev)
	if problem := authcheck.Rules(graph, state, ev); problem != nil {
		return problem.Error()
	}
	return ""
}

// canReadMembershipOf reports whether this server may treat "is this user in that room" as a fact.
// Either it hosts the room, or it still has a member there. A copy of a room this server has left is
// frozen: nothing arrives to update it, and a rule read from it is a rule read from the day the last
// member walked out. Answering from that copy is how a server ends up certain about a room that has
// moved on without it.
func (s *Service) canReadMembershipOf(roomID string) bool {
	if _, err := s.db.RoomRepo.Get(roomID); err != nil {
		return false
	}
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return false
	}
	for _, member := range members {
		if matrix.DomainOf(member.UserID) == s.serverName {
			return true
		}
	}
	return false
}

// A claim naming somebody other than the joiner does not lift the requirement on the joiner. The
// room's rule is read against the joiner's own membership: Synapse, the server every other server
// has to agree with in the end, refuses a join by a person who belongs to none of the rooms on the
// allow list even when the event names a member of one of them who may invite. A server that
// admitted what the room's own server refuses is a room holding two answers about the same join -
// and the join is unmade the moment a server with a fuller view of the room reads it.

func (s *Service) restrictedJoinIsAdmitted(ctx context.Context, roomID string, event matrix.Object, peer string) string {
	content := objectOf(event, "content")
	authorised, _ := content["join_authorised_via_users_server"].(string)
	joiner, _ := event["sender"].(string)
	allowed := s.roomsThatAdmit(ctx, roomID)
	if len(allowed) == 0 {
		// The room does not admit people through other rooms. A witness named on such a join is
		// decoration on a join the room's own rules already let through.
		return ""
	}
	joinerRooms, err := s.db.MembershipRepo.RoomsFor(joiner, matrix.MembershipJoin)
	if err != nil {
		return fmt.Sprintf("the membership of %s cannot be read to judge the join", joiner)
	}
	hosted, admitted := 0, false
	for _, room := range allowed {
		if !s.canReadMembershipOf(room) {
			continue
		}
		hosted++
		for _, id := range joinerRooms {
			if id == room {
				admitted = true
			}
		}
	}
	// An invitation is its own admission. The room invited this user, which is the room saying
	// it wants them; requiring them to also belong to one of the rooms the rule reads would
	// contradict the room's own answer.
	if current, err := s.db.MembershipRepo.Get(roomID, joiner); err == nil &&
		(current.Membership == matrix.MembershipInvite || current.Membership == matrix.MembershipJoin) {
		admitted = true
	}
	if hosted > 0 && !admitted {
		return fmt.Sprintf("%s is not a member of any of the rooms %s admits people through", joiner, roomID)
	}
	if authorised == "" {
		// The join names no witness. Whether that is allowed is the join rule's business, and the
		// rules have already been read against this server's own state of the room.
		return ""
	}
	domain := matrix.DomainOf(authorised)
	if domain != s.serverName && domain != peer {
		return fmt.Sprintf("the join is authorised by %s, a user of neither this server nor %s", authorised, peer)
	}
	witnessRooms, err := s.db.MembershipRepo.RoomsFor(authorised, matrix.MembershipJoin)
	if err != nil {
		return fmt.Sprintf("the join is authorised by %s, whose membership this server cannot read", authorised)
	}
	for _, id := range witnessRooms {
		for _, want := range allowed {
			if id == want && s.canReadMembershipOf(want) {
				return ""
			}
		}
	}
	return fmt.Sprintf("the join is authorised by %s, which is not a member of any of the rooms %s admits people through", authorised, roomID)
}

// withoutOtherMemberships drops the membership events of a state list except those named
// in keep, and reports whether anything was dropped.
func withoutOtherMemberships(state []any, keep map[string]bool) ([]any, bool) {
	out := make([]any, 0, len(state))
	omitted := false
	for _, entry := range state {
		event, ok := entry.(matrix.Object)
		if !ok {
			out = append(out, entry)
			continue
		}
		if federation.EventField(event, "type") == matrix.TypeMember &&
			!keep[federation.EventField(event, "state_key")] {
			omitted = true
			continue
		}
		out = append(out, entry)
	}
	return out, omitted
}

// heroesPerRoom is how many names a partial-state answer keeps when the room's power
// levels do not name enough of them.
const heroesPerRoom = 5

// heroPowerLevel is the level from which a user runs the room - the same 50 that gates a
// kick, a ban or a state change.
const heroPowerLevel = 50

// roomHeroes names the users whose membership survives a partial-state answer: the room's
// creator, anyone the power levels raised to 50 or above, everyone when the room's default
// level is 50 or above, and enough of the remaining joined users to reach five names.
//
// Partial state exists because a large room is mostly memberships, but a client that has
// to show a room shows it by its members: the room's face is five of them. Dropping every
// membership would answer a join with a room that has nobody in it, which is why the names
// a client would display stay in the answer.
func (s *Service) roomHeroes(ctx context.Context, roomID string) map[string]bool {
	heroes := map[string]bool{}
	if raw, err := s.db.StateRepo.Content(roomID, matrix.TypeCreate, ""); err == nil {
		content := matrix.Object{}
		if json.Unmarshal([]byte(raw), &content) == nil {
			if creator, ok := content["creator"].(string); ok && creator != "" {
				heroes[creator] = true
			}
		}
	}
	levels := s.powerLevels(ctx, roomID)
	members, err := s.db.MembershipRepo.Members(roomID, matrix.MembershipJoin)
	if err != nil {
		return heroes
	}
	plain := make([]string, 0, len(members))
	for _, member := range members {
		level, explicit := levels.Users[member.UserID]
		switch {
		case explicit && level >= heroPowerLevel:
			heroes[member.UserID] = true
		case !explicit && levels.UsersDefault >= heroPowerLevel:
			heroes[member.UserID] = true
		case !explicit:
			plain = append(plain, member.UserID)
		}
	}
	sort.Strings(plain)
	for _, user := range plain {
		if len(heroes) >= heroesPerRoom {
			break
		}
		heroes[user] = true
	}
	return heroes
}

// roomStateForTransport reads a room's whole state in the form the room version defines,
// which is what a join answer and a state query both carry.
func (s *Service) roomStateForTransport(ctx context.Context, roomID, version string) ([]any, error) {
	_ = ctx
	rows, err := s.db.StateRepo.Events(roomID, nil)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		// A soft-failed state event is not this room's state, whichever table holds it.
		if row.SoftFailed {
			continue
		}
		pdu, err := transportPDU(row.JSON, version)
		if err != nil {
			log.Printf("federation: the state event %s of %s cannot be prepared: %v", row.EventID, roomID, err)
			continue
		}
		out = append(out, pdu)
	}
	return out, nil
}

// authChainForRoom gathers the events the room's state cites, and the events those cite:
// the chain a receiving server needs to check the state it was handed without fetching
// each event back one at a time.
func (s *Service) authChainForRoom(roomID, version string) ([]any, error) {
	state, err := s.db.StateRepo.Events(roomID, nil)
	if err != nil {
		return nil, matrix.ErrInternal(err)
	}
	seen := map[string]bool{}
	out := make([]any, 0, len(state))
	wave := state
	for depth := 0; depth < maxAuthChainDepth && len(wave) > 0; depth++ {
		next := make([]string, 0, len(wave))
		for _, row := range wave {
			if seen[row.EventID] {
				continue
			}
			seen[row.EventID] = true
			pdu, err := transportPDU(row.JSON, version)
			if err == nil {
				out = append(out, pdu)
			}
			next = append(next, storedIDsOf(row.AuthEvents)...)
		}
		if len(next) == 0 {
			break
		}
		rows, err := s.db.EventRepo.ListByIDs(next)
		if err != nil {
			return nil, matrix.ErrInternal(err)
		}
		wave = rows
	}
	return out, nil
}

// storedIDsOf reads one of the JSON id arrays stored beside an event.
func storedIDsOf(raw string) []string {
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return ids
}
