package rooms

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// RemoteJoin carries one of this server's users into a room this server does not hold. The
// federation stack implements it: the join has to be signed by this server, sent to whichever
// server already holds the room, and what comes back has to be judged against keys this package
// knows nothing about.
//
// `destinations` are the servers the client named as able to reach the room. A room ID carries
// the name of the server that created it, which is often but not always a server that still
// holds it; the client's hints are how a room whose creator has gone quiet is still joined.
type RemoteJoin interface {
	Join(ctx context.Context, roomID, userID string, destinations []string) (RemoteJoinResult, error)
}

// RemoteJoinResult says what a completed remote join brought here.
type RemoteJoinResult struct {
	// EventID is this server's own membership event — the one it signed and the resident
	// server accepted.
	EventID string
	// State and Outliers count what was learned about the room. An outlier is a handed-over
	// event whose predecessors are not held here; its state counts, but this server cannot
	// yet say where in the history it sits.
	State    int
	Outliers int
}

// ErrRemoteJoinUnavailable says a local user asked to join a room held by another homeserver
// and this server has no federation stack to carry the ask. It is distinct from
// [ErrRemoteTarget] and from a missing room because the reason matters to whoever reads it: a
// room that does not exist and a server that cannot reach one are different problems, and
// answering the second with a 404 would send an administrator looking at the wrong thing.
var ErrRemoteJoinUnavailable = matrixerr.New(matrixerr.CodeUnknown,
	"Joining a room held by another homeserver needs federation, which this server is not running.")

// SetRemoteJoin installs the federation stack's join handshake. Without it a local user joining
// a remote room is refused rather than half-attempted.
func (s *Service) SetRemoteJoin(join RemoteJoin) { s.remoteJoin = join }

// roomKnown says this server holds the room. Only the room's existence is asked: a room held
// here is a room this server can carry a membership change for on its own.
func (s *Service) roomKnown(ctx context.Context, roomID string) (bool, error) {
	if _, err := s.rooms.ByRoomID(ctx, roomID); err == nil {
		return true, nil
	} else if !notFound(err) {
		return false, fmt.Errorf("rooms: cannot load the room a membership change names: %w", err)
	}
	return false, nil
}

// joinRemote takes a local user into a room this server does not hold.
//
// The room's ID is checked before anything is sent. A room ID whose name is this server's own
// is a room this server should know about; if it does not, it is not somebody else's room, and
// asking a stranger to hand one over would let that stranger invent a room under this server's
// name and have this server store it.
// joinThroughNeighbours carries out over federation a join this server could not authorise in a room
// it does hold.
//
// Holding a room and being able to let somebody into it are different things. A restricted room names
// the rooms whose members may walk in uninvited, and the member who could vouch for the joiner may
// live on another server: this server can sign only for its own members, and none of them holds the
// invitation power. That is what M_UNABLE_TO_GRANT_JOIN reports, and the Client-Server API\u2019s answer is
// that the client asks again with a different server named. Going out on this server\u2019s own to find a
// neighbour would answer the client\u2019s question with another server\u2019s judgement the client never asked
// for — and it would be another server\u2019s idea of who is still in the allow room, which can be a moment
// out of date and turn a refusal the room means into a grant the room does not. So the servers asked
// here are the ones the client named plus the ones this server knows hold the room — which it knows
// precisely because it holds the room itself. A server that does *not* hold the room does not get that
// licence: it asks what it was told to ask, and a join its candidate list cannot authorise fails so
// that the client can ask again elsewhere. See [Joiner.candidates].
func (s *Service) joinThroughNeighbours(ctx context.Context, change MembershipChange, cause error) (string, bool) {
	if s.remoteJoin == nil {
		return "", false
	}
	if problem, ok := matrixerr.As(cause); !ok || problem.Code != matrixerr.CodeUnableToGrantJoin {
		return "", false
	}
	others := make([]string, 0, len(change.Via)+4)
	seen := map[string]bool{}
	add := func(server string) {
		if server == "" || matrixid.IsLocal(server, s.name) || seen[server] {
			return
		}
		seen[server] = true
		others = append(others, server)
	}
	for _, server := range change.Via {
		add(strings.TrimSpace(server))
	}
	// The room's own member servers are candidates only while this server is still *in* the room —
	// a user of ours is joined there. That is what makes the room's server list something this server
	// knows rather than something it guesses, and it is also the difference between carrying out a
	// join on behalf of a member of the room and pushing a join at a room that has moved on without
	// us. A server with nobody joined in the room asks the servers it was told to ask and no others,
	// which is how the refusal the room means reaches the client.
	if inRoom, err := s.stillTracksRoom(ctx, change.RoomID); err == nil && inRoom {
		if servers, err := s.ServersInRoom(ctx, change.RoomID); err == nil {
			for _, server := range servers {
				add(server)
			}
		}
	}
	if len(others) == 0 {
		// Nobody else here to ask. The room's answer is this server's answer after all.
		return "", false
	}
	change.Via = others
	eventID, err := s.joinRemote(ctx, change)
	if err != nil {
		s.log.Warn("rooms: a join this server could not authorise failed over and failed there too",
			slog.String("room_id", change.RoomID), slog.String("user_id", change.Target),
			slog.Any("error", err))
		return "", false
	}
	return eventID, true
}

func (s *Service) joinRemote(ctx context.Context, change MembershipChange) (string, error) {
	if s.remoteJoin == nil {
		return "", ErrRemoteJoinUnavailable
	}
	_, domain, err := matrixid.SplitID(matrixid.SigilRoom, change.RoomID)
	if err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", change.RoomID)
	}
	if matrixid.IsLocal(domain, s.name) {
		return "", matrixerr.Newf(matrixerr.CodeNotFound, "Room %q is not known to this server.", change.RoomID)
	}
	// The user's own extras (the Client-Server API copies a join body's keys into the event)
	// travel with the ask; the federation handshake supplies the room's own shape around them.
	result, err := s.remoteJoin.Join(ctx, change.RoomID, change.Target, change.Via)
	if err != nil {
		return "", err
	}
	s.log.Info("rooms: a local user joined a room held elsewhere",
		slog.String("room_id", change.RoomID), slog.String("user_id", change.Target),
		slog.String("event_id", result.EventID),
		slog.Int("state_events", result.State), slog.Int("unlinked", result.Outliers))
	return result.EventID, nil
}

// joinElsewhere asks whether a membership change has to leave this server. It answers whether
// the caller should hand the change to the federation stack rather than write an event here.
func (s *Service) joinElsewhere(ctx context.Context, change MembershipChange) (bool, error) {
	if change.Membership != eventauth.MembershipJoin {
		return false, nil
	}
	known, err := s.roomKnown(ctx, change.RoomID)
	return !known, err
}
