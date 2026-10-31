package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
)

// The User-Interactive Auth stages Lattice understands. A stage is the name a
// client puts in auth.type to prove it satisfied one step of a flow.
const (
	// StageDummy needs no credentials: reaching it is proof enough.
	StageDummy = "m.login.dummy"
	// StagePassword asks for the password of an existing account.
	StagePassword = "m.login.password"
	// StageRegistrationToken asks for a token handed out by the operator.
	StageRegistrationToken = "m.login.registration_token"
	// StageAdmin is Lattice specific: an already authenticated administrator
	// vouches for the action, which is how accounts are created on a locked
	// down server.
	StageAdmin = "org.matrix.lattice.admin"
)

// AuthSessionLifetime is how long a client may keep working through an
// interactive auth exchange before it has to start a new one.
const AuthSessionLifetime = 24 * time.Hour

// AuthFlow is one route through an interactive auth exchange: the ordered
// stages a client has to complete.
type AuthFlow struct {
	Stages []string `json:"stages"`
}

// AuthInfo is the User-Interactive Auth payload a server returns with a 401 so
// the client knows what to do next.
type AuthInfo struct {
	Flows     []AuthFlow    `json:"flows"`
	Params    matrix.Object `json:"params"`
	Session   string        `json:"session"`
	Completed []string      `json:"completed"`
}

// authSession is one in-flight interactive auth exchange. Sessions live in
// memory: they span only the handful of requests of a single registration or
// administrative action, and the housekeeping pass drops the abandoned ones.
// A session can be read by concurrent requests that present the same session
// id, so it guards its own state.
type authSession struct {
	mu        sync.Mutex
	id        string
	flows     []AuthFlow
	completed []string
	expiresMS int64
}

// touch refreshes a session with the flows that are currently configured and a
// new expiry, so a client that keeps working never loses the exchange.
func (a *authSession) touch(flows []AuthFlow, nowMS int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.flows = flows
	a.expiresMS = nowMS + AuthSessionLifetime.Milliseconds()
}

// complete records that a stage is done.
func (a *authSession) complete(stage string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.done(stage) {
		a.completed = append(a.completed, stage)
	}
}

// done reports whether the session already finished a stage. Callers must hold
// a.mu.
func (a *authSession) done(stage string) bool {
	for _, finished := range a.completed {
		if finished == stage {
			return true
		}
	}
	return false
}

// snapshot returns the flows and completed stages as copies, which is what goes
// into a response body.
func (a *authSession) snapshot() (flows []AuthFlow, completed []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	flows = make([]AuthFlow, len(a.flows))
	copy(flows, a.flows)
	completed = make([]string, len(a.completed))
	copy(completed, a.completed)
	return flows, completed
}

// finished reports whether every stage of one advertised flow is done, which
// means the session has served its purpose.
func (a *authSession) finished() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, flow := range a.flows {
		done := true
		for _, stage := range flow.Stages {
			if !a.done(stage) {
				done = false
				break
			}
		}
		if done {
			return true
		}
	}
	return false
}

// expired reports whether the session outlived its lifetime.
func (a *authSession) expired(nowMS int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.expiresMS <= nowMS
}

// advertises reports whether any flow of the session can be satisfied by a
// stage.
func (a *authSession) advertises(stage string) bool {
	flows, _ := a.snapshot()
	for _, flow := range flows {
		for _, advertised := range flow.Stages {
			if advertised == stage {
				return true
			}
		}
	}
	return false
}

// authString returns a string field of an auth dict, or the empty string.
func authString(auth matrix.Object, key string) string {
	value, _ := auth[key].(string)
	return value
}

// beginAuthSession returns the session a client refers to, refreshing its
// expiry, or starts a new one when the client has no session (yet) or the one
// it sent is unknown or expired. Replacing an unknown session keeps the client
// moving instead of leaving it with a dead end.
func (s *Service) beginAuthSession(flows []AuthFlow, sessionID string) *authSession {
	nowMS := s.NowMS()
	if sessionID != "" {
		s.uiaMu.Lock()
		session, ok := s.uiaSessions[sessionID]
		s.uiaMu.Unlock()
		if ok && !session.expired(nowMS) {
			session.touch(flows, nowMS)
			return session
		}
	}
	session := &authSession{
		id:        matrix.NewToken(),
		flows:     flows,
		completed: []string{},
		expiresMS: nowMS + AuthSessionLifetime.Milliseconds(),
	}
	s.uiaMu.Lock()
	s.uiaSessions[session.id] = session
	s.uiaMu.Unlock()
	return session
}

// dropAuthSession forgets a session, which is what a finished or abandoned
// exchange amounts to.
func (s *Service) dropAuthSession(session *authSession) {
	if session == nil {
		return
	}
	s.uiaMu.Lock()
	defer s.uiaMu.Unlock()
	delete(s.uiaSessions, session.id)
}

// authInfo renders the User-Interactive Auth payload of a session.
func (s *Service) authInfo(session *authSession) AuthInfo {
	flows, completed := session.snapshot()
	return AuthInfo{
		Flows:     flows,
		Params:    s.authParams(),
		Session:   session.id,
		Completed: completed,
	}
}

// authParams are the optional parameters clients may need for a stage. Lattice
// has none to publish, but the key is always present so clients can treat the
// payload uniformly.
func (s *Service) authParams() matrix.Object {
	return matrix.Object{}
}

// authChallenge builds the 401 that asks a client to complete a stage. The body
// carries the flows, the session id and the stages already done, which is what
// clients need to keep the exchange going.
func (s *Service) authChallenge(session *authSession, format string, args ...any) *matrix.Error {
	info := s.authInfo(session)
	err := matrix.ErrUnauthorized(format, args...)
	return err.WithExtra(matrix.Object{
		"flows":     info.Flows,
		"params":    info.Params,
		"session":   info.Session,
		"completed": info.Completed,
	})
}

// expireAuthSessions drops sessions nobody finished and returns how many went
// away. It runs from the housekeeping loop.
func (s *Service) expireAuthSessions(nowMS int64) int64 {
	s.uiaMu.Lock()
	defer s.uiaMu.Unlock()
	var removed int64
	for id, session := range s.uiaSessions {
		if session.expired(nowMS) {
			delete(s.uiaSessions, id)
			removed++
		}
	}
	return removed
}

// RegistrationFlows lists the interactive auth flows account registration
// accepts. A configured registration token replaces the open dummy flow, which
// is how an operator closes a server to invitations only.
func (s *Service) RegistrationFlows() []AuthFlow {
	if s.RegistrationTokenSupported() {
		return []AuthFlow{{Stages: []string{StageRegistrationToken}}}
	}
	return []AuthFlow{{Stages: []string{StageDummy}}}
}

// RegistrationTokenSupported reports whether a registration token is a way in.
func (s *Service) RegistrationTokenSupported() bool {
	return len(s.registrationSecrets) > 0
}

// RegistrationChallenge opens (or continues) a registration auth session and
// returns the 401 that describes the flows. GET /register exists for nothing
// else: clients use it to learn how to register before sending credentials.
func (s *Service) RegistrationChallenge(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	session := s.beginAuthSession(s.RegistrationFlows(), sessionID)
	return s.authChallenge(session, "additional authentication is required to register an account")
}

// validRegistrationToken reports whether a client presented one of the
// configured registration tokens.
func (s *Service) validRegistrationToken(token string) bool {
	if token == "" {
		return false
	}
	for _, secret := range s.registrationSecrets {
		if secret == token {
			return true
		}
	}
	return false
}

// authorizeRegistration runs the User-Interactive Auth exchange in front of
// account creation. A request without an auth dict is answered with a 401 that
// lists the flows, which is how clients discover how to register in the first
// place.
func (s *Service) authorizeRegistration(params RegisterParams) error {
	session := s.beginAuthSession(s.RegistrationFlows(), authString(params.Auth, "session"))
	// A session that has completed every stage of one flow is spent: dropping it
	// stops a client from replaying a finished exchange.
	defer func() {
		if session.finished() {
			s.dropAuthSession(session)
		}
	}()

	if len(params.Auth) == 0 {
		return s.authChallenge(session, "additional authentication is required to register an account")
	}
	stage := authString(params.Auth, "type")
	if stage == "" {
		return s.authChallenge(session, "the authentication request did not name a stage")
	}

	switch stage {
	case StageDummy:
		if !session.advertises(StageDummy) {
			return s.authChallenge(session, "this homeserver only accepts registration tokens")
		}
	case StageRegistrationToken:
		if !session.advertises(StageRegistrationToken) {
			return s.authChallenge(session, "session does not support authentication protocol %s", stage)
		}
		if !s.validRegistrationToken(authString(params.Auth, "token")) {
			return matrix.ErrInvalidParam("the registration token was not recognised")
		}
	case StagePassword:
		if !session.advertises(StagePassword) {
			return s.authChallenge(session, "session does not support authentication protocol %s", stage)
		}
		if err := s.checkPasswordAuth(params.Auth, params.Username); err != nil {
			var merr *matrix.Error
			if errors.As(err, &merr) && merr.HTTPStatus() == http.StatusForbidden {
				// A wrong password is not fatal: the client gets the flow list
				// back and can ask for the credentials again.
				return s.authChallenge(session, "the user authentication passed in the request body is incorrect")
			}
			return err
		}
	case StageAdmin:
		if !params.CallerAdmin {
			return s.authChallenge(session, "only an administrator may register accounts this way")
		}
	default:
		return s.authChallenge(session, "session does not support authentication protocol %s", stage)
	}
	session.complete(stage)
	return nil
}

// checkPasswordAuth verifies the m.login.password credentials a client put in
// an auth dict.
func (s *Service) checkPasswordAuth(auth matrix.Object, fallbackUser string) error {
	identifier, _ := auth["identifier"].(matrix.Object)
	user := authString(identifier, "user")
	password := authString(auth, "password")
	if user == "" {
		user = authString(auth, "user")
	}
	if user == "" {
		user = fallbackUser
	}
	account, err := s.localUser(user)
	if err != nil {
		return err
	}
	return s.verifyPassword(account, password)
}
