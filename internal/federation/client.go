package federation

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Client talks to one remote homeserver on this server's behalf, signing every request
// the way the Server-Server API requires.
type Client struct {
	resolver Resolver
	HTTP     *http.Client
	origin   string
	signer   Signer
	log      *slog.Logger
	// mu guards byDestination, which holds one HTTP client per destination so that
	// verifying a certificate against the server's name does not cost a new connection
	// on every request.
	mu            sync.Mutex
	byDestination map[string]*http.Client
}

// NewClient returns a client that signs with signer and reaches servers through the
// resolver.
func NewClient(resolver Resolver, httpClient *http.Client, origin string, signer Signer, log *slog.Logger) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		resolver:      resolver,
		HTTP:          httpClient,
		origin:        origin,
		signer:        signer,
		log:           log,
		byDestination: map[string]*http.Client{},
	}
}

// Response is a remote server's answer: the status it gave and the decoded body.
type Response struct {
	StatusCode int
	Body       []byte
}

// Do signs and sends one request.
//
// The URL dialled is the resolved address, while the Host header carries the name the
// certificate must be valid for. Sending both in one place is what keeps a delegated
// server from being able to answer for a name it was not told to answer for.
func (c *Client) Do(ctx context.Context, destination, method, target string, body []byte) (Response, error) {
	resolved, err := c.resolver.Resolve(ctx, destination)
	if err != nil {
		return Response{}, err
	}
	header, err := AuthorizationHeader(c.origin, c.signer, destination, method, target, body)
	if err != nil {
		return Response{}, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, resolved.DialURL+target, reader)
	if err != nil {
		return Response{}, fmt.Errorf("federation: cannot build the request for %s: %w", destination, err)
	}
	request.Host = resolved.HostHeader
	request.Header.Set("Authorization", header)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.clientFor(resolved).Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("federation: the request to %s failed: %w", destination, err)
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxFederationAuthBodyBytes))
	if err != nil {
		return Response{}, fmt.Errorf("federation: the answer from %s could not be read: %w", destination, err)
	}
	return Response{StatusCode: response.StatusCode, Body: payload}, nil
}

// clientFor picks the HTTP client used for one resolved target.
//
// Discovery answers with an address, because that is what a socket needs, while the
// certificate belongs to the *name*. Go verifies a certificate against the host in the
// URL, so dialing https://203.0.113.9:8448 would check a name nobody asked for and
// reject the very certificate that proves who is on the other end. When the address is
// an IP and the name is not, this uses a client that verifies against the name while
// still connecting to the address. One client per destination is kept so connections are
// reused rather than rebuilt per request.
func (c *Client) clientFor(resolved Target) *http.Client {
	if resolved.HostHeader == "" || !hostIsIP(uHost(resolved.DialURL)) || hostIsIP(resolved.HostHeader) {
		return c.HTTP
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.byDestination[resolved.ServerName]; ok {
		return existing
	}
	HTTP := *c.HTTP
	if base, ok := c.HTTP.Transport.(*http.Transport); ok && base != nil {
		clone := base.Clone()
		config := clone.TLSClientConfig
		if config == nil {
			config = &tls.Config{}
		}
		config = config.Clone()
		config.ServerName = resolved.HostHeader
		clone.TLSClientConfig = config
		HTTP.Transport = clone
	}
	c.byDestination[resolved.ServerName] = &HTTP
	return &HTTP
}

// uHost extracts the host of a URL without caring whether it parses.
func uHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// GetJSON sends a signed GET and decodes the answer, refusing anything that is not a
// 2xx JSON object. A remote server's error body is returned to the caller as an error
// naming the code, because "the remote said M_NOT_FOUND" is worth more than a bare
// failure.
func (c *Client) GetJSON(ctx context.Context, destination, target string) (map[string]any, error) {
	response, err := c.Do(ctx, destination, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return decodeResponse(destination, response)
}

// PostJSON sends a signed POST with a JSON body.
func (c *Client) PostJSON(ctx context.Context, destination, target string, payload any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("federation: cannot encode the request for %s: %w", destination, err)
	}
	response, err := c.Do(ctx, destination, http.MethodPost, target, body)
	if err != nil {
		return nil, err
	}
	return decodeResponse(destination, response)
}

// decodeResponse checks a remote answer and returns its body as an object.
func decodeResponse(destination string, response Response) (map[string]any, error) {
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var problem struct {
			ErrCode string `json:"errcode"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(response.Body, &problem)
		if problem.ErrCode != "" {
			return nil, fmt.Errorf("federation: %s answered %s: %s", destination, problem.ErrCode, problem.Error)
		}
		return nil, fmt.Errorf("federation: %s answered HTTP %d", destination, response.StatusCode)
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		return nil, fmt.Errorf("federation: %s answered something that is not a JSON object: %w", destination, err)
	}
	return decoded, nil
}

// EDU is an ephemeral data unit: a thing worth telling a server right now, and worth
// nothing later.
type EDU struct {
	Type    string         `json:"edu_type"`
	Content map[string]any `json:"content"`
}

// TransactionLimits are the specification's caps on one transaction.
const (
	// MaxPDUs is how many PDUs one transaction may carry.
	MaxPDUs = 50
	// MaxEDUs is how many EDUs one transaction may carry.
	MaxEDUs = 100

	maxPDUsPerTransaction = MaxPDUs
	maxEDUsPerTransaction = MaxEDUs
)

// PDUResult is what a destination said about one PDU.
type PDUResult struct {
	EventID string
	Error   string
}

// SendTransaction PUTs one transaction to a destination and returns what it said about
// each PDU.
//
// The transaction ID is chosen by the sender and is only meaningful to this pair of
// servers; a destination that has seen the ID before answers with what it answered
// then, which is what makes a retry safe.
func (c *Client) SendTransaction(ctx context.Context, destination, txnID string, pdus []json.RawMessage, edus []EDU) ([]PDUResult, error) {
	if len(pdus) > maxPDUsPerTransaction {
		return nil, fmt.Errorf("federation: a transaction may carry %d PDUs, not %d", maxPDUsPerTransaction, len(pdus))
	}
	if len(edus) > maxEDUsPerTransaction {
		return nil, fmt.Errorf("federation: a transaction may carry %d EDUs, not %d", maxEDUsPerTransaction, len(edus))
	}
	if txnID == "" {
		return nil, errors.New("federation: a transaction needs an ID")
	}
	transaction := map[string]any{
		"origin":           c.origin,
		"origin_server_ts": c.now().UnixMilli(),
		"pdus":             pdus,
	}
	if len(edus) > 0 {
		transaction["edus"] = edus
	}
	body, err := json.Marshal(transaction)
	if err != nil {
		return nil, fmt.Errorf("federation: cannot encode the transaction: %w", err)
	}
	response, err := c.Do(ctx, destination, http.MethodPut,
		fmt.Sprintf("/_matrix/federation/v1/send/%s", txnID), body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, fmt.Errorf("federation: %s answered HTTP %d to the transaction", destination, response.StatusCode)
	}
	var answer struct {
		PDUs map[string]struct {
			Error string `json:"error"`
		} `json:"pdus"`
	}
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return nil, fmt.Errorf("federation: %s answered a transaction with something unreadable: %w", destination, err)
	}
	results := make([]PDUResult, 0, len(answer.PDUs))
	for eventID, result := range answer.PDUs {
		results = append(results, PDUResult{EventID: eventID, Error: result.Error})
	}
	return results, nil
}

func (c *Client) now() time.Time { return time.Now().UTC() }
