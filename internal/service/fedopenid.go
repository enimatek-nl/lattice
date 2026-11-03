package service

import (
	"context"
	"log"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// GET /_matrix/federation/v1/openid/userinfo is the other end of the token this server
// already mints at POST /user/{userId}/openid/request_token. Minting one without answering
// what it means is the half that does the damage: a user is handed a credential to show a
// stranger, and the stranger has nowhere to take it.
//
// Unlike almost every other server-server endpoint this one is not signed by the asker, and
// that is not an oversight. The party that asks is usually an identity server working for a
// third party's invite, and it has no homeserver key to sign with; the credential is the
// token itself, which only the user chose to hand over. What the endpoint gives away is one
// fact - which user of *this* server a token this server issued stands for - which is a fact
// this server is the authority on and cannot learn from anybody else.

// OpenIDUserInfo resolves an OpenID token this server issued into the user it represents.
func (s *Service) OpenIDUserInfo(ctx context.Context, token string) (matrix.Object, error) {
	if token == "" {
		return nil, matrix.ErrUnauthorized("an OpenID token is needed to ask what it stands for")
	}
	userID, err := s.OpenIDUser(ctx, token)
	if err != nil {
		// Never ours, expired, or never issued: the asker cannot act on the difference and
		// the user is better off not having the difference reported.
		log.Printf("federation: an OpenID token was presented that this server will not stand behind: %v", err)
		return nil, matrix.ErrUnknownToken(false)
	}
	return matrix.Object{"sub": userID}, nil
}
