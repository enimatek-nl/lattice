package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	// clients hands out the per-destination HTTP clients that verify a certificate against
	// the server's name while dialing the address that name resolved to.
	clients *nameVerifyingClients
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
		resolver: resolver,
		HTTP:     httpClient,
		clients:  newNameVerifyingClients(httpClient),
		origin:   origin,
		signer:   signer,
		log:      log,
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
	response, _, err := c.send(ctx, destination, method, target, body, maxFederationAuthBodyBytes)
	return response, err
}

// GetRaw sends a signed GET and returns the answer's status, headers and bytes.
//
// The size bound is a parameter rather than the signing limit because not everything a server sends is
// JSON: the content repository answers with file bytes, and the caller — which knows how much it is willing
// to hold — has to say how much may be read. Pulling a remote server's file into memory under a fixed
// internal cap is one way for a server to run out of memory.
func (c *Client) GetRaw(ctx context.Context, destination, target string, maxBytes int64) (Response, http.Header, error) {
	return c.send(ctx, destination, http.MethodGet, target, nil, maxBytes)
}

// send is one signed request, with the response's headers kept.
func (c *Client) send(ctx context.Context, destination, method, target string, body []byte, maxBytes int64) (Response, http.Header, error) {
	resolved, err := c.resolver.Resolve(ctx, destination)
	if err != nil {
		return Response{}, nil, err
	}
	header, err := AuthorizationHeader(c.origin, c.signer, destination, method, target, body)
	if err != nil {
		return Response{}, nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, resolved.DialURL+target, reader)
	if err != nil {
		return Response{}, nil, fmt.Errorf("federation: cannot build the request for %s: %w", destination, err)
	}
	request.Host = resolved.HostHeader
	request.Header.Set("Authorization", header)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.clientFor(resolved).Do(request)
	if err != nil {
		return Response{}, nil, fmt.Errorf("federation: the request to %s failed: %w", destination, err)
	}
	defer func() { _ = response.Body.Close() }()
	limit := maxBytes
	if limit <= 0 {
		limit = maxFederationAuthBodyBytes
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return Response{}, nil, fmt.Errorf("federation: the answer from %s could not be read: %w", destination, err)
	}
	return Response{StatusCode: response.StatusCode, Body: payload}, response.Header, nil
}

// clientFor picks the HTTP client for one resolved target: one that verifies the certificate
// against the server's name while connecting to the address that name resolved to. See
// [nameVerifyingClients] for why that distinction is not cosmetic.
func (c *Client) clientFor(resolved Target) *http.Client {
	return c.clients.clientFor(resolved)
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
