package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Inviter carries an invite to the homeserver of the user being invited.
//
// The round trip exists because of who may say what about whom. This server holds the room and so
// can build the invite event, but the user it names lives elsewhere, and the room's other servers
// are entitled to see that user's own server agree that the user was invited. So the event goes
// there unsigned-but-signed-by-the-inviter, and comes back with a second signature on it.
type Inviter struct {
	Client *Client
	// Name is this server's name, carried for the log lines: the destination already names
	// itself in the request's authorization.
	Name string
	Log  *slog.Logger
}

// SendInvite sends the invite to the invited user's server and returns the event with that
// server's signature added.
//
// v2 is asked for first, because it names the room version. A server that answers 404 or 400 to it
// is one that does not know that endpoint, and for a room version 1 or 2 room the older form is
// still a correct question — the specification says so, and a v1-only peer is a real thing in the
// federation.
func (i *Inviter) SendInvite(ctx context.Context, destination, roomVersion string, event map[string]any) (map[string]any, error) {
	if i.Client == nil {
		return nil, fmt.Errorf("federation: the invite transport is not wired up")
	}
	roomID, _ := event["room_id"].(string)
	eventID, _ := event["event_id"].(string)
	if roomID == "" || eventID == "" {
		return nil, fmt.Errorf("federation: an invite has to name its room and its event before it is sent")
	}
	target := "/_matrix/federation/v2/invite/" + roomID + "/" + eventID
	body := map[string]any{"room_version": roomVersion, "invite_event": event}
	countersigned, status, err := i.ask(ctx, destination, target, body)
	if err == nil {
		return countersigned, nil
	}
	// A refusal that names a reason — the room's rules, this server's user, a room that will
	// not have it — is a verdict, and a second attempt at the same verdict would only ask
	// twice. Only an answer that says "I do not know this endpoint" invites the older form.
	if status != http.StatusNotFound && status != http.StatusBadRequest {
		return nil, err
	}
	switch roomVersion {
	case "1", "2":
	default:
		return nil, err
	}
	if i.Log != nil {
		i.Log.Debug("federation: asking a server for an invite signature over the v1 endpoint",
			"destination", destination, "room_id", roomID, "event_id", eventID, "error", err)
	}
	v1Target := "/_matrix/federation/v1/invite/" + roomID + "/" + eventID
	countersigned, _, err2 := i.ask(ctx, destination, v1Target, event)
	if err2 != nil {
		return nil, err2
	}
	return countersigned, nil
}

// ask makes one invite request and reads the event out of the answer.
func (i *Inviter) ask(ctx context.Context, destination, target string, body any) (map[string]any, int, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("federation: cannot encode the invite: %w", err)
	}
	response, err := i.Client.Do(ctx, destination, http.MethodPut, target, encoded)
	if err != nil {
		return nil, 0, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		problem := strings.TrimSpace(string(response.Body))
		var answer struct {
			ErrCode string `json:"errcode"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(response.Body, &answer) == nil && answer.Error != "" {
			problem = answer.ErrCode + ": " + answer.Error
		}
		return nil, response.StatusCode, fmt.Errorf("federation: %s refused to sign the invite: %s", destination, problem)
	}
	var answer struct {
		Event map[string]any `json:"event"`
	}
	if err := json.Unmarshal(response.Body, &answer); err != nil || answer.Event == nil {
		return nil, response.StatusCode, fmt.Errorf("federation: %s answered an invite without the event it signed", destination)
	}
	return answer.Event, response.StatusCode, nil
}
