// Package rooms creates rooms and puts events into them.
//
// This is where the M2 primitives meet: the room version registry decides the rules,
// internal/eventauth decides whether an event is allowed, internal/events computes
// hashes, event IDs and signatures, and internal/persist stores the event graph and
// the room state. Keeping that orchestration here means the HTTP layer stays a
// transport and the rules stay in one place.
//
// Local sending does not run state resolution. A room this server created has a
// single chain of forward extremities and its current state is what the state rows
// say. State resolution becomes necessary when the graph forks, which is what
// receiving federated events does; internal/state holds that algorithm.
package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"gorm.io/gorm"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/persist"
	"github.com/enimatek/lattice/internal/redaction"
	"github.com/enimatek/lattice/internal/roomversion"
)

// maxPrevEvents is how many predecessors a created event cites. The specifications
// cap prev_events at 10; a room holding more forward extremities than that needs the
// forks merged first, which is federation work.
const maxPrevEvents = 10

// Preset names the initial-state layouts the Client-Server API defines.
type Preset string

// The presets defined by the Client-Server API.
const (
	PresetPrivateChat        Preset = "private_chat"
	PresetPublicChat         Preset = "public_chat"
	PresetTrustedPrivateChat Preset = "trusted_private_chat"
)

// CreateRequest describes a room to create.
type CreateRequest struct {
	Creator     string
	Preset      Preset
	RoomVersion string
	// CreationContent is merged into m.room.create. The creator, the room version
	// and a predecessor are never taken from the client.
	CreationContent map[string]any
	// InitialState is applied after the preset, so a client can replace a preset
	// entry for the same (type, state_key) pair.
	InitialState []StateContent
	// PowerLevelOverride merges into the preset's m.room.power_levels event.
	PowerLevelOverride map[string]any
	// RoomAliasName is the local part of an alias to create for the room, on this
	// server's name.
	RoomAliasName string
	// Invite lists users to invite as the room is created.
	Invite []string
	// IsDirect marks those invitations as private ones, which the specification records in
	// the member events and in the creator's `m.direct` account data rather than in the room:
	// being a direct message is a fact about the conversation between two users, not a
	// property every member of the room is entitled to know.
	IsDirect bool
	// Published puts the room in this server's public room directory from the start,
	// which is what `visibility: public` on /createRoom means.
	Published bool
	Name      string
	Topic     string
	AvatarURL string
}

// StateContent is one requested state event.
type StateContent struct {
	Type     string
	StateKey string
	Content  map[string]any
}

// SendRequest describes an event to add to a room.
type SendRequest struct {
	RoomID    string
	Sender    string
	DeviceID  string
	TxnID     string
	EventType string
	// StateKey is only used when IsState is set; a state event may legitimately have
	// an empty state key.
	StateKey string
	IsState  bool
	Content  map[string]any
	// Extra are top-level fields this server adds to the event before it is authorised
	// and signed. Only things that cannot live in `content` belong here: a redaction in a
	// room version before 11 names its target at the top level, and where that field goes
	// is decided by the room version, not by the client.
	Extra map[string]any
}

// Service creates rooms and appends events to them.
type Service struct {
	db     *persist.Database
	rooms  persist.RoomRepo
	events persist.EventRepo
	state  persist.StateRepo
	groups persist.StateGroupRepo
	alias  persist.RoomAliasRepo
	signer events.Signer
	// serve turns a stored event into the form a client is allowed to see. It lives here
	// so that `/messages`, `/event` and `/sync` answer the redaction question the same
	// way; when each path decided for itself, the same event could come back with its
	// content in one response and emptied in another.
	serve *redaction.Servicer
	// presence announces a user's arrival to the members of a room they join. It is a
	// separate service because presence is the user's, not the room's; the room service
	// only tells it that somebody arrived.
	presence PresenceNotifier
	// upgrades is told that a room ended and which room replaced it, so that the users'
	// rules about the old room can be carried over. Implemented by
	// [*pushrules.Service].
	upgrades UpgradeNotifier
	// relayer hands a persisted event to the other homeservers in the room. It is nil
	// until federation is wired up, and a room service without one works exactly as
	// before: local rooms stay local.
	relayer Relayer
	// deviceListAnnouncer tells a user's other servers about their devices; nil until
	// [Service.SetDeviceListAnnouncer].
	deviceListAnnouncer DeviceListAnnouncer
	// inviter carries an invite for a user who lives elsewhere to their own server; nil
	// until [Service.SetInviter].
	inviter RemoteInviter
	// remoteJoin carries a local user into a room this server does not hold. It is nil
	// until federation is wired up, and a room service without one refuses such a join
	// rather than pretending the room does not exist.
	remoteJoin RemoteJoin
	name       string
	log        *slog.Logger
	// now is replaced in tests. A homeserver's timestamps must come from the server,
	// but a test has to control them to make ordering assertions.
	now func() time.Time
}

// New builds the room service. signer must not be nil: an unsigned event is refused
// by every other server, so there is no useful unsigned mode.
// PresenceNotifier is what the room service uses to say that a user has arrived in a
// room. Implemented by [*presence.Service].
//
// It is deliberately called only after the transaction that wrote the membership event
// committed. The presence service writes through the pool, and a write attempted while a
// transaction holds the single connection would wait for that same connection: a deadlock
// rather than a delay.
type PresenceNotifier interface {
	Announce(ctx context.Context, userID string) error
}

// UpgradeNotifier is what the room service uses to say that a room has been replaced by
// another one. It is called for a tombstone this server has just persisted, whichever way
// the upgrade happened: the endpoint, or a client that built the new room by hand and then
// closed the old one.
//
// Like the presence notifier, it is called only after the transaction that wrote the
// tombstone committed.
type UpgradeNotifier interface {
	RoomUpgraded(ctx context.Context, oldRoomID, newRoomID string) error
}

// Relayer is what the room service uses to say that an event exists and the other
// servers in the room should be told. Implemented by [*federation.Sender].
//
// It is called after the write committed, like the other notifiers: a queue entry for an
// event that was never persisted would make this server promise to send something it does
// not have. Queuing is not delivery — the sender retries on its own schedule, so one
// unreachable remote cannot make a local send fail.
type Relayer interface {
	Relay(ctx context.Context, roomID, eventID string) error
}

// SetRelayer connects the federation sender. Without it this server keeps rooms to
// itself, which is the honest state of a server with federation not yet wired in.
func (s *Service) SetRelayer(relayer Relayer) { s.relayer = relayer }

// DeviceListAnnouncer tells the other homeservers that share a room with a local user that this
// user's devices are worth re-reading. The end-to-end encryption key service implements it.
type DeviceListAnnouncer interface {
	AnnounceDeviceListsToFederation(ctx context.Context, userID string)
}

// SetDeviceListAnnouncer connects that announcement. It is separate from [Service.SetRelayer]
// because the two answer different questions — where to send a room's events, and who to tell
// about a user's devices — even though the same sender carries both in a running server.
func (s *Service) SetDeviceListAnnouncer(announcer DeviceListAnnouncer) {
	s.deviceListAnnouncer = announcer
}

// AnnounceDeviceLists tells the other homeservers in a local user's rooms that the user's devices
// are worth re-reading.
//
// The join handshake needs this directly. A join made here arrives as part of the bundle a resident
// server hands over, and an adopted bundle does not run the side effects of an event this server
// accepted itself — it did not decide to accept that event, another server did, and running a peer's
// whole handover through the room's effects would echo it back into the room. But the user who just
// joined is exactly the fact the other servers have not been told about, and only this server can
// tell them.
func (s *Service) AnnounceDeviceLists(ctx context.Context, userID string) {
	if s.deviceListAnnouncer == nil {
		return
	}
	s.deviceListAnnouncer.AnnounceDeviceListsToFederation(ctx, userID)
}

func New(db *persist.Database, signer events.Signer, presence PresenceNotifier, upgrades UpgradeNotifier, serverName string, log *slog.Logger) (*Service, error) {
	if db == nil {
		return nil, errors.New("rooms: a database is required")
	}
	if signer == nil {
		return nil, errors.New("rooms: a signer is required")
	}
	if serverName == "" {
		return nil, errors.New("rooms: a server name is required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{
		db:       db,
		rooms:    db.Rooms,
		events:   db.Events,
		serve:    redaction.NewServicer(db.Events, db.Rooms, db.StateGroups),
		state:    db.State,
		groups:   db.StateGroups,
		alias:    db.Aliases,
		signer:   signer,
		presence: presence,
		upgrades: upgrades,
		name:     serverName,
		log:      log,
		now:      func() time.Time { return time.Now().UTC() },
	}, nil
}

// WithTx returns a service whose repositories work inside tx, so a multi-step
// operation is atomic.
func (s *Service) WithTx(tx *gorm.DB) *Service {
	rooms, events := s.rooms.WithTx(tx), s.events.WithTx(tx)
	return &Service{
		db:       s.db,
		rooms:    rooms,
		events:   events,
		serve:    redaction.NewServicer(events, rooms, s.groups.WithTx(tx)),
		state:    s.state.WithTx(tx),
		groups:   s.groups.WithTx(tx),
		alias:    s.alias.WithTx(tx),
		signer:   s.signer,
		presence: s.presence,
		upgrades: s.upgrades,
		relayer:  s.relayer,
		name:     s.name,
		log:      s.log,
		now:      s.now,
	}
}

// Create makes a new room and returns its ID. The room and its initial state are
// written in one transaction: a room that exists without its create event would be
// unreadable to anything that looked at it in between.
func (s *Service) Create(ctx context.Context, req CreateRequest) (string, error) {
	rules, _, err := roomversion.Pick(req.RoomVersion)
	if err != nil {
		return "", matrixerr.New(matrixerr.CodeUnsupportedRoomVersion,
			"This server cannot create a room in that room version.").
			WithExtra(map[string]any{"room_version": req.RoomVersion})
	}
	if req.Preset == "" {
		req.Preset = PresetPrivateChat
	}
	if !knownPreset(req.Preset) {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam,
			"The preset %q is not one this server knows.", string(req.Preset))
	}

	roomID, err := s.newRoomID()
	if err != nil {
		return "", err
	}

	createContent := map[string]any{}
	for key, value := range req.CreationContent {
		if key == "creator" || key == "room_version" || key == "predecessor" {
			continue
		}
		createContent[key] = value
	}
	createContent["creator"] = req.Creator
	createContent["room_version"] = rules.ID
	// A predecessor is taken from `creation_content` but checked rather than copied: the
	// specification requires both a room and an event in it, and a client that names only
	// the room is not wrong, it is describing an upgrade it cannot finish by itself.
	if raw, present := req.CreationContent["predecessor"]; present {
		predecessor, err := s.validatePredecessor(ctx, raw)
		if err != nil {
			return "", err
		}
		if predecessor != nil {
			createContent["predecessor"] = predecessor
		}
	}

	initial := presetState(req.Preset, req.Creator)
	if len(req.PowerLevelOverride) > 0 {
		initial = append(initial, StateContent{
			Type:    eventauth.TypePowerLevels,
			Content: MergePowerLevels(presetPowerLevels(req.Creator), req.PowerLevelOverride),
		})
	}
	// The order is the specification's: the preset builds the defaults, `initial_state`
	// overrides them, and the shorthand `name` / `topic` / `avatar_url` parameters
	// override that again. A server that appends the shorthand first lets a client's
	// own state event win over the topic it explicitly asked for.
	initial = append(initial, req.InitialState...)
	if req.Name != "" {
		initial = append(initial, StateContent{Type: "m.room.name", Content: map[string]any{"name": req.Name}})
	}
	if req.Topic != "" {
		initial = append(initial, StateContent{Type: "m.room.topic", Content: topicContent(req.Topic)})
	}
	if req.AvatarURL != "" {
		initial = append(initial, StateContent{Type: "m.room.avatar", Content: map[string]any{"url": req.AvatarURL}})
	}
	initial = deduplicateState(initial)

	// The invitation list is part of creating the room: a room created "with Bob in it"
	// that Bob was never invited to is a different room from the one the client asked for.
	//
	// Local users are invited as part of the room's first state, because that is one
	// transaction and there is nobody else whose signature is needed. Remote users cannot be:
	// their own homeserver has to countersign the invite, and that question can only be asked
	// once the room exists. They are held for afterwards rather than refused — a room whose
	// creation fails because one invitee's server was unreachable would tell the client that
	// nothing happened when a room was in fact created.
	var remoteInvites []string
	for _, invited := range req.Invite {
		if err := matrixid.UserValid(invited); err != nil {
			return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", invited)
		}
		domain, err := matrixid.UserDomain(invited)
		if err != nil {
			return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", invited)
		}
		if !matrixid.IsLocal(domain, s.name) {
			remoteInvites = append(remoteInvites, invited)
			continue
		}
		content := map[string]any{"membership": eventauth.MembershipInvite}
		if req.IsDirect {
			content["is_direct"] = true
		}
		initial = append(initial, StateContent{
			Type: eventauth.TypeMember, StateKey: invited, Content: content,
		})
	}

	// A requested alias is created with the room, in the same transaction: a room
	// that existed without the alias its creator asked for would be a room nobody can
	// find by the name it was given.
	alias := ""
	if req.RoomAliasName != "" {
		alias = "#" + req.RoomAliasName + ":" + s.name
		if err := matrixid.RoomAliasValid(alias); err != nil {
			return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a usable room alias.", req.RoomAliasName)
		}
		if _, domain, err := matrixid.SplitID(matrixid.SigilAlias, alias); err != nil || !matrixid.IsLocal(domain, s.name) {
			return "", matrixerr.Newf(matrixerr.CodeInvalidParam,
				"A room alias this server creates must be on this server's name, not %q.", alias)
		}
	}

	var created string
	err = s.db.Tx(ctx, func(tx *gorm.DB) error {
		id, err := s.WithTx(tx).createRoom(ctx, rules, roomID, req.Creator, createContent, initial, req.Published)
		if err != nil {
			return err
		}
		if alias != "" {
			if err := s.WithTx(tx).alias.Create(ctx, &persist.RoomAlias{
				Alias: alias, RoomID: id, Creator: req.Creator,
			}); err != nil {
				if errors.Is(err, persist.ErrConflict) {
					return matrixerr.Newf(matrixerr.CodeDuplicateAlias, "The alias %q is already in use.", alias)
				}
				return fmt.Errorf("rooms: cannot store the room alias: %w", err)
			}
			// A room created with a name says so itself. The alias row is written first
			// because the canonical alias event is checked against it: a room may only
			// name itself by an alias that resolves to it, and this one does now.
			if _, err := s.WithTx(tx).send(ctx, SendRequest{
				RoomID:    id,
				Sender:    req.Creator,
				EventType: canonicalAliasEventType,
				IsState:   true,
				Content:   map[string]any{"alias": alias},
			}); err != nil {
				return fmt.Errorf("rooms: cannot record the room's own alias: %w", err)
			}
		}
		created = id
		return nil
	})
	if err != nil {
		return "", err
	}
	// The creator's join is written by createRoom rather than by Send, so it is
	// announced here. A room whose creator is invisible to nobody is a room where the
	// first member is told nothing about anybody, including themselves being seen.
	s.announceArrival(ctx, created, req.Creator)
	if err := s.markDirect(ctx, req.Creator, created, req.Invite, req.IsDirect); err != nil {
		s.log.Warn("rooms: the room was created but not recorded as a direct message",
			"room_id", created, "error", err)
	}
	for _, invited := range remoteInvites {
		if _, err := s.inviteRemote(ctx, MembershipChange{
			RoomID: created, Target: invited, Sender: req.Creator,
			Membership: eventauth.MembershipInvite, IsDirect: req.IsDirect,
		}); err != nil {
			// The room stands. An invite the other server refused is reported in the log and
			// left for the client to retry through /invite, where the answer is a status it
			// can act on; un-creating a room that already exists is not an option, and
			// pretending the invite worked is worse than leaving it visibly undone.
			s.log.Warn("rooms: a room was created but a remote invitee was not invited",
				"room_id", created, "invited", invited, "error", err)
		}
	}
	return created, nil
}

// markDirect records the room in the creator's `m.direct` account data against each user the room
// was created to invite.
//
// The room itself does not carry this. A room can be a direct message for one pair of users and an
// ordinary room for another who joins later, and the flag belongs to the person who opened the
// conversation — which is why the specification puts it in the inviter's account data and in the
// invitation, and not in the room's state. An existing mapping is kept and extended: account data
// is a current value, and overwriting it would lose the other conversations the user has.
func (s *Service) markDirect(ctx context.Context, creator, roomID string, invited []string, direct bool) error {
	if !direct || len(invited) == 0 {
		return nil
	}
	mapping := map[string][]string{}
	row, err := s.db.AccountData.Get(ctx, creator, "m.direct")
	if err != nil {
		if !notFound(err) {
			return err
		}
	} else if err := json.Unmarshal([]byte(row.Content), &mapping); err != nil {
		// Account data is written by clients, so it can be anything a client put there. A
		// mapping this server cannot read is replaced rather than obeyed: the alternative is a
		// room the creator opened as a direct message and cannot find as one.
		s.log.Warn("rooms: the existing m.direct account data is not readable and will be replaced",
			"user_id", creator, "error", err)
		mapping = map[string][]string{}
	}
	for _, user := range invited {
		seen := false
		for _, existing := range mapping[user] {
			if existing == roomID {
				seen = true
				break
			}
		}
		if !seen {
			mapping[user] = append(mapping[user], roomID)
		}
	}
	raw, err := json.Marshal(mapping)
	if err != nil {
		return fmt.Errorf("rooms: cannot encode the direct message mapping: %w", err)
	}
	if _, err := s.db.AccountData.Put(ctx, creator, "m.direct", string(raw)); err != nil {
		return fmt.Errorf("rooms: cannot record the room as a direct message: %w", err)
	}
	return nil
}

// createRoom writes the room row and then its initial state, in dependency order.
// Each initial event cites the one before it, so the room starts as a short chain
// rather than as several roots.
func (s *Service) createRoom(ctx context.Context, rules roomversion.Rules, roomID, creator string, createContent map[string]any, initial []StateContent, published bool) (string, error) {
	// The directory lists a room by name and topic, so the row starts with the values
	// the room is created with. Later changes follow the state through
	// refreshDirectoryText.
	name, topic := "", ""
	for _, entry := range initial {
		if entry.StateKey != "" {
			continue
		}
		switch entry.Type {
		case "m.room.name":
			if text, ok := entry.Content["name"].(string); ok {
				name = text
			}
		case "m.room.topic":
			if text, ok := entry.Content["topic"].(string); ok {
				topic = text
			}
		}
	}
	if err := s.rooms.Create(ctx, &persist.Room{
		RoomID:      roomID,
		RoomVersion: rules.ID,
		Creator:     creator,
		CreatedTS:   s.now().UnixMilli(),
		Published:   published,
		Name:        name,
		Topic:       topic,
	}); err != nil {
		return "", fmt.Errorf("rooms: cannot store the room: %w", err)
	}

	built := []map[string]any{
		newStateEvent(s.name, roomID, creator, eventauth.TypeCreate, "", createContent, s.now()),
		newStateEvent(s.name, roomID, creator, eventauth.TypeMember, creator,
			map[string]any{"membership": eventauth.MembershipJoin}, s.now()),
	}
	for _, requested := range initial {
		built = append(built, newStateEvent(s.name, roomID, creator, requested.Type, requested.StateKey, requested.Content, s.now()))
	}

	snapshot := newMemorySnapshot()
	// The initial state is built in a chain, and in a room version 1 or 2 room each event has to
	// name the reference hash of the event before it — which is not in storage yet, because the room
	// and its state are committed together. So the events built in this pass are kept at hand.
	known := make(map[string]map[string]any, len(built))
	var previous string
	var lastStream int64
	for index, event := range built {
		if previous == "" {
			event["prev_events"] = []any{}
		} else {
			event["prev_events"] = []any{previous}
		}
		event["depth"] = int64(index + 1)
		event["auth_events"] = toAnySlice(eventauth.SelectEventIDs(rules, event, snapshot))
		if err := s.nameAndReference(event, ctx, rules, known); err != nil {
			return "", err
		}

		// Seal first, because the event ID is a hash of the sealed event.
		if err := events.Seal(s.name, rules, event, s.signer); err != nil {
			return "", fmt.Errorf("rooms: cannot sign the initial state: %w", err)
		}
		eventID, err := events.EventID(rules, event)
		if err != nil {
			return "", fmt.Errorf("rooms: cannot compute the event ID of the initial state: %w", err)
		}
		event["event_id"] = eventID
		known[eventID] = event

		// Authorising our own construction is not busywork. It is the only thing
		// standing between a bug in this builder and a room that every other server
		// refuses as soon as it federates.
		if err := eventauth.Check(rules, event, snapshot, eventauth.Opts{}); err != nil {
			return "", fmt.Errorf("rooms: the initial state we built is not authorised: %w", err)
		}

		stream, err := s.events.NextStreamOrdering(ctx)
		if err != nil {
			return "", err
		}
		lastStream = stream
		if err := s.persistEvent(ctx, event, stream); err != nil {
			return "", err
		}
		snapshot.add(event)
		previous = eventID
	}

	if err := s.rooms.SetLatestStream(ctx, roomID, lastStream); err != nil {
		return "", err
	}
	s.log.Debug("room created",
		slog.String("room_id", roomID),
		slog.String("room_version", rules.ID),
		slog.String("creator", creator))
	return roomID, nil
}

// Send appends an event to a room and returns its event ID. A repeated transaction ID
// from the same device returns the event ID produced the first time, which is what
// makes `PUT /send/{type}/{txn}` safe to retry.
func (s *Service) Send(ctx context.Context, req SendRequest) (string, error) {
	var eventID string
	err := s.db.Tx(ctx, func(tx *gorm.DB) error {
		id, err := s.WithTx(tx).send(ctx, req)
		if err != nil {
			return err
		}
		eventID = id
		return nil
	})
	if err != nil {
		return "", err
	}
	s.applySideEffects(ctx, eventID)
	return eventID, nil
}

func (s *Service) send(ctx context.Context, req SendRequest) (string, error) {
	if req.TxnID != "" && req.DeviceID != "" {
		existing, err := s.events.TxnLookup(ctx, req.DeviceID, req.RoomID, req.TxnID)
		if err != nil && !notFound(err) {
			return "", fmt.Errorf("rooms: cannot look up the transaction: %w", err)
		}
		if existing != nil {
			return existing.EventID, nil
		}
	}

	room, err := s.rooms.ByRoomID(ctx, req.RoomID)
	if err != nil {
		if notFound(err) {
			return "", matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", req.RoomID)
		}
		return "", fmt.Errorf("rooms: cannot load the room: %w", err)
	}
	rules, ok := roomversion.Get(room.RoomVersion)
	if !ok {
		return "", matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot authorise events in.",
			req.RoomID, room.RoomVersion).WithExtra(map[string]any{"room_version": room.RoomVersion})
	}

	snapshot, err := s.loadState(ctx, req.RoomID)
	if err != nil {
		return "", err
	}
	prev, err := s.prevEvents(ctx, req.RoomID)
	if err != nil {
		return "", err
	}
	depth, err := s.depth(ctx, prev)
	if err != nil {
		return "", err
	}

	event := map[string]any{
		"type":    req.EventType,
		"room_id": req.RoomID,
		"sender":  req.Sender,
		// A PDU names the server that made it. The signature is keyed by that server's key, and
		// a room's exclusion of servers, its auth rules and every other server's decision whether
		// to believe this one all read the name from here; an event without it is a PDU that
		// cannot be traced to who sent it.
		"origin":           s.name,
		"content":          valueOrEmpty(req.Content),
		"prev_events":      toAnySlice(prev),
		"depth":            depth,
		"origin_server_ts": s.now().UnixMilli(),
		"auth_events":      []any{},
	}
	if req.IsState {
		event["state_key"] = req.StateKey
	}
	for field, value := range req.Extra {
		event[field] = value
	}
	event["auth_events"] = toAnySlice(eventauth.SelectEventIDs(rules, event, snapshot))
	if err := s.nameAndReference(event, ctx, rules, nil); err != nil {
		return "", err
	}

	// A room may only advertise aliases this server can resolve, so the alias claims
	// in m.room.canonical_alias are checked before the event is signed.
	if req.IsState && req.EventType == canonicalAliasEventType {
		if err := s.checkCanonicalAlias(ctx, req.RoomID, valueOrEmpty(req.Content)); err != nil {
			return "", err
		}
	}

	if err := eventauth.Check(rules, event, snapshot, eventauth.Opts{}); err != nil {
		return "", s.refused(err, req)
	}
	if err := events.Seal(s.name, rules, event, s.signer); err != nil {
		return "", fmt.Errorf("rooms: cannot sign the event: %w", err)
	}
	eventID, err := events.EventID(rules, event)
	if err != nil {
		return "", fmt.Errorf("rooms: cannot compute the event ID: %w", err)
	}
	event["event_id"] = eventID

	stream, err := s.events.NextStreamOrdering(ctx)
	if err != nil {
		return "", err
	}
	if err := s.persistEvent(ctx, event, stream); err != nil {
		return "", err
	}
	if req.TxnID != "" && req.DeviceID != "" {
		// The name the client gave this send travels with the event, so that the client can
		// recognise it later in a timeline. The transaction row below is what makes a retry
		// idempotent; this is the copy the client is shown.
		if err := s.events.SetTxnID(ctx, eventID, req.TxnID); err != nil {
			return "", fmt.Errorf("rooms: cannot record the transaction ID of an event: %w", err)
		}
		if err := s.events.TxnSave(ctx, &persist.EventTxn{
			DeviceID: req.DeviceID, RoomID: req.RoomID, TxnID: req.TxnID, EventID: eventID,
		}); err != nil {
			if !errors.Is(err, persist.ErrConflict) {
				return "", fmt.Errorf("rooms: cannot record the transaction: %w", err)
			}
			// A concurrent retry won the race; its event is the answer to this one.
			existing, lookupErr := s.events.TxnLookup(ctx, req.DeviceID, req.RoomID, req.TxnID)
			if lookupErr != nil || existing == nil {
				return "", fmt.Errorf("rooms: a transaction collision left no stored event: %v", err)
			}
			return existing.EventID, nil
		}
	}
	if err := s.rooms.SetLatestStream(ctx, req.RoomID, stream); err != nil {
		return "", err
	}
	return eventID, nil
}

// refused turns an authorisation refusal into the Matrix error a client sees. The
// rule text goes to the server log; the client gets M_FORBIDDEN, which is what the
// specification defines and does not advertise which rule to probe.
func (s *Service) refused(err error, req SendRequest) error {
	s.log.Debug("event refused by the authorisation rules",
		slog.String("room_id", req.RoomID),
		slog.String("sender", req.Sender),
		slog.String("type", req.EventType),
		slog.String("reason", err.Error()))
	if failure, ok := eventauth.AsFailure(err); ok {
		return matrixerr.New(matrixerr.CodeForbidden, "You are not allowed to send that event in this room.").
			WithExtra(map[string]any{"rule": failure.Rule})
	}
	return matrixerr.New(matrixerr.CodeForbidden, "That event could not be authorised in this room.")
}

// persistEvent writes the event, its reference edges, its state entry and the state
// that authorised it.
func (s *Service) persistEvent(ctx context.Context, event map[string]any, stream int64) error {
	canonical, err := events.Canonical(event)
	if err != nil {
		return fmt.Errorf("rooms: cannot encode the event: %w", err)
	}
	eventID, _ := event["event_id"].(string)
	roomID, _ := event["room_id"].(string)
	eventType, _ := event["type"].(string)

	row := &persist.Event{
		EventID:        eventID,
		RoomID:         roomID,
		Type:           eventType,
		Sender:         stringOr(event["sender"]),
		Depth:          int64Of(event["depth"]),
		OriginServerTS: int64Of(event["origin_server_ts"]),
		ReceivedTS:     s.now().UnixMilli(),
		JSON:           canonical,
		StreamOrdering: stream,
	}
	if stateKey, ok := event["state_key"].(string); ok {
		key := stateKey
		row.StateKey = &key
	}
	if err := s.events.Insert(ctx, row); err != nil {
		if errors.Is(err, persist.ErrConflict) {
			// The same event ID twice inside one transaction means an ID collision,
			// which is a server fault rather than something the client caused.
			return fmt.Errorf("rooms: event ID %q already exists", eventID)
		}
		return fmt.Errorf("rooms: cannot store the event: %w", err)
	}

	for _, reference := range events.ReferencedIDs(event, "prev_events") {
		if err := s.events.Link(ctx, eventID, reference, persist.EdgePrev); err != nil {
			return fmt.Errorf("rooms: cannot record a prev_event: %w", err)
		}
	}
	authIDs := events.ReferencedIDs(event, "auth_events")
	for _, reference := range authIDs {
		if err := s.events.Link(ctx, eventID, reference, persist.EdgeAuth); err != nil {
			return fmt.Errorf("rooms: cannot record an auth_event: %w", err)
		}
	}
	if len(authIDs) > 0 {
		if err := s.state.RememberAuthContext(ctx, eventID, authIDs); err != nil {
			return fmt.Errorf("rooms: cannot record the authorisation context: %w", err)
		}
	}
	if row.IsState() {
		if err := s.state.Set(ctx, &persist.RoomState{
			RoomID: roomID, Type: eventType, StateKey: *row.StateKey, EventID: eventID,
		}); err != nil {
			return fmt.Errorf("rooms: cannot update the room state: %w", err)
		}
		// A published room is listed under its name and topic, so those have to follow
		// the state rather than lag behind it.
		content, _ := event["content"].(map[string]any)
		if err := s.refreshDirectoryText(ctx, roomID, eventType, content); err != nil {
			return fmt.Errorf("rooms: cannot update the room's directory entry: %w", err)
		}
	}
	// What state this event was sent against is recorded as well as what state the room
	// is in now. Current state answers "where is this room"; the per-event snapshot answers
	// "what did each branch think", and a fork can only be settled from the second.
	if err := s.recordStateAt(ctx, event, row); err != nil {
		return err
	}
	// And the room's current state is wherever the branches say it is, which after a fork
	// is not wherever the last write left it.
	if err := s.reconcileCurrentState(ctx, row, len(events.ReferencedIDs(event, "prev_events"))); err != nil {
		return err
	}
	// A redaction and the destruction it causes are stored together, and an event that
	// arrives late gets the chance to settle a redaction that has been waiting for it.
	if err := s.noteRedaction(ctx, event); err != nil {
		return err
	}
	if err := s.applyWaitingRedactions(ctx, eventID); err != nil {
		return err
	}
	return nil
}

// prevEvents returns the forward extremities a new event should cite.
func (s *Service) prevEvents(ctx context.Context, roomID string) ([]string, error) {
	extremities, err := s.events.ForwardExtremities(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot load the forward extremities: %w", err)
	}
	if len(extremities) <= maxPrevEvents {
		return extremities, nil
	}
	// More extremities than an event may cite means this room has forks that have not
	// been merged. Citing the newest keeps sending working, and logging it keeps the
	// situation from being invisible.
	s.log.Warn("a room has more forward extremities than an event may cite",
		slog.String("room_id", roomID), slog.Int("extremities", len(extremities)))
	sort.Strings(extremities)
	return extremities[len(extremities)-maxPrevEvents:], nil
}

// depth is one more than the shallowest predecessor, which is how Matrix orders
// history without trusting insertion order.
func (s *Service) depth(ctx context.Context, prev []string) (int64, error) {
	if len(prev) == 0 {
		return 1, nil
	}
	rows, err := s.events.ByIDs(ctx, prev)
	if err != nil {
		return 0, fmt.Errorf("rooms: cannot load the predecessor events: %w", err)
	}
	if len(rows) != len(prev) {
		return 0, errors.New("rooms: a predecessor event is missing while computing depth")
	}
	best := int64(-1)
	for _, row := range rows {
		if best < 0 || row.Depth < best {
			best = row.Depth
		}
	}
	return best + 1, nil
}

// loadState reads the room's current state into a snapshot the rules can query.
//
// Holding the state in memory for one operation is a deliberate SQLite-era decision:
// a room's state is small next to its history, and the alternative — a query for
// every state lookup inside the rules — is slower and harder to keep consistent
// mid-operation. A room large enough for this to hurt is a scalability item, not a
// correctness one.
func (s *Service) loadState(ctx context.Context, roomID string) (*dbSnapshot, error) {
	rows, err := s.state.All(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("rooms: cannot load the room state: %w", err)
	}
	snapshot := &dbSnapshot{
		events: s.events, ctx: ctx, roomID: roomID,
		state: map[eventauth.Key]map[string]any{}, byID: map[string]map[string]any{},
	}
	for _, row := range rows {
		event, err := snapshot.load(row.EventID)
		if err != nil {
			if notFound(err) {
				// A state row pointing at an event we do not have. Skipping it is
				// safer than authorising against a hole in the state.
				continue
			}
			return nil, fmt.Errorf("rooms: cannot load a state event: %w", err)
		}
		snapshot.state[eventauth.Key{Type: row.Type, StateKey: row.StateKey}] = event
	}
	return snapshot, nil
}

// newRoomID makes a room ID whose localpart is opaque, as the specification requires.
func (s *Service) newRoomID() (string, error) {
	local, err := matrixid.GenerateRandomString(nil, 22)
	if err != nil {
		return "", fmt.Errorf("rooms: cannot generate a room ID: %w", err)
	}
	roomID := "!" + local + ":" + s.name
	if err := matrixid.RoomIDValid(roomID); err != nil {
		return "", fmt.Errorf("rooms: generated an invalid room ID: %w", err)
	}
	return roomID, nil
}

// presetState returns the initial state for a preset.
//
// The specification names the three presets but leaves their contents to the
// implementation, so these are our choices and they follow Synapse where the effect
// is observable: a public room is world readable and open to joins; private rooms
// are invite-only with history shared with members; the trusted variant lets every
// member invite. docs/requirements.md records this as an interpretation decision.
func presetState(preset Preset, creator string) []StateContent {
	joinRule := "invite"
	history := "shared"
	levels := presetPowerLevels(creator)
	switch preset {
	case PresetPublicChat:
		joinRule = "public"
		history = "world_readable"
	case PresetTrustedPrivateChat:
		levels["invite"] = 0
	case PresetPrivateChat:
	}
	// The power levels come first. These three are siblings in the room's graph — a creation
	// batch is one event deep, and none of them cites another — so the order is not something
	// the room's own events record, and it shows up only in the sequence a peer is handed when
	// it asks to fill a hole in this room's history. Two things still argue for this order:
	// every state event after it is authorised against the power levels, so the event that
	// says who may write should be the one this server holds before it writes anything else;
	// and a peer walking the history oldest-first sees the authority of the room before the
	// state that authority permitted.
	return []StateContent{
		{Type: eventauth.TypePowerLevels, Content: levels},
		{Type: eventauth.TypeJoinRules, Content: map[string]any{"join_rule": joinRule}},
		{Type: "m.room.history_visibility", Content: map[string]any{"history_visibility": history}},
	}
}

// presetPowerLevels is the power level layout a new room starts with.
func presetPowerLevels(creator string) map[string]any {
	return map[string]any{
		"users":          map[string]any{creator: 100},
		"users_default":  0,
		"events_default": 0,
		"state_default":  50,
		"invite":         0,
		"kick":           50,
		"ban":            50,
		"redact":         50,
		"events":         map[string]any{},
		"notifications":  map[string]any{"room": 50},
	}
}

// MergePowerLevels overlays a client's override on the preset. The nesting is one
// level deep on purpose: the nested power level maps are users, events and
// notifications, and replacing those wholesale would let an override erase the
// creator.
func MergePowerLevels(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range override {
		nestedBase, baseOK := out[key].(map[string]any)
		nestedOverride, overrideOK := value.(map[string]any)
		if baseOK && overrideOK {
			merged := make(map[string]any, len(nestedBase)+len(nestedOverride))
			for nestedKey, nestedValue := range nestedBase {
				merged[nestedKey] = nestedValue
			}
			for nestedKey, nestedValue := range nestedOverride {
				merged[nestedKey] = nestedValue
			}
			out[key] = merged
			continue
		}
		out[key] = value
	}
	return out
}

func knownPreset(preset Preset) bool {
	switch preset {
	case PresetPrivateChat, PresetPublicChat, PresetTrustedPrivateChat:
		return true
	default:
		return false
	}
}

// newStateEvent builds the map form of a state event, before it is sealed. The origin server is
// named because a PDU has to say who made it: see the note in [Service.send].
func newStateEvent(origin, roomID, sender, eventType, stateKey string, content map[string]any, at time.Time) map[string]any {
	return map[string]any{
		"type":             eventType,
		"room_id":          roomID,
		"sender":           sender,
		"origin":           origin,
		"content":          valueOrEmpty(content),
		"depth":            int64(1),
		"origin_server_ts": at.UnixMilli(),
		"prev_events":      []any{},
		"auth_events":      []any{},
		"state_key":        stateKey,
	}
}

func valueOrEmpty(content map[string]any) map[string]any {
	if content == nil {
		return map[string]any{}
	}
	return content
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func anyStrings(value any) []string {
	switch typed := value.(type) {
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	case []string:
		return typed
	default:
		return nil
	}
}

func stringOr(value any) string {
	text, _ := value.(string)
	return text
}

// int64Of reads a number out of an event object. Events this server built itself carry
// Go integers; an event that came off the wire carries json.Number, because the decoder
// that preserves the canonical form does not turn numbers into floats. Missing that case
// would quietly store a depth and timestamp of zero.
func int64Of(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
