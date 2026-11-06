package rooms

import (
	"context"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
)

// ErrRemoteTarget names a membership change this server cannot carry out because the
// user it names lives on another homeserver. Changing their membership means telling
// that server, which is federation, and federation is not implemented yet.
//
// It is a distinct error so the endpoint can answer 501 rather than 403: saying "you
// are not allowed" would report a permission problem where there is a missing feature,
// and an administrator reading it would look in the wrong place.
var ErrRemoteTarget = matrixerr.New(matrixerr.CodeUnknown,
	"Changing the membership of a user who lives on another homeserver has to be carried out by that server, and this server does not implement that part of federation yet.")

// MembershipChange asks for an m.room.member state event.
//
// The membership value and the target are all the transport supplies: whether the
// action is permitted is decided by the authorisation rules, not by a per-endpoint
// permission check, so that invite, kick and ban cannot drift apart from what the
// room's power levels actually say.
type MembershipChange struct {
	RoomID string
	// Target is the user the membership is about, which becomes the state key.
	Target string
	// Sender is the user performing the action. For join, leave and knock it must be
	// the same as Target, which the authorisation rules enforce.
	Sender     string
	DeviceID   string
	Membership string
	Reason     string
	// Via names the servers a client believes can reach the room. Only a join uses them:
	// a room ID names the server that created the room, which is not always one that still
	// holds it, and the Client-Server API lets a client say otherwise.
	Via []string
	// Extra carries the additional keys a client put in a join or knock body. The
	// Client-Server API copies them into the member event's content, which is how a
	// client can attach its own information to the fact that it joined. `membership` and
	// `reason` are set by the server afterwards, so a body cannot forge a membership.
	// Join and knock only: the other membership endpoints define a fixed body.
	Extra map[string]any
}

// SetMembership writes an m.room.member event and returns its event ID.
func (s *Service) SetMembership(ctx context.Context, change MembershipChange) (string, error) {
	if !knownMembership(change.Membership) {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam,
			"%q is not a membership this server knows.", change.Membership)
	}
	if err := matrixid.UserValid(change.Target); err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", change.Target)
	}
	if err := matrixid.RoomIDValid(change.RoomID); err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", change.RoomID)
	}

	// A join in a room this server does not hold is a different operation: it is carried out
	// by a server that does hold the room, and this server's part is to speak for its own
	// user and sign. Deciding that here, before any event is built, keeps a half-written
	// membership event out of a local room.
	if elsewhere, err := s.joinElsewhere(ctx, change); err != nil {
		return "", err
	} else if elsewhere {
		return s.joinRemote(ctx, change)
	}

	// A local action only. Deciding this here, before any event is built, keeps a
	// half-written membership event out of the room.
	domain, err := matrixid.UserDomain(change.Target)
	if err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", change.Target)
	}
	if !matrixid.IsLocal(domain, s.name) {
		// An invite is the one membership change this server can carry out for a user who
		// lives elsewhere, because this server holds the room and can build the event; the
		// invited user's server signs it as well. The other changes belong to the other
		// server: it is the one that speaks for that user, and a leave or a ban written here
		// would be this server claiming to be somebody else's homeserver.
		if change.Membership == eventauth.MembershipInvite {
			return s.inviteRemote(ctx, change)
		}
		return "", ErrRemoteTarget
	}

	// Kicking is only meaningful for someone who is actually in the room. The event
	// authorisation rules do not say this, because in the room version rules a leave
	// event for a user with no membership is not itself invalid; the Client-Server API
	// endpoint is where "you cannot kick someone who is not here" belongs, and
	// Synapse, Dendrite and the sytest lineage all refuse it. Without this, a room
	// owner could write a leave event for a stranger and a client would see it as a
	// successful kick.
	if change.Membership == eventauth.MembershipLeave && change.Sender != change.Target {
		current, err := s.MembershipOf(ctx, change.RoomID, change.Target)
		if err != nil {
			return "", err
		}
		switch current {
		case eventauth.MembershipJoin, eventauth.MembershipInvite,
			eventauth.MembershipKnock, eventauth.MembershipBan:
		default:
			return "", matrixerr.Newf(matrixerr.CodeForbidden,
				"%s is not a member of this room, so there is nothing to kick.", change.Target)
		}
	}

	content := make(map[string]any, len(change.Extra)+2)
	for key, value := range change.Extra {
		content[key] = value
	}
	content["membership"] = change.Membership
	if change.Reason != "" {
		content["reason"] = change.Reason
	}
	return s.Send(ctx, SendRequest{
		RoomID:    change.RoomID,
		Sender:    change.Sender,
		DeviceID:  change.DeviceID,
		EventType: eventauth.TypeMember,
		StateKey:  change.Target,
		IsState:   true,
		Content:   content,
	})
}

// knownMembership accepts only the five memberships the specification defines. An
// unknown one would be stored as current state and then refused by every other server.
func knownMembership(membership string) bool {
	switch membership {
	case eventauth.MembershipJoin, eventauth.MembershipInvite, eventauth.MembershipLeave,
		eventauth.MembershipBan, eventauth.MembershipKnock:
		return true
	default:
		return false
	}
}

// MembershipOf returns the current membership of a user in a room, as this server
// records it. A room with no member event for the user reports the empty string, which
// is different from "banned": there is no state to act on.
func (s *Service) MembershipOf(ctx context.Context, roomID, userID string) (string, error) {
	row, err := s.state.Get(ctx, roomID, eventauth.TypeMember, userID)
	if err != nil {
		if notFound(err) {
			return "", nil
		}
		return "", err
	}
	event, err := s.eventJSON(ctx, row.EventID)
	if err != nil {
		return "", err
	}
	membership, _ := event["content"].(map[string]any)["membership"].(string)
	return membership, nil
}
