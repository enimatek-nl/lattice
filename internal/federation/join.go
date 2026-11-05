package federation

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/enimatek/lattice/internal/eventauth"
	"github.com/enimatek/lattice/internal/events"
	"github.com/enimatek/lattice/internal/matrixerr"
	"github.com/enimatek/lattice/internal/matrixid"
	"github.com/enimatek/lattice/internal/rooms"
	"github.com/enimatek/lattice/internal/roomversion"
)

// Joiner carries one of this server's users into a room this server does not hold.
//
// The handshake has two halves and this is the half that asks. It gets the shape of a join from
// a server that already holds the room, signs that shape as this server's own user joining, and
// hands it back; the resident server decides whether its copy of the room permits it, and hands
// back the room this server now has to catch up on. What comes back is stored as a handed-over
// bundle rather than replayed as a history, because a room being joined arrives as a set of
// events that cite each other and cite the room's forward extremities, which are not in the set.
type Joiner struct {
	Client  *Client
	Keyring *Keyring
	// Keys signs this server's events. The join event is this server speaking for its own
	// user, so it is sealed with this key and no other.
	Keys events.Signer
	// LocalKeys verifies events this server itself signed. They come back in the answer —
	// this server's own membership event is part of the room now — and this server's signing
	// key is not something it fetches over the network.
	LocalKeys LocalKeyLookup
	Name      string
	Ingest    BundleIngestor
	Log       *slog.Logger
	// Now is replaced in tests.
	Now func() time.Time
}

// LocalKeyLookup answers this server's own signing keys. [*keys.Service] implements it.
type LocalKeyLookup interface {
	VerifyLocal(origin, keyID string) (ed25519.PublicKey, error)
}

// BundleIngestor stores the events another server handed over. It is implemented by
// [*rooms.Service].
type BundleIngestor interface {
	AcceptRemoteBundle(ctx context.Context, bundle rooms.RemoteBundle, verify rooms.VerifyPDU) (rooms.BundleOutcome, error)
}

// ErrNoJoinTarget says no server was willing or able to take the join.
var ErrNoJoinTarget = errors.New("federation: no server in the room would take the join")

// Join runs the handshake against the servers the client named, falling back to the server the
// room's ID names.
//
// A refusal by a resident server on the room's own rules is not retried against another server.
// It is a verdict about the room, and every server in the room is expected to reach the same
// one; trying three of them would only ask the same question three times, and would look to the
// room like an attempt to find a server laxer about its join rules.
func (j *Joiner) Join(ctx context.Context, roomID, userID string, destinations []string) (rooms.RemoteJoinResult, error) {
	if j.Client == nil || j.Keys == nil || j.Ingest == nil {
		return rooms.RemoteJoinResult{}, errors.New("federation: the join handshake is not wired up")
	}
	if err := matrixid.RoomIDValid(roomID); err != nil {
		return rooms.RemoteJoinResult{}, matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid room ID.", roomID)
	}
	if err := matrixid.UserValid(userID); err != nil {
		return rooms.RemoteJoinResult{}, matrixerr.Newf(matrixerr.CodeInvalidParam, "%q is not a valid user ID.", userID)
	}
	var lastErr error
	for _, destination := range j.candidates(ctx, roomID, destinations) {
		result, err := j.joinVia(ctx, destination, roomID, userID)
		if err == nil {
			return result, nil
		}
		var refusal *RemoteRefusal
		if errors.As(err, &refusal) {
			return rooms.RemoteJoinResult{}, refusal.toMatrixError()
		}
		lastErr = err
		j.Log.Warn("federation: a server could not carry a join",
			slog.String("destination", destination), slog.String("room_id", roomID),
			slog.Any("error", err))
	}
	if lastErr != nil {
		return rooms.RemoteJoinResult{}, fmt.Errorf("%w: last attempt: %v", ErrNoJoinTarget, lastErr)
	}
	return rooms.RemoteJoinResult{}, ErrNoJoinTarget
}

// RemoteRefusal is a resident server saying the room does not permit this join. The distinction
// from a network failure is the point: one is an answer, the other is a lack of one.
type RemoteRefusal struct {
	Server  string
	ErrCode string
	Message string
}

func (r *RemoteRefusal) Error() string {
	return fmt.Sprintf("%s refused the join: %s", r.Server, r.Message)
}

func (r *RemoteRefusal) toMatrixError() error {
	code := matrixerr.CodeForbidden
	if matrixerr.Code(r.ErrCode) == matrixerr.CodeNotFound {
		code = matrixerr.CodeNotFound
	}
	return matrixerr.Newf(code, "The homeserver holding this room did not let %s join: %s",
		r.Server, r.Message)
}

// candidates are the servers to ask, in the order worth asking them: the client's hints first,
// because a client choosing a server usually knows something the room ID does not say, then the
// server the room's ID names. This server's own name is dropped — it asked because it does not
// hold the room.
func (j *Joiner) candidates(ctx context.Context, roomID string, destinations []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(destinations)+1)
	add := func(candidate string) {
		if candidate == "" || candidate == j.Name || seen[candidate] {
			return
		}
		seen[candidate] = true
		out = append(out, candidate)
	}
	for _, candidate := range destinations {
		add(strings.TrimSpace(candidate))
	}
	if _, domain, err := matrixid.SplitID(matrixid.SigilRoom, roomID); err == nil {
		add(domain)
	}
	return out
}

// joinVia runs the whole handshake against one server.
func (j *Joiner) joinVia(ctx context.Context, destination, roomID, userID string) (rooms.RemoteJoinResult, error) {
	template, version, err := j.makeJoin(ctx, destination, roomID, userID)
	if err != nil {
		return rooms.RemoteJoinResult{}, err
	}
	event := template
	rules, ok := roomversion.Get(version)
	if !ok {
		return rooms.RemoteJoinResult{}, matrixerr.Newf(matrixerr.CodeIncompatibleRoomVersion,
			"Room %q uses room version %s, which this server cannot join in.", roomID, version).
			WithExtra(map[string]any{"room_version": version})
	}

	// The joining server supplies what only it can: which server speaks for this user, and when
	// the join was made. Everything else in the template — where the history ends, what
	// authorises a join here, how deep the event sits — came from a server that holds the room.
	event["origin"] = j.Name
	event["origin_server_ts"] = j.now().UnixMilli()
	if err := events.Seal(j.Name, rules, event, j.Keys); err != nil {
		return rooms.RemoteJoinResult{}, fmt.Errorf("federation: cannot sign the join for %s: %w", roomID, err)
	}
	eventID, err := events.EventID(rules, event)
	if err != nil {
		return rooms.RemoteJoinResult{}, fmt.Errorf("federation: cannot name the join event: %w", err)
	}
	event["event_id"] = eventID
	sealed, err := events.Canonical(event)
	if err != nil {
		return rooms.RemoteJoinResult{}, fmt.Errorf("federation: cannot encode the join: %w", err)
	}

	answer, err := j.sendJoin(ctx, destination, roomID, eventID, sealed)
	if err != nil {
		return rooms.RemoteJoinResult{}, err
	}

	// The resident server accepted. What it hands back is the room as it stood just before
	// this join: its state, and the events that authorise that state. Stored as a bundle —
	// these events cite one another, and the join cites the room's forward extremities, which
	// the handover does not include.
	bundle := rooms.RemoteBundle{PDUs: append(pdusFrom(answer, "auth_chain"), pdusFrom(answer, "state")...)}
	bundle.PDUs = append(bundle.PDUs, sealed)
	outcome, err := j.Ingest.AcceptRemoteBundle(ctx, bundle, j.verifier(ctx))
	if err != nil {
		return rooms.RemoteJoinResult{}, err
	}
	if !accepted(outcome, eventID) {
		// The join itself not landing is the one outcome that makes the whole handshake a
		// failure: this server asked, signed, and was not let in.
		reason := "it was not stored here"
		if problem, bad := outcome.Refused[eventID]; bad {
			reason = problem
		} else if len(outcome.Unresolved) > 0 {
			reason = fmt.Sprintf("the room it was handed with is incomplete here (%d events could not be judged)", len(outcome.Unresolved))
		}
		return rooms.RemoteJoinResult{}, fmt.Errorf("federation: %s accepted the join but this server could not store it: %s",
			destination, reason)
	}
	for eventID, problem := range outcome.Refused {
		if eventID == "" {
			continue
		}
		j.Log.Warn("federation: an event handed over with a join was refused here",
			slog.String("room_id", roomID), slog.String("event_id", eventID),
			slog.String("reason", problem))
	}
	return rooms.RemoteJoinResult{
		EventID:  eventID,
		State:    len(pdusFrom(answer, "state")),
		Outliers: len(outcome.Outliers),
	}, nil
}

// makeJoin asks a resident server what a join by this user would look like, and checks the
// answer before any signing is done.
//
// The checks are not paranoia. The template becomes an event this server signs, so a template
// that names a different room, or a different user, is a request to sign somebody else's join.
// An already-signed or already-hashed template is the same problem in another form: this server
// would be adding its name to an event somebody else had already made.
func (j *Joiner) makeJoin(ctx context.Context, destination, roomID, userID string) (map[string]any, string, error) {
	target := "/_matrix/federation/v1/make_join/" + roomID + "/" + userID
	response, err := j.Client.Do(ctx, destination, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	if refusal := refusalFrom(destination, response); refusal != nil {
		return nil, "", refusal
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, "", fmt.Errorf("federation: %s answered the join template with %d", destination, response.StatusCode)
	}
	var offer struct {
		Event       map[string]any `json:"event"`
		RoomVersion string         `json:"room_version"`
	}
	if err := json.Unmarshal(response.Body, &offer); err != nil {
		return nil, "", fmt.Errorf("federation: %s answered the join template with something unreadable: %v", destination, err)
	}
	event := offer.Event
	if event == nil {
		return nil, "", fmt.Errorf("federation: %s answered the join template with no event", destination)
	}
	// The template becomes an event this server signs, so it has to be this user's join in
	// this room and nobody else's.
	if kind, _ := event["type"].(string); kind != eventauth.TypeMember {
		return nil, "", fmt.Errorf("federation: %s offered a %s as a join", destination, kind)
	}
	if got, _ := event["room_id"].(string); got != roomID {
		return nil, "", fmt.Errorf("federation: %s offered a join template for room %q", destination, got)
	}
	if got, _ := event["sender"].(string); got != userID {
		return nil, "", fmt.Errorf("federation: %s offered a join by %s", destination, got)
	}
	if got, _ := event["state_key"].(string); got != userID {
		return nil, "", fmt.Errorf("federation: %s offered a member event for %s", destination, got)
	}
	content, _ := event["content"].(map[string]any)
	if membership, _ := content["membership"].(string); membership != eventauth.MembershipJoin {
		return nil, "", fmt.Errorf("federation: %s offered a membership of %v", destination, content["membership"])
	}
	// An already-hashed or already-signed template would mean this server adding its name to
	// an event somebody else had already made.
	for _, key := range []string{"event_id", "signatures", "hashes"} {
		if _, present := event[key]; present {
			return nil, "", fmt.Errorf("federation: %s offered a join template that already carries %s", destination, key)
		}
	}
	if offer.RoomVersion == "" {
		return nil, "", fmt.Errorf("federation: %s offered a join template with no room version", destination)
	}
	return event, offer.RoomVersion, nil
}

// sendJoin hands the signed join to the resident server.
func (j *Joiner) sendJoin(ctx context.Context, destination, roomID, eventID string, pdu []byte) (map[string]any, error) {
	target := "/_matrix/federation/v2/send_join/" + roomID + "/" + eventID
	response, err := j.Client.Do(ctx, destination, http.MethodPut, target, pdu)
	if err != nil {
		return nil, err
	}
	if refusal := refusalFrom(destination, response); refusal != nil {
		return nil, refusal
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("federation: %s refused the signed join with %d: %s",
			destination, response.StatusCode, strings.TrimSpace(string(response.Body)))
	}
	var answer map[string]any
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return nil, fmt.Errorf("federation: %s answered the join with something unreadable: %v", destination, err)
	}
	return answer, nil
}

// pdusFrom reads a list of events out of a federation answer, back into the bytes they arrived
// as. The answer decoded them into maps; the room service takes events as bytes, because what an
// event is is the bytes its origin signed.
func pdusFrom(answer map[string]any, key string) [][]byte {
	list, _ := answer[key].([]any)
	out := make([][]byte, 0, len(list))
	for _, entry := range list {
		body, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			continue
		}
		out = append(out, encoded)
	}
	return out
}

// verifier is the check every handed-over event has to pass before it is stored.
//
// The keyring holds other servers' keys; this server's own key is not fetched over the network,
// and this server's own membership event is part of what comes back — the room it just joined
// includes the join it just made.
func (j *Joiner) verifier(ctx context.Context) rooms.VerifyPDU {
	return func(event map[string]any, rules roomversion.Rules) error {
		remote := j.Keyring.LookupForRoom(ctx, rules)
		if origin, _ := event["origin"].(string); origin == j.Name {
			return events.Verify(rules, event, j.LocalKeys.VerifyLocal)
		}
		return events.Verify(rules, event, remote)
	}
}

// refusalFrom turns a resident server's refusal into something the caller can tell apart from a
// server that did not answer.
func refusalFrom(destination string, response Response) *RemoteRefusal {
	if response.StatusCode >= 200 && response.StatusCode <= 299 {
		return nil
	}
	var problem struct {
		ErrCode string `json:"errcode"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(response.Body, &problem); err != nil || problem.ErrCode == "" {
		return nil
	}
	if problem.ErrCode == string(matrixerr.CodeNotFound) || problem.ErrCode == string(matrixerr.CodeForbidden) {
		return &RemoteRefusal{
			Server:  destination,
			ErrCode: problem.ErrCode,
			Message: problem.Error,
		}
	}
	return nil
}

// accepted says whether an event from the bundle is now held here.
func accepted(outcome rooms.BundleOutcome, eventID string) bool {
	for _, id := range outcome.Accepted {
		if id == eventID {
			return true
		}
	}
	return false
}

func (j *Joiner) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now().UTC()
}
