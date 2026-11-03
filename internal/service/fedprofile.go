package service

import (
	"context"
	"log"
	"net/url"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// A display name and an avatar are public information about a local user, and a server is
// the authority on the names it hands out: a user ID says whose address it carries, and
// only that server can say what the user behind it is called.
//
// What is not done here is answering for somebody else's user. A peer that asks about
// `@someone:other.example` is told "not found" rather than referred or guessed at: this
// server has no way to know what that user is called, and an answer phrased like an
// authority is how a wrong name ends up copied into a hundred member lists. The one thing
// worth saying out loud is that the question was malformed - a name that cannot address a
// server is not a user this server could ever hold.

// profileFields are the profile keys a peer is allowed to ask for by name.
var profileFields = []string{ProfileDisplayName, ProfileAvatarURL}

// AnswerProfileQuery answers GET /_matrix/federation/v1/query/profile for a peer: the
// display name and avatar of a user whose name says it belongs to this server.
func (s *Service) AnswerProfileQuery(ctx context.Context, userID, field string) (matrix.Object, error) {
	if !matrix.IsUserID(userID) {
		return nil, matrix.ErrInvalidParam("%q is not a user id", userID)
	}
	domain := matrix.DomainOf(userID)
	if !matrix.ValidServerName(domain) {
		// `localhost:http` is the usual shape here: a URI that was never split into its
		// parts. Asking about such a name is a malformed request rather than a question
		// with no answer, and the difference matters to whoever sent it.
		return nil, matrix.ErrInvalidParam("the user id %q carries %q, which is not a server name",
			userID, domain)
	}
	if domain != s.serverName {
		// The name says whose user this is, and it does not say ours.
		return nil, matrix.ErrNotFound("this server does not hold the user %s", userID)
	}
	if !s.UserExists(ctx, userID) {
		// A user that was never registered, or one that left and had its account closed,
		// has no profile to report. The name may still appear in old events; that is not
		// the same as the account still being here.
		return nil, matrix.ErrNotFound("this server has no user %s", userID)
	}
	answer := matrix.Object{}
	for _, key := range profileFields {
		if field != "" && field != key {
			// A field this server does not know is not a reason to refuse: the two it does
			// know are public either way, and a refusal teaches the asker nothing about the
			// user it was asking about.
			continue
		}
		// A key that was never set is left out rather than answered empty: "no name" and
		// "a name that is the empty string" are different statements, and the client that
		// has to choose a fallback should choose it itself.
		if value := s.profileValue(ctx, userID, key); value != "" {
			answer[key] = value
		}
	}
	return answer, nil
}

// profileValue reads one profile key without the fallback that room display names use:
// what a user wrote, or nothing.
func (s *Service) profileValue(ctx context.Context, userID, key string) string {
	value, err := s.db.ProfileRepo.Get(userID, key)
	if err != nil {
		return ""
	}
	return value
}

// RemoteProfile asks the server a user ID names for that user's profile.
//
// This is a request an unauthenticated client can cause, which is why an unreachable peer
// is answered as "this server cannot tell you" rather than as a failure of the request:
// somebody else's outage is not a reason to make a client's read look like a bug here, and
// it is certainly not a reason to keep the connection open waiting for it.
func (s *Service) RemoteProfile(ctx context.Context, userID string) (matrix.Object, error) {
	transport := s.federationTransport()
	if transport == nil {
		return nil, matrix.ErrNotFound("the profile of %s is not held here, and this server does not federate", userID)
	}
	domain := matrix.DomainOf(userID)
	if !matrix.ValidServerName(domain) {
		return nil, matrix.ErrInvalidParam("%q is not a server name, so nobody can be asked about %s", domain, userID)
	}
	body, err := transport.Get(ctx, domain, "/_matrix/federation/v1/query/profile",
		url.Values{"user_id": []string{userID}})
	if err != nil {
		log.Printf("federation: asking %s for the profile of %s: %v", domain, userID, err)
		if failure, ok := err.(*matrix.Error); ok && failure.Status == 404 {
			return nil, matrix.ErrNotFound("%s says it has no user %s to describe", domain, userID)
		}
		return nil, matrix.ErrNotFound("%s could not be asked about %s", domain, userID)
	}
	answer := matrix.Object{}
	for _, key := range profileFields {
		if value, ok := body[key].(string); ok && value != "" {
			answer[key] = value
		}
	}
	return answer, nil
}

// ProfileForReader answers a client asking about a user: from this server's own rows when
// the name is one this server handed out, and by asking the server whose name it is
// otherwise. Both answers are the same shape, which is what lets a client show a member
// list containing local and remote users without caring which is which.
func (s *Service) ProfileForReader(ctx context.Context, userID string) (matrix.Object, error) {
	if s.LocalUser(userID) {
		return s.Profile(ctx, userID)
	}
	return s.RemoteProfile(ctx, userID)
}

// ProfileKeyForReader is the single-key form of [Service.ProfileForReader].
func (s *Service) ProfileKeyForReader(ctx context.Context, userID, key string) (string, error) {
	if s.LocalUser(userID) {
		return s.ProfileKey(ctx, userID, key)
	}
	profile, err := s.RemoteProfile(ctx, userID)
	if err != nil {
		return "", err
	}
	value, ok := profile[key].(string)
	if !ok || value == "" {
		return "", matrix.ErrNotFound("%s has no %s to report for %s", matrix.DomainOf(userID), key, userID)
	}
	return value, nil
}
