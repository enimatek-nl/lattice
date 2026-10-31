package service

import (
	"context"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
)

// This file answers the third-party identifier endpoints - the email addresses
// and phone numbers linked to an account.
//
// Lattice has no identity server integration, no email or SMS delivery and no
// place to store an address that has been proven to belong to anybody, so the
// honest answer is an empty list and a refusal for everything that would change
// it. That is still worth implementing rather than leaving unrouted: a client
// that cannot ask shows the user an error ("Unable to load email addresses and
// phone numbers" in Element) where an empty settings row belongs, and an
// unrouted path hides which thing this homeserver does not do.

// ThirdPartyLinkRefusal is the answer to every endpoint that would put an email
// address or phone number on an account: linking one means proving the requester
// controls it, which means sending them a message, which this server does not do.
var ThirdPartyLinkRefusal = matrix.ErrForbidden(
	"this homeserver does not link third-party identifiers (email addresses or phone numbers) to accounts")

// ThirdPartyRemoveRefusal is the answer to every endpoint that would take one
// away. Everything that could be taken away is already absent, so "there is no
// such third-party identifier" is both the truthful answer and the one the
// specification gives for a miss.
var ThirdPartyRemoveRefusal = matrix.ErrNotFound(
	"this account has no third-party identifiers to remove")

// ThirdPartyTokenRefusal is the answer to the requestToken endpoints, which ask
// the homeserver to send a validation message. The specification's code for a
// homeserver that will not start this exchange is M_FORBIDDEN, and the reason
// belongs in the body: a client cannot work out from a bare code whether the
// address is bad or the server does not do this at all.
func ThirdPartyTokenRefusal(medium string) error {
	return matrix.ErrForbidden(
		"this homeserver does not send verification messages to %s addresses", medium)
}

// ThirdPartyIDs lists the third-party identifiers linked to an account. There is
// nowhere in Lattice for one to come from, so the list is empty; the endpoint
// answers so that a client can render "nothing linked" instead of an error.
func (s *Service) ThirdPartyIDs(ctx context.Context, userID string) ([]matrix.ThreePID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := s.db.UserRepo.GetByID(userID); err != nil && !persist.IsNotFound(err) {
		return nil, matrix.ErrInternal(err)
	}
	return []matrix.ThreePID{}, nil
}
