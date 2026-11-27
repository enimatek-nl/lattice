package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/enimatek-nl/lattice/internal/matrix"
	"github.com/enimatek-nl/lattice/internal/persist"
	"github.com/enimatek-nl/lattice/internal/push"
)

// The outbound half of push: what this server owes a registered pusher, which is a
// request to the gateway the pusher names.
//
// Until this file existed the pipeline stopped one step short of the phone. A client
// could register an endpoint with POST /pushers/set, GET /pushers listed it back with
// the right URL, the rules decided that a message was worth a notification, and a
// notification row was written - and nothing ever knocked at the gateway, so an app
// that is not open learned about the message at its next sync, which for a phone in a
// pocket is not a notification at all.
//
// The specification's shape is kept: one request per recipient per gateway, because
// `counts`, `unread` and `prio` describe that recipient and not the device, and the
// devices that share a gateway and a format ride along in the `devices` array.
const (
	// pushGatewayTimeout bounds one request. A gateway is somebody else's server, and
	// a slow one must not hold a delivery worker.
	pushGatewayTimeout = 10 * time.Second

	// pushQueueDepth is how much undelivered work the loop tolerates before it starts
	// dropping. A notification row is already written at that point, so a drop costs a
	// wake-up and not a message: the app reads it from its next sync.
	pushQueueDepth = 256

	// pushMaxAttempts counts the first try. A gateway that is down for longer than a
	// few minutes has bigger problems than this queue, and a backlog here is a
	// notification that arrives after the person already unlocked the phone.
	pushMaxAttempts = 3

	pushRetryFloor   = 2 * time.Second
	pushRetryCeiling = 2 * time.Minute

	// pushResponseLimit caps how much of a gateway's answer is read. The one field
	// that matters is a list of pushkeys.
	pushResponseLimit = 64 << 10
)

// pushDevice is one entry of the `devices` array: what to knock at, when it was
// registered, and what the rules said about this notification for it.
type pushDevice struct {
	AppID     string          `json:"app_id"`
	PushKey   string          `json:"pushkey"`
	PushKeyTS int64           `json:"pushkey_ts"`
	Data      matrix.Object   `json:"data,omitempty"`
	Tweaks    []matrix.Object `json:"tweaks,omitempty"`
}

// pushOwner names a pusher for the bookkeeping that follows a gateway's answer: a
// rejected pushkey is removed for one user's app, not for the next account that
// happens to use the same key.
type pushOwner struct {
	UserID  string
	AppID   string
	PushKey string
}

// pushCounts is the badge the gateway puts on the app icon.
type pushCounts struct {
	Unread    int64 `json:"unread"`
	Highlight int64 `json:"highlight"`
}

// pushNotification is the body of one gateway request, as the Push Gateway API
// describes it. `content` and `sender` are absent for a pusher registered with
// `format: "event_id_only"`, which is what an app that fetches the event itself asks
// for - and what the lab's gateway checks for.
type pushNotification struct {
	EventID string        `json:"event_id"`
	RoomID  string        `json:"room_id,omitempty"`
	Sender  string        `json:"sender,omitempty"`
	Type    string        `json:"type,omitempty"`
	Content matrix.Object `json:"content,omitempty"`
	Counts  *pushCounts   `json:"counts,omitempty"`
	Unread  bool          `json:"unread,omitempty"`
	Prio    string        `json:"prio,omitempty"`
	Devices []pushDevice  `json:"devices"`
}

// pushTask is one prepared request. The body is rendered while the event is still
// being sent, so a worker only has to make the call.
type pushTask struct {
	url     string
	what    string
	reason  string
	body    []byte
	devices []pushOwner
	tries   int
}

// pushDelivery is the loop's state: one shared queue, one HTTP client, and the context
// of the loop that is currently running - which is also the switch that decides
// whether delivery is on at all.
type pushDelivery struct {
	client *http.Client
	queue  chan pushTask

	mu      sync.Mutex
	running context.Context
}

// StartPushDelivery drains the push queue until the context is cancelled or the
// returned function is called. Like the federation outbox it runs on its own:
// a homeserver that sends messages must not be held up by a push gateway that is
// slow, and a gateway that is dead must not make a message fail to send.
func (s *Service) StartPushDelivery(ctx context.Context) func() {
	loopCtx, cancel := context.WithCancel(ctx)
	s.push.mu.Lock()
	previous := s.push.running
	s.push.running = loopCtx
	s.push.mu.Unlock()
	if previous != nil {
		// Two loops would race for the queue and double every knock. The first one keeps
		// running; starting a second is a caller that lost its stop function.
		cancel()
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-loopCtx.Done():
				return
			case task := <-s.push.queue:
				s.deliverPush(task)
			}
		}
	}()

	return func() {
		cancel()
		s.push.mu.Lock()
		if s.push.running == loopCtx {
			s.push.running = nil
		}
		s.push.mu.Unlock()
		<-done
	}
}

// queuePush hands one prepared request to the loop. It is deliberately non-blocking:
// the caller is sending a message, and waiting for a gateway would make a
// notification endpoint somebody else's outage.
func (s *Service) queuePush(task pushTask) {
	s.push.mu.Lock()
	ctx := s.push.running
	s.push.mu.Unlock()
	if ctx == nil {
		// Delivery is off. The notification row stays, so /notifications and the next
		// sync are still complete.
		return
	}
	select {
	case s.push.queue <- task:
	case <-ctx.Done():
	default:
		log.Printf("push: queue full, dropped %s: the app will read it from its next sync", task.what)
	}
}

// retryPush delays a task that could have been the gateway's fault, and gives up out
// loud when it runs out of tries. Silence here is what made the old behaviour
// undiagnosable: a pusher that is never contacted looks exactly like a pusher nobody
// wanted to contact.
func (s *Service) retryPush(task pushTask, reason string) {
	task.tries++
	task.reason = reason
	if task.tries >= pushMaxAttempts {
		log.Printf("push: gave up on %s after %d attempt(s) to %s: %s",
			task.what, task.tries, task.url, reason)
		return
	}
	delay := pushRetryDelay(task.tries)
	s.push.mu.Lock()
	ctx := s.push.running
	s.push.mu.Unlock()
	if ctx == nil {
		return
	}
	time.AfterFunc(delay, func() {
		select {
		case <-ctx.Done(): // delivery stopped while this was waiting
		case s.push.queue <- task:
		default:
			log.Printf("push: queue full, dropped the retry of %s", task.what)
		}
	})
}

// pushRetryDelay doubles from two seconds up to two minutes, with up to a quarter
// taken off at random so that a gateway which recovers is not knocked at by every
// homeserver in every room at the same instant.
func pushRetryDelay(tries int) time.Duration {
	delay := pushRetryFloor
	for i := 1; i < tries && delay < pushRetryCeiling; i++ {
		delay *= 2
	}
	if delay > pushRetryCeiling {
		delay = pushRetryCeiling
	}
	if jitter := delay / 4; jitter > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(int64(jitter))); err == nil {
			delay -= time.Duration(n.Int64())
		}
	}
	return delay
}

// deliverPush makes one attempt and settles what a reply means.
func (s *Service) deliverPush(task pushTask) {
	ctx, cancel := context.WithTimeout(context.Background(), pushGatewayTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, task.url, bytes.NewReader(task.body))
	if err != nil {
		// A URL that cannot even be asked for is not going to start.
		log.Printf("push: %s: cannot ask %s: %v", task.what, task.url, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.push.client.Do(req)
	if err != nil {
		s.retryPush(task, "gateway unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, pushResponseLimit))

	switch {
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode >= http.StatusInternalServerError:
		s.retryPush(task, fmt.Sprintf("gateway answered %d", resp.StatusCode))
	case resp.StatusCode == http.StatusNotFound:
		// Nothing lives at that path. The URL is the client's doing and the pushkey is
		// not what the gateway objected to, so the pushers stay and the operator sees
		// the one line.
		log.Printf("push: %s: no push endpoint at %s: %s", task.what, task.url, firstLine(raw))
	case resp.StatusCode == http.StatusBadRequest:
		// The Push Gateway API answers 400 for a pushkey it will not accept, which is
		// the one answer that says: stop registering for this device.
		s.dropPushers(task.devices, "the push gateway refused the pushkey with a 400: "+firstLine(raw))
	case resp.StatusCode >= http.StatusBadRequest:
		// Some other rejection. It says nothing about the pushkey, so the pushers stay;
		// retrying a request the gateway already refused once is a way to be rate
		// limited for nothing.
		log.Printf("push: %s: gateway answered %d: %s", task.what, resp.StatusCode, firstLine(raw))
	default:
		// A gateway may accept the request and still name pushkeys it has thrown away.
		if rejected := rejectedPushKeys(raw); len(rejected) > 0 {
			s.dropPushers(task.devices, "the push gateway rejected "+fmt.Sprint(len(rejected))+" pushkey(s): "+firstLine(raw))
		}
	}
}

// dropPushers removes the endpoints a gateway no longer wants. Without this a pusher
// for a device that was uninstalled stays forever, and every message to that room
// queues a request that will be refused again.
func (s *Service) dropPushers(devices []pushOwner, reason string) {
	dropped := 0
	for _, owner := range devices {
		if err := s.db.PusherRepo.Delete(owner.UserID, owner.PushKey, owner.AppID); err != nil {
			log.Printf("push: cannot forget the pusher %s of %s: %v", owner.PushKey, owner.UserID, err)
			continue
		}
		dropped++
	}
	if dropped > 0 {
		log.Printf("push: forgot %d pusher(s): %s", dropped, reason)
	}
}

// rejectedPushKeys reads the `rejected` list out of a gateway's answer. A gateway that
// answers 200 with an empty body has nothing to report, which is the common case.
func rejectedPushKeys(raw []byte) []string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var answer struct {
		Rejected []string `json:"rejected"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil
	}
	return answer.Rejected
}

// firstLine keeps a gateway's complaint on one line of the log without letting it
// occupy a paragraph.
func firstLine(raw []byte) string {
	text := string(bytes.TrimSpace(raw))
	if index := bytes.IndexByte(raw, '\n'); index >= 0 {
		text = string(bytes.TrimSpace(raw[:index]))
	}
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return "(no body)"
	}
	return text
}

// queuePushesForEvent turns the notifications the rules allowed into requests. It runs
// inside the send, so it only reads what is already in the database and renders; the
// network happens on the loop.
func (s *Service) queuePushesForEvent(ctx context.Context, roomID string, event *persist.Event, recipients []pushRecipient) {
	if event == nil || len(recipients) == 0 {
		return
	}
	for _, recipient := range recipients {
		pushers, err := s.db.PusherRepo.List(recipient.UserID)
		if err != nil {
			log.Printf("push: cannot list the pushers of %s: %v", recipient.UserID, err)
			continue
		}
		// One request per gateway and format per recipient: the numbers at the top of the
		// body belong to this recipient, and devices that share a gateway and a format
		// have nothing to disagree about.
		groups := map[string]*pushGroup{}
		var order []string
		for _, pusher := range pushers {
			// Only the http kind is a request; an email pusher wants an email, which this
			// server does not send, and an inactive pusher is a device that asked not to be
			// woken.
			if pusher.Kind != "http" || !pusher.Active {
				continue
			}
			data := matrix.MustParseObject([]byte(pusher.Data))
			url, _ := data["url"].(string)
			if url == "" {
				continue
			}
			format, _ := data["format"].(string)
			key := url + "\x00" + format
			group := groups[key]
			if group == nil {
				group = &pushGroup{url: url, format: format}
				groups[key] = group
				order = append(order, key)
			}
			group.owners = append(group.owners, pushOwner{UserID: pusher.UserID, AppID: pusher.AppID, PushKey: pusher.PushKey})
			group.devices = append(group.devices, pushDevice{
				AppID:     pusher.AppID,
				PushKey:   pusher.PushKey,
				PushKeyTS: pushkeyTS(pusher, s.now()),
				// The pusher's own data travels along, so a gateway can tell two accounts
				// of one app apart from the payload alone.
				Data:   data,
				Tweaks: tweaksOf(recipient.Actions),
			})
		}
		if len(order) == 0 {
			continue
		}
		counts, highlights, err := s.unreadCount(ctx, recipient.UserID, roomID, 0)
		if err != nil {
			log.Printf("push: cannot count the unread of %s in %s: %v", recipient.UserID, roomID, err)
			continue
		}
		for _, key := range order {
			group := groups[key]
			body, err := json.Marshal(map[string]any{
				"notification": group.notification(event, recipient, counts, highlights),
			})
			if err != nil {
				log.Printf("push: cannot render the notification for %s: %v", recipient.UserID, err)
				continue
			}
			s.queuePush(pushTask{
				url:     group.url,
				what:    fmt.Sprintf("the notification of %s for %s", recipient.UserID, event.EventID),
				body:    body,
				devices: group.owners,
			})
		}
	}
}

// pushRecipient is one participant the rules just allowed a notification for, with the
// actions of the rule that matched - which is what decides the tweaks and the priority.
type pushRecipient struct {
	UserID  string
	Actions matrix.Array
}

// pushGroup is the set of devices one request carries.
type pushGroup struct {
	url     string
	format  string
	devices []pushDevice
	owners  []pushOwner
}

// notification renders the body for one recipient.
func (g *pushGroup) notification(event *persist.Event, recipient pushRecipient, counts, highlights int64) pushNotification {
	body := pushNotification{
		EventID: event.EventID,
		RoomID:  event.RoomID,
		Counts:  &pushCounts{Unread: counts, Highlight: highlights},
		// The specification sends this flag when there is more than one notification to
		// read, which is how an app knows to show a number instead of a dot.
		Unread: counts > 1,
		// A notification that is allowed to make a sound is worth waking the device for;
		// one that is not may wait for the screen.
		Prio:    "low",
		Devices: g.devices,
	}
	if push.HasTweak(recipient.Actions, "sound") {
		body.Prio = "high"
	}
	if g.format == "event_id_only" {
		// The app asked to be told only that something happened, and will fetch it. The
		// specification is explicit that this is where `content` and `sender` stop.
		return body
	}
	body.Sender = event.Sender
	body.Type = event.Type
	body.Content = contentWithDefaultPayload(event, g.devices)
	return body
}

// contentWithDefaultPayload is the event content as a full-format pusher should see
// it: what the event carries, with the payload the pusher asked to have standing in it
// underneath.
func contentWithDefaultPayload(event *persist.Event, devices []pushDevice) matrix.Object {
	content := matrix.MustParseObject([]byte(event.Content))
	var payload matrix.Object
	for _, device := range devices {
		if candidate, _ := device.Data["default_payload"].(matrix.Object); len(candidate) > 0 {
			payload = candidate
			break
		}
	}
	if payload == nil {
		return content
	}
	merged := matrix.Object{}
	for key, value := range payload {
		merged[key] = value
	}
	for key, value := range content {
		merged[key] = value
	}
	return merged
}

// tweaksOf turns the tweak actions of the matched rule into the `tweaks` list a gateway
// reads: highlight, sound, and anything else a rule name set.
func tweaksOf(actions matrix.Array) []matrix.Object {
	var tweaks []matrix.Object
	for _, action := range actions {
		entry, ok := action.(matrix.Object)
		if !ok {
			continue
		}
		name, _ := entry["set_tweak"].(string)
		if name == "" {
			continue
		}
		tweak := matrix.Object{"set_tweak": name}
		// A tweak without a value means the default one, and the spec keeps the field out
		// rather than sending a null.
		if value, ok := entry["value"]; ok {
			tweak["value"] = value
		}
		tweaks = append(tweaks, tweak)
	}
	return tweaks
}

// pushkeyTS is when the endpoint was registered, in seconds, which is what a gateway
// uses to tell a pushkey it has just renewed from an old one of the same device.
func pushkeyTS(pusher *persist.Pusher, now time.Time) int64 {
	if pusher.CreatedAt.IsZero() {
		return now.Unix()
	}
	return pusher.CreatedAt.Unix()
}
