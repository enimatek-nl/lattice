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
var ErrRemoteTarget = matrixerr.New(matrixerr.CodeUnrecognized,
	"Changing the membership of a user on another homeserver requires federation, which this server does not implement yet.")

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

	// A local action only. Deciding this here, before any event is built, keeps a
	// half-written membership event out of the room.
	domain, err := matrixid.UserDomain(change.Target)
	if err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", change.Target)
	}
	if !matrixid.IsLocal(domain, s.name) {
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

	content := map[string]any{"membership": change.Membership}
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
