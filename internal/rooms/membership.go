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

// ErrRemoteSender names a membership event this server would have to sign on behalf of a
// user who lives on another homeserver. A kick or a ban carried out by a moderator who
// lives here is this server's own act and this server's own signature; an act attributed
// to somebody else's user is that homeserver's, and signing it here would be this server
// putting words in another's mouth — which no room version would be able to tell apart
// from the real thing.
var ErrRemoteSender = matrixerr.New(matrixerr.CodeForbidden,
	"A membership change sent by a user who lives on another homeserver is signed by that homeserver, not by this one.")

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
	// IsDirect marks an invitation as one opened as a private conversation, which the
	// specification records in the member event so that the invited user's server and devices
	// see what the inviter meant. Only an invitation uses it.
	IsDirect bool
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
	if elsewhere, err := s.carriedElsewhere(ctx, change); err != nil {
		return "", err
	} else if elsewhere {
		return s.carryRemote(ctx, change)
	}

	// A local action only. Deciding this here, before any event is built, keeps a
	// half-written membership event out of the room.
	domain, err := matrixid.UserDomain(change.Target)
	if err != nil {
		return "", matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", change.Target)
	}
	if !matrixid.IsLocal(domain, s.name) {
		// The user the membership is about lives elsewhere. Three different questions hide in
		// that one fact, and they answer differently.
		//
		// An invitation is built here and signed twice: this server holds the room and writes
		// the event, and the invited user's server countersigns it, because an invitation is a
		// claim made by both — the room that opens the door and the homeserver that will keep
		// the key.
		//
		// A kick, a ban or an unban of a user who lives elsewhere is this server acting in this
		// server's own room, and the room's authorisation rules are what decide it. In every
		// room version that has them, a member event about *another* user is judged by the
		// sender's power to kick, ban or unban, not by who the target's homeserver is: the
		// signature that carries the act is the sender's server's, and that is this one. A room
		// whose moderator lives here could otherwise never remove a member who joined from
		// elsewhere, and a room cannot defend itself against somebody it let in. What the other
		// server is owed is the event, which fan-out delivers — not a signature it never had to
		// give.
		//
		// A join or a knock speaks for the person doing it, so a target who lives elsewhere is
		// not this server's to sign for at all.
		switch change.Membership {
		case eventauth.MembershipInvite:
			return s.inviteRemote(ctx, change)
		case eventauth.MembershipLeave, eventauth.MembershipBan:
			senderDomain, err := matrixid.UserDomain(change.Sender)
			if err != nil || !matrixid.IsLocal(senderDomain, s.name) {
				return "", ErrRemoteSender
			}
		default:
			return "", ErrRemoteTarget
		}
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
	// A client cannot name the user who authorised its own join. That key is a claim about who
	// in the room vouched for the joiner, and the signature the room version demands is only
	// meaningful when the server chose it. Whatever a client put in the body is dropped here and
	// re-set below from what this server decided.
	authoriser := ""
	if change.Membership == eventauth.MembershipJoin {
		var err error
		if authoriser, err = s.RestrictedJoin(ctx, change.RoomID, change.Target); err != nil {
			if eventID, ok := s.joinThroughNeighbours(ctx, change, err); ok {
				return eventID, nil
			}
			return "", err
		}
	}
	content["membership"] = change.Membership
	// A user speaking for themselves brings their profile with them, because that is what the room's
	// other members read. A membership event written by someone else about this user — a kick, an
	// invite this server built — does not: this server is not the authority on how that person is
	// presented, and a name written by the wrong hand is the kind of thing a room then believes.
	if change.Membership == eventauth.MembershipJoin && change.Sender == change.Target {
		for key, value := range s.profileForMember(ctx, change.Target) {
			if _, present := content[key]; !present {
				content[key] = value
			}
		}
	}
	if change.IsDirect && change.Membership == eventauth.MembershipInvite {
		content["is_direct"] = true
	}
	if change.Reason != "" {
		content["reason"] = change.Reason
	}
	eventID, err := s.Send(ctx, SendRequest{
		RoomID:     change.RoomID,
		Sender:     change.Sender,
		DeviceID:   change.DeviceID,
		EventType:  eventauth.TypeMember,
		StateKey:   change.Target,
		IsState:    true,
		Content:    content,
		Authoriser: authoriser,
	})
	if err != nil {
		return eventID, err
	}
	// Coming back to a room undoes an earlier request not to be told about it. Done once the
	// membership event exists, because a join that was refused should not quietly change what
	// the user's own sync shows, and a marker left behind after a join that succeeded would hide
	// the room from the person now standing in it.
	if change.Membership == eventauth.MembershipJoin || change.Membership == eventauth.MembershipInvite {
		if err := s.rememberRoomAgain(ctx, change.RoomID, change.Target); err != nil {
			return eventID, err
		}
	}
	return eventID, nil
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
