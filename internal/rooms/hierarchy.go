package rooms

// The room hierarchy is a space read from the top: a client asks one room what it contains, and the
// server walks the `m.space.child` links for it and answers with a summary of each room instead of
// its state. Two things make it more than a graph walk, and they are the reason the endpoint exists
// at all:
//
//   - A room is listed only for a reader who is allowed to see it. A space can point at rooms the
//     reader has no standing in, and a summary carries a name, a topic and an avatar — enough to
//     leak a room's existence and its subject. So every room on the walk is asked the question the
//     read endpoints ask: may this reader see this room.
//   - A child may live on a server that holds nothing of it. This server cannot summarise such a
//     room; the child's own servers can, and the Server-Server API has a matching query for that.
//     What a peer answers is treated as what that peer says about its own room, never as fact.
//
// The two answers are not the same size, and the difference matters. The federation query decides
// whether the *asking server* could see a room, because a server answers about a peer and not about
// somebody else's user; the client-facing walk then decides again for the user who asked. That is the
// specification's split, and it is why a restricted room can be reported by a peer and still withheld
// from a user whose membership that peer cannot see.

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/roomversion"
)

// The bounds of one hierarchy request. The specification asks servers to apply a default and impose a
// maximum on the page size and on the depth, because the question is "what is under this room" and a
// room's children are whatever whoever created the space linked.
const (
	// hierarchyDefaultLimit is the page size when the client does not name one.
	hierarchyDefaultLimit = 50
	// hierarchyMaxLimit is the largest page a client may ask for.
	hierarchyMaxLimit = 1000
	// hierarchyMaxDepth is how deep a walk may go. A request for more is answered at this depth
	// rather than refused: the client asked for a subtree, and it gets one.
	hierarchyMaxDepth = 20
	// hierarchyMaxVisited bounds the rooms one request may look at, however the depth is arranged.
	// It is what stops a cycle of spaces — prohibited by the specification but storable — from
	// spending the process.
	hierarchyMaxVisited = 1000
	// hierarchyMaxChildren caps the children of one room that are listed and followed, and
	// hierarchyMaxServers the peers asked about one room. A space with ten thousand children, or a
	// child naming fifty servers, is not worth a request that never ends.
	hierarchyMaxChildren = 50
	hierarchyMaxServers  = 3
)

// spaceRoomType is the `m.room.create` content type that marks a room as a space. Only a space's
// children are worth following, and a peer's summary of a space is not treated as an answer about
// that space's children.
const spaceRoomType = "m.space"

// HierarchyPage is one page of a space tree, as a client's hierarchy request answers it.
type HierarchyPage struct {
	// Rooms holds the room summaries, starting with the room that was asked about.
	Rooms []map[string]any `json:"rooms"`
	// NextBatch is the token for the following page. It is absent when there is no following page.
	NextBatch string `json:"next_batch,omitempty"`
}

// HierarchyAnswer is what a homeserver gives a peer that asked about a room it holds.
//
// It is deliberately shallower than the client-facing walk: the room asked about — with the stripped
// `m.space.child` events naming its children — one summary per child the asking server could see, and
// the IDs of the children it could not. The children are not expanded, because the asking server
// expands them, possibly from other peers, and it is the asking server that has a user in mind.
type HierarchyAnswer struct {
	Room                 map[string]any   `json:"room"`
	Children             []map[string]any `json:"children"`
	InaccessibleChildren []string         `json:"inaccessible_children"`
}

// RemoteHierarchy asks a homeserver that holds a room what it says about that room and its children.
// It is the outgoing half of the Server-Server API's hierarchy query.
type RemoteHierarchy interface {
	Hierarchy(ctx context.Context, destination, roomID string, suggestedOnly bool) (HierarchyAnswer, error)
}

// SetRemoteHierarchy gives this server a way to ask other servers about rooms it does not hold. With
// no way to ask, a room this server has never seen is simply absent from a hierarchy — an honest
// answer, but not the correct one.
func (s *Service) SetRemoteHierarchy(hierarchy RemoteHierarchy) { s.remoteHierarchy = hierarchy }

// HierarchyOptions are the knobs a hierarchy request carries.
type HierarchyOptions struct {
	// Limit is the page size; zero asks for the server's default.
	Limit int
	// MaxDepth is how deep to walk: 0 is the requested room alone, and a negative value means the
	// client named no depth, which is walked out to hierarchyMaxDepth. Anything larger than
	// hierarchyMaxDepth is walked to hierarchyMaxDepth.
	MaxDepth int
	// SuggestedOnly keeps out the children a space did not mark as suggested.
	SuggestedOnly bool
	// From resumes a previous page. It is opaque to the client and carries the parameters that must
	// not change in the middle of paging through one tree.
	From string
}

// hierarchyQueueItem is one room waiting to be summarised.
type hierarchyQueueItem struct {
	roomID string
	// via names the servers the parent's `m.space.child` event points at for this child. It is the
	// only reason this server knows anywhere to ask; for the root room of a request there is
	// usually none.
	via []string
	// depth counts how far below the requested room this room sits.
	depth int
	// remote is a summary a peer already offered for this room. It is used directly only when it is
	// enough on its own — a room that is not a space has no children to look for, and a room at the
	// depth limit will not have any looked for — because a peer's summary of a space says nothing
	// about that space's children.
	remote map[string]any
}

// Hierarchy answers a client's hierarchy request: the tree under a room, as far as this user may see
// it.
//
// The walk is depth-first in the order the space's own `m.space.child` events order their children, so
// a nested space's rooms arrive between that space and the rooms listed after it, which is what a
// client drawing a tree expects. A room the user may not see is left out silently, but its children
// are still walked: a room the reader cannot enter can contain rooms the reader can, and hiding that
// subtree would make the shape of a space depend on membership in a room the client is not asking
// about.
//
// The room the request is *about* is the exception, and gets 403: an empty answer there would leave
// the client guessing whether the tree is empty or the reader is outside it.
func (s *Service) Hierarchy(ctx context.Context, roomID, requester string, opts HierarchyOptions) (HierarchyPage, error) {
	if roomID == "" {
		return HierarchyPage{}, matrixerr.New(matrixerr.CodeMissingParams, "A hierarchy request needs a room ID.")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = hierarchyDefaultLimit
	}
	if limit > hierarchyMaxLimit {
		limit = hierarchyMaxLimit
	}
	depth := opts.MaxDepth
	if depth < 0 || depth > hierarchyMaxDepth {
		depth = hierarchyMaxDepth
	}
	offset := 0
	if opts.From != "" {
		suggestedAt, depthAt, at, err := parseHierarchyToken(opts.From)
		if err != nil {
			return HierarchyPage{}, err
		}
		// The walk has to be the one the first page came out of. A token carried into a request with
		// different filters points into a different tree, and answering from that tree would be a
		// quiet mixture of two.
		if suggestedAt != opts.SuggestedOnly || depthAt != depth {
			return HierarchyPage{}, matrixerr.New(matrixerr.CodeInvalidParam,
				"suggested_only and max_depth cannot change while paging through a space.")
		}
		offset = at
	}

	// The room asked about has to be one this user may look at.
	known, err := s.roomKnown(ctx, roomID)
	if err != nil {
		return HierarchyPage{}, err
	}
	var root map[string]any
	if known {
		visible, err := s.visibleToUser(ctx, roomID, requester)
		if err != nil {
			return HierarchyPage{}, err
		}
		if !visible {
			return HierarchyPage{}, matrixerr.New(matrixerr.CodeForbidden, "You are not allowed to view this room.")
		}
	} else {
		summary, _, _, err := s.hierarchyFromPeers(ctx, roomID, nil, requester, opts.SuggestedOnly)
		if err != nil {
			return HierarchyPage{}, err
		}
		if summary == nil {
			return HierarchyPage{}, matrixerr.New(matrixerr.CodeForbidden, "You are not allowed to view this room.")
		}
		root = summary
	}

	var walked []map[string]any
	queue := []hierarchyQueueItem{{roomID: roomID, remote: root}}
	visited := 0
	seen := map[string]bool{}
	for len(queue) > 0 && visited < hierarchyMaxVisited {
		item := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[item.roomID] {
			// A room already walked. A space can be linked more than once, and a cycle, though
			// prohibited, can be stored.
			continue
		}
		seen[item.roomID] = true
		visited++

		entry, children, skip, err := s.hierarchyRoomForUser(ctx, item, requester, opts.SuggestedOnly)
		if err != nil {
			return HierarchyPage{}, err
		}
		if entry == nil {
			continue
		}
		walked = append(walked, entry)
		if item.depth >= depth {
			continue
		}
		// Pushed in reverse so that the first child, by the space's own ordering, is the next room
		// taken off the stack.
		states := childStates(entry)
		for index := len(states) - 1; index >= 0; index-- {
			id, _ := states[index]["state_key"].(string)
			if id == "" || skip[id] {
				continue
			}
			queue = append(queue, hierarchyQueueItem{
				roomID: id,
				via:    stringSlice(contentOfEvent(states[index])["via"]),
				depth:  item.depth + 1,
				remote: children[id],
			})
		}
	}

	page := HierarchyPage{Rooms: []map[string]any{}}
	if offset >= len(walked) {
		return page, nil
	}
	end := offset + limit
	if end < len(walked) {
		page.Rooms = walked[offset:end]
		page.NextBatch = hierarchyToken(end, opts.SuggestedOnly, depth)
	} else {
		page.Rooms = walked[offset:]
	}
	return page, nil
}

// hierarchyRoomForUser summarises one room for one user: from this server's own state when it holds
// the room, from a peer when it does not. A nil entry means the room is not listed — unknown, or not
// visible to this user.
//
// Along with the entry it returns the child summaries a peer supplied (empty for a local room) and the
// children that peer said were not worth asking about.
func (s *Service) hierarchyRoomForUser(ctx context.Context, item hierarchyQueueItem, requester string, suggestedOnly bool) (map[string]any, map[string]map[string]any, map[string]bool, error) {
	known, err := s.roomKnown(ctx, item.roomID)
	if err != nil {
		return nil, nil, nil, err
	}
	if known {
		visible, err := s.visibleToUser(ctx, item.roomID, requester)
		if err != nil {
			return nil, nil, nil, err
		}
		if !visible {
			return nil, nil, nil, nil
		}
		entry, err := s.hierarchyEntry(ctx, item.roomID, suggestedOnly, true)
		return entry, map[string]map[string]any{}, map[string]bool{}, err
	}

	// A peer's summary may already be in hand from the parent's answer. It is enough when the room is
	// not a space — there are no children to look for — or when the walk is not going deeper anyway.
	if item.remote != nil && (roomTypeOf(item.remote) != spaceRoomType || item.depth+1 >= hierarchyMaxDepth) {
		visible, err := s.remoteVisibleToUser(ctx, item.roomID, item.remote, requester)
		if err != nil {
			return nil, nil, nil, err
		}
		if !visible {
			return nil, nil, nil, nil
		}
		return item.remote, map[string]map[string]any{}, map[string]bool{}, nil
	}
	summary, children, skip, err := s.hierarchyFromPeers(ctx, item.roomID, item.via, requester, suggestedOnly)
	if err != nil || summary == nil {
		return nil, nil, nil, err
	}
	return summary, children, skip, nil
}

// hierarchyFromPeers asks the servers a child points at for a room this server does not hold, and
// filters what comes back for the user who asked.
//
// A nil summary with a nil error is an ordinary outcome: nobody answered, or nobody answered about
// the room that was asked for.
func (s *Service) hierarchyFromPeers(ctx context.Context, roomID string, via []string, requester string, suggestedOnly bool) (map[string]any, map[string]map[string]any, map[string]bool, error) {
	if s.remoteHierarchy == nil {
		return nil, nil, nil, nil
	}
	children := map[string]map[string]any{}
	skip := map[string]bool{}
	for _, destination := range hierarchyDestinations(via, roomID) {
		answer, err := s.remoteHierarchy.Hierarchy(ctx, destination, roomID, suggestedOnly)
		if err != nil {
			// A peer that does not answer is not a failure of the client's request: the room is not
			// listed and the walk goes on. It is logged, because "the space looked smaller than it is"
			// is otherwise impossible to diagnose.
			s.log.Debug("a hierarchy query was set aside",
				"room", roomID, "server", destination, "error", err)
			continue
		}
		if answer.Room == nil || roomIDOf(answer.Room) != roomID {
			// A summary is about the room its ID names. A peer answering with a different room ID is
			// answering about something else, and listing that room under this one would be this
			// server repeating a claim it cannot check.
			s.log.Debug("a hierarchy answer named a different room than was asked for",
				"room", roomID, "server", destination, "answered", roomIDOf(answer.Room))
			continue
		}
		visible, err := s.remoteVisibleToUser(ctx, roomID, answer.Room, requester)
		if err != nil {
			return nil, nil, nil, err
		}
		if !visible {
			return nil, nil, nil, nil
		}
		for _, child := range answer.Children {
			if id := roomIDOf(child); id != "" {
				children[id] = child
			}
		}
		for _, id := range answer.InaccessibleChildren {
			skip[id] = true
		}
		return answer.Room, children, skip, nil
	}
	return nil, nil, nil, nil
}

// hierarchyDestinations orders the servers worth asking about a room: the ones the parent's
// `m.space.child` event names, then the server the room's own ID names.
//
// The second group is not a guess about where the room is. A room ID says which server created it, and
// that server is the one that would hold it if anybody does; a hierarchy query is an authenticated
// read that only a holder answers, and only about what the asking server could see. That is a
// different thing from inventing a server to carry a *join* to, which is what a client, not a server,
// decides.
func hierarchyDestinations(via []string, roomID string) []string {
	out := make([]string, 0, hierarchyMaxServers)
	seen := map[string]bool{}
	add := func(server string) {
		if server == "" || seen[server] || len(out) >= hierarchyMaxServers {
			return
		}
		seen[server] = true
		out = append(out, server)
	}
	for _, server := range via {
		add(server)
	}
	if _, domain, err := matrixid.SplitID(matrixid.SigilRoom, roomID); err == nil {
		add(domain)
	}
	return out
}

// hierarchyEntry builds the summary of one room this server holds.
//
// The fields are the room's own state read out of this server's copy, which is the only copy it has:
// an entry about a remote room is built by the server that holds it, and this function is not how a
// remote room is described.
func (s *Service) hierarchyEntry(ctx context.Context, roomID string, suggestedOnly bool, includeChildren bool) (map[string]any, error) {
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return nil, matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
		}
		return nil, fmt.Errorf("rooms: cannot load the room for a hierarchy entry: %w", err)
	}
	listed, err := s.listRoom(ctx, room)
	if err != nil {
		return nil, err
	}
	rule, allowed, err := s.joinRuleAndAllowed(ctx, roomID)
	if err != nil {
		return nil, err
	}
	avatar, err := s.avatarURL(ctx, roomID)
	if err != nil {
		return nil, err
	}
	entry := map[string]any{
		"room_id":            roomID,
		"room_version":       room.RoomVersion,
		"num_joined_members": listed.NumJoinedMembers,
		"world_readable":     listed.WorldReadable,
		// guest_can_join is the room's own answer about guests, independent of whether the room is
		// otherwise open.
		"guest_can_join": s.guestsAllowed(ctx, roomID),
	}
	if listed.Name != "" {
		entry["name"] = listed.Name
	}
	if listed.Topic != "" {
		entry["topic"] = listed.Topic
	}
	if listed.CanonicalAlias != "" {
		entry["canonical_alias"] = listed.CanonicalAlias
	}
	if listed.RoomType != "" {
		entry["room_type"] = listed.RoomType
	}
	if avatar != "" {
		entry["avatar_url"] = avatar
	}
	if rule != "" {
		entry["join_rule"] = rule
	}
	if len(allowed) > 0 {
		entry["allowed_room_ids"] = allowed
	}
	children, err := s.childState(ctx, roomID, suggestedOnly)
	if err != nil {
		return nil, err
	}
	if !includeChildren {
		// The shape still carries the key, because the Server-Server API asks for it; an empty list
		// here means "this answer says nothing about children", and a peer is only ever allowed to
		// read it as the whole story for a room that is not a space.
		children = []map[string]any{}
	}
	entry["children_state"] = children
	return entry, nil
}

// childState returns a space's `m.space.child` events as stripped state: the type, the state key, the
// content, the sender and the timestamp, which is everything a client needs to draw an edge and
// nothing that lets it read the child.
//
// The events come out in the order the specification gives children: by the `order` string when it is
// a well-formed one, then by the timestamp of the link, then by room ID. Children whose link carries
// no `via` are dropped — a link with no server to reach the child through is how a redaction leaves a
// `m.space.child` event, and a redacted link is not a claim that the room belongs to the space.
func (s *Service) childState(ctx context.Context, roomID string, suggestedOnly bool) ([]map[string]any, error) {
	entries, err := s.state.ByType(ctx, roomID, "m.space.child")
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot read a space's children: %w", err)
	}
	type child struct {
		stateKey string
		stripped map[string]any
		order    string
		ordered  bool
		ts       int64
	}
	children := make([]child, 0, len(entries))
	for _, entry := range entries {
		event, err := s.eventJSON(ctx, entry.EventID)
		if err != nil {
			return nil, err
		}
		content := contentOfEvent(event)
		via := stringSlice(content["via"])
		if len(via) == 0 {
			continue
		}
		if suggestedOnly {
			suggested, ok := content["suggested"].(bool)
			if !ok || !suggested {
				continue
			}
		}
		timestamp := int64Of(event["origin_server_ts"])
		stripped := map[string]any{
			"type":             "m.space.child",
			"state_key":        entry.StateKey,
			"content":          content,
			"sender":           stringOf(event["sender"]),
			"origin_server_ts": timestamp,
		}
		order, ordered := childOrder(content["order"])
		children = append(children, child{
			stateKey: entry.StateKey,
			stripped: stripped,
			order:    order,
			ordered:  ordered,
			ts:       timestamp,
		})
	}
	sort.SliceStable(children, func(a, b int) bool {
		left, right := children[a], children[b]
		if left.ordered != right.ordered {
			// A link with a usable `order` sorts before one without: the space said which comes first.
			return left.ordered
		}
		if left.ordered && left.order != right.order {
			return left.order < right.order
		}
		if left.ts != right.ts {
			return left.ts < right.ts
		}
		return left.stateKey < right.stateKey
	})
	out := make([]map[string]any, 0, len(children))
	for index, next := range children {
		if index >= hierarchyMaxChildren {
			break
		}
		out = append(out, next.stripped)
	}
	return out, nil
}

// childOrder accepts an `order` string only if it is short enough and printable enough to order; the
// specification restricts it to printable US-ASCII and 50 characters.
func childOrder(value any) (string, bool) {
	order, ok := value.(string)
	if !ok || order == "" || len(order) > 50 {
		return "", false
	}
	for _, character := range order {
		if character < 0x20 || character > 0x7E {
			return "", false
		}
	}
	return order, true
}

// childStates reads the children of a summary, whether this server built it or a peer did.
func childStates(entry map[string]any) []map[string]any {
	list, _ := entry["children_state"].([]map[string]any)
	if list == nil {
		if raw, ok := entry["children_state"].([]any); ok {
			for _, item := range raw {
				if event, ok := item.(map[string]any); ok {
					list = append(list, event)
				}
			}
		}
	}
	return list
}

func roomIDOf(entry map[string]any) string { return stringOf(entry["room_id"]) }

func roomTypeOf(entry map[string]any) string { return stringOf(entry["room_type"]) }

func stringOf(value any) string {
	text, _ := value.(string)
	return text
}

func boolOf(value any) bool {
	yes, _ := value.(bool)
	return yes
}

// stringSlice accepts a JSON array of strings and ignores anything else, because every use of it here
// is reading a field another server wrote.
func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}

// A hierarchy page token carries the position in the walk and the two parameters that shape the walk,
// so that paging cannot silently change the tree being paged through. It is not a secret and it is not
// a lease: a page two is a re-walk to that position, which is what lets a token be understood without
// keeping state per request.
func hierarchyToken(offset int, suggestedOnly bool, depth int) string {
	raw := strconv.Itoa(offset) + "|" + strconv.FormatBool(suggestedOnly) + "|" + strconv.Itoa(depth)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func parseHierarchyToken(token string) (bool, int, int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return false, 0, 0, matrixerr.New(matrixerr.CodeInvalidParam, "The pagination token is not one this server gave.")
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return false, 0, 0, matrixerr.New(matrixerr.CodeInvalidParam, "The pagination token is not one this server gave.")
	}
	offset, err := strconv.Atoi(parts[0])
	if err != nil || offset < 0 {
		return false, 0, 0, matrixerr.New(matrixerr.CodeInvalidParam, "The pagination token is not one this server gave.")
	}
	suggested, err := strconv.ParseBool(parts[1])
	if err != nil {
		return false, 0, 0, matrixerr.New(matrixerr.CodeInvalidParam, "The pagination token is not one this server gave.")
	}
	depth, err := strconv.Atoi(parts[2])
	if err != nil {
		return false, 0, 0, matrixerr.New(matrixerr.CodeInvalidParam, "The pagination token is not one this server gave.")
	}
	return suggested, depth, offset, nil
}

// visibleToUser answers whether a user of this server may see a room in a space tree. It is the same
// question the read endpoints ask, asked of a room the reader may never have heard of.
//
// The order of the checks is not decoration. A room that is public, or that opened its history to the
// world, is open to anybody and says so in its own state; checking membership first would make a public
// room invisible to the people it is public to. A room that restricts joining is the interesting case:
// its restriction names another room, and this server may only lean on its copy of that room while it
// is still inside it — the same currency rule a restricted join is answered with, and for the same
// reason. Showing a room on membership that nobody has been telling this server about anymore is a leak
// of the room's name and subject to somebody the room has moved on without.
func (s *Service) visibleToUser(ctx context.Context, roomID, userID string) (bool, error) {
	known, err := s.roomKnown(ctx, roomID)
	if err != nil {
		return false, err
	}
	if !known {
		// A room this server does not hold has no local state to answer from. A peer's summary of it
		// is weighed by remoteVisibleToUser, not here.
		return false, nil
	}
	rule, allowed, version, err := s.hierarchyJoinRule(ctx, roomID)
	if err != nil {
		return false, err
	}
	if joinRuleOpensRoom(rule, version) {
		return true, nil
	}
	visibility, err := s.historyVisibility(ctx, roomID)
	if err != nil {
		return false, err
	}
	if visibility == "world_readable" {
		return true, nil
	}
	membership, err := s.MembershipOf(ctx, roomID, userID)
	if err != nil {
		return false, err
	}
	if membership == eventauth.MembershipJoin || membership == eventauth.MembershipInvite {
		return true, nil
	}
	return s.userInAllowRooms(ctx, allowed, userID)
}

// remoteVisibleToUser weighs a summary a peer gave for a room this server does not hold.
//
// A peer is the authority on its own room's join rule, its history visibility and the rooms its
// restriction names — that is what the peer was asked. It is not an authority on who the user in front
// of this server is, so the membership half of the question is answered here, out of this server's own
// state. And if this server's own state says the user may see the room, that is believed over the
// peer's silence: a server is told about its own members.
func (s *Service) remoteVisibleToUser(ctx context.Context, roomID string, summary map[string]any, userID string) (bool, error) {
	if rule := stringOf(summary["join_rule"]); rule == "" || joinRuleNamedOpensRoom(rule) {
		return true, nil
	}
	if boolOf(summary["world_readable"]) {
		return true, nil
	}
	if allowed := stringSlice(summary["allowed_room_ids"]); len(allowed) > 0 {
		visible, err := s.userInAllowRooms(ctx, allowed, userID)
		if err != nil {
			return false, err
		}
		if visible {
			return true, nil
		}
	}
	// The peer said nothing that opens the room. This server may still know something that does: the
	// user may be a member of it, or invited to it, here.
	return s.visibleToUser(ctx, roomID, userID)
}

// userInAllowRooms says whether the user is a joined member of one of the rooms a restriction names,
// and only where this server is in a position to know.
func (s *Service) userInAllowRooms(ctx context.Context, allowed []string, userID string) (bool, error) {
	for _, allowRoom := range allowed {
		membership, err := s.MembershipOf(ctx, allowRoom, userID)
		if err != nil {
			return false, err
		}
		if membership != eventauth.MembershipJoin {
			continue
		}
		current, err := s.stillTracksRoom(ctx, allowRoom)
		if err != nil {
			return false, err
		}
		if current {
			return true, nil
		}
	}
	return false, nil
}

// hierarchyJoinRule reads a room's join rule, the rooms it names when it restricts joining, and the
// room version whose features those rules are written in.
func (s *Service) hierarchyJoinRule(ctx context.Context, roomID string) (string, []string, roomversion.Rules, error) {
	rule, allowed, err := s.joinRuleAndAllowed(ctx, roomID)
	if err != nil {
		return "", nil, roomversion.Rules{}, err
	}
	room, err := s.rooms.ByRoomID(ctx, roomID)
	if err != nil {
		if notFound(err) {
			return rule, allowed, roomversion.Rules{}, nil
		}
		return "", nil, roomversion.Rules{}, fmt.Errorf("rooms: cannot load a room's version: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		// A version this server does not know cannot be assumed to support a join rule it may or may
		// not have; the rule is then not treated as opening the room.
		return rule, allowed, roomversion.Rules{}, nil
	}
	return rule, allowed, rules, nil
}

// joinRuleOpensRoom reports the join rules that let anybody in, given the room version's features. A
// rule the version cannot express does not open anything: it is state the room's own auth rules ignore.
func joinRuleOpensRoom(rule string, version roomversion.Rules) bool {
	switch rule {
	case "public":
		return true
	case "knock":
		return version.SupportsKnock
	case "knock_restricted":
		return version.SupportsKnockRestricted
	default:
		return false
	}
}

// joinRuleNamedOpensRoom is joinRuleOpensRoom for a room whose version is not known here, which is the
// case for a summary a peer sent. A hierarchy answer does not carry a room version, so a peer's
// `knock` or `knock_restricted` is taken at its word — the same reading the specification's own
// summary rules give, and it opens the *listing*, never a join.
func joinRuleNamedOpensRoom(rule string) bool {
	return rule == "public" || rule == "knock" || rule == "knock_restricted"
}

// visibleToServer answers the same question for a homeserver rather than a user, which is what the
// Server-Server API asks: a peer is asking whether *it* could see this room, and this server has no
// standing to answer about the peer's users.
//
// "Could see" is read broadly, as the specification does: a server that has anybody joined or invited
// in the room is in the room, and a server with anybody joined in a room the restriction names could
// carry one of its own users into this one. The peer is then trusted to do its own filtering for the
// user it has in mind — which is why an answer that is wider than the asking user's entitlement is not
// a leak, and an answer narrower than the asking server's standing would break a room the peer is
// entitled to walk.
func (s *Service) visibleToServer(ctx context.Context, roomID, server string) (bool, error) {
	if server == "" {
		return false, nil
	}
	rule, allowed, version, err := s.hierarchyJoinRule(ctx, roomID)
	if err != nil {
		return false, err
	}
	if joinRuleOpensRoom(rule, version) {
		return true, nil
	}
	visibility, err := s.historyVisibility(ctx, roomID)
	if err != nil {
		return false, err
	}
	if visibility == "world_readable" {
		return true, nil
	}
	inRoom, err := serverInRoom(ctx, s, roomID, server, eventauth.MembershipJoin, eventauth.MembershipInvite)
	if err != nil {
		return false, err
	}
	if inRoom {
		return true, nil
	}
	for _, allowRoom := range allowed {
		// An allow room this server does not hold cannot be read at all; one it holds is read for the
		// peer's standing, which is the question being asked.
		known, err := s.roomKnown(ctx, allowRoom)
		if err != nil {
			return false, err
		}
		if !known {
			continue
		}
		present, err := serverInRoom(ctx, s, allowRoom, server, eventauth.MembershipJoin)
		if err != nil {
			return false, err
		}
		if present {
			return true, nil
		}
	}
	return false, nil
}

// serverInRoom says whether any of the room's current membership events name a user of the given server
// with one of the given memberships.
func serverInRoom(ctx context.Context, s *Service, roomID, server string, memberships ...string) (bool, error) {
	entries, err := s.state.ByType(ctx, roomID, eventauth.TypeMember)
	if err != nil {
		return false, fmt.Errorf("rooms: cannot read a room's members: %w", err)
	}
	for _, entry := range entries {
		domain, err := matrixid.UserDomain(entry.StateKey)
		if err != nil || !matrixid.IsLocal(domain, server) {
			continue
		}
		event, err := s.eventJSON(ctx, entry.EventID)
		if err != nil {
			return false, err
		}
		membership, _ := contentOfEvent(event)["membership"].(string)
		for _, wanted := range memberships {
			if membership == wanted {
				return true, nil
			}
		}
	}
	return false, nil
}

// HierarchyQuery answers another homeserver's hierarchy query about a room this server holds.
//
// The answer is the room, one summary per child the asking server could see, and the IDs of the children
// it could not. That last list is not a rudeness: it tells the asking server not to spend another
// request on a room this server has already answered about, which is what keeps a walk over a large
// space from turning into a retry loop.
//
// The children are not expanded. The asking server expands them, from whichever server each child
// points at, because that is the server that can answer for it.
func (s *Service) HierarchyQuery(ctx context.Context, roomID, origin string, suggestedOnly bool) (HierarchyAnswer, error) {
	known, err := s.roomKnown(ctx, roomID)
	if err != nil {
		return HierarchyAnswer{}, err
	}
	if !known {
		return HierarchyAnswer{}, matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
	}
	visible, err := s.visibleToServer(ctx, roomID, origin)
	if err != nil {
		return HierarchyAnswer{}, err
	}
	if !visible {
		// The same answer as for a room this server has never heard of. Whether a room exists is a
		// fact a server that cannot see it has not been given yet, and a hierarchy answer carries room
		// names to whoever asks.
		return HierarchyAnswer{}, matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", roomID)
	}
	entry, err := s.hierarchyEntry(ctx, roomID, suggestedOnly, true)
	if err != nil {
		return HierarchyAnswer{}, err
	}
	answer := HierarchyAnswer{
		Room:                 entry,
		Children:             []map[string]any{},
		InaccessibleChildren: []string{},
	}
	for _, child := range childStates(entry) {
		childID, _ := child["state_key"].(string)
		if childID == "" {
			continue
		}
		childKnown, err := s.roomKnown(ctx, childID)
		if err != nil {
			return HierarchyAnswer{}, err
		}
		if !childKnown {
			// A child this server has never seen is not a child it can vouch for, in either
			// direction: it is not listed, and it is not named as inaccessible, because a server that
			// does not hold a room has not established that it exists.
			continue
		}
		childVisible, err := s.visibleToServer(ctx, childID, origin)
		if err != nil {
			return HierarchyAnswer{}, err
		}
		if !childVisible {
			answer.InaccessibleChildren = append(answer.InaccessibleChildren, childID)
			continue
		}
		// A child is summarised without its own children: the asking server asks about those from the
		// server the child points at.
		childEntry, err := s.hierarchyEntry(ctx, childID, suggestedOnly, false)
		if err != nil {
			return HierarchyAnswer{}, err
		}
		answer.Children = append(answer.Children, childEntry)
	}
	return answer, nil
}
