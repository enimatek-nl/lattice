package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enimatek/lattice/internal/matrixid"
)

// Target is where a server name was found to live.
//
// The three fields differ on purpose. The specification requires requests to be dialled
// at the resolved address while carrying the name that the certificate is expected for,
// so a delegate cannot answer for a name it was not told to answer for. Collapsing them
// into one URL would let a delegated server present a certificate for itself and be
// believed.
type Target struct {
	// ServerName is the name that was resolved, with its port if it had one.
	ServerName string
	// DialURL is the scheme, resolved address and port to connect to.
	DialURL string
	// HostHeader is the value for the Host header, and the name the TLS certificate
	// must be valid for.
	HostHeader string
}

// Resolver turns a server name into a target.
type Resolver interface {
	Resolve(ctx context.Context, serverName string) (Target, error)
}

// StaticResolver answers from a table, falling back to another resolver for names the
// table does not cover. A test of two homeservers on one machine has no DNS and no TLS
// listener; this replaces only the addressing, leaving discovery's rules, the signing
// and the checking untouched.
type StaticResolver struct {
	Targets  map[string]Target
	Fallback Resolver
}

// Resolve implements [Resolver].
func (r *StaticResolver) Resolve(ctx context.Context, serverName string) (Target, error) {
	if target, ok := r.Targets[serverName]; ok {
		return target, nil
	}
	if r.Fallback != nil {
		return r.Fallback.Resolve(ctx, serverName)
	}
	return Target{}, fmt.Errorf("federation: %q has no known address", serverName)
}

// wellKnownPath is the delegation document every server name is given the chance to
// publish.
const wellKnownPath = "/.well-known/matrix/server"

// Discovery timings, from the specification's recommendations.
const (
	wellKnownDefaultTTL = 24 * time.Hour
	wellKnownMaxTTL     = 48 * time.Hour
	wellKnownErrorTTL   = time.Hour
	// srvService is the registered service name; srvLegacy is the unregistered one
	// that the specification still asks servers to try before giving up.
	srvService = "_matrix-fed._tcp."
	srvLegacy  = "_matrix._tcp."
	// defaultFederationPort is the port a server name without one implies.
	defaultFederationPort = 8448
)

// Discovery resolves server names as the Server-Server API describes: a published
// delegation, then an SRV record, then the name itself on the federation port.
type Discovery struct {
	// HTTP fetches the /.well-known document. It must not follow redirects into
	// arbitrary hosts unbounded; the client supplied here is expected to have a
	// timeout.
	HTTP *http.Client
	// Policy decides whether a resolved address may be dialed.
	Policy IPpolicy
	// LookupSRV and LookupHost are the DNS calls. They are fields so a test can answer
	// them without a DNS server, and so a deployment with its own resolver can plug it
	// in.
	LookupSRV  func(ctx context.Context, name string) ([]*net.SRV, error)
	LookupHost func(ctx context.Context, host string) ([]string, error)
	// Now is the clock, for cache expiry.
	Now func() time.Time

	cache cache
}

// cache holds resolved delegation documents until they expire. A well-known lookup is a
// request to a stranger for every outbound call otherwise, and the specification asks
// for caching with a bound on how long an answer may be trusted.
type cache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	target   Target
	expiry   time.Time
	resolved bool
}

// NewDiscovery returns resolution with the default resolver and clock.
func NewDiscovery(httpClient *http.Client, policy IPpolicy) *Discovery {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Discovery{
		HTTP:   httpClient,
		Policy: policy,
		LookupSRV: func(ctx context.Context, name string) ([]*net.SRV, error) {
			_, records, err := net.DefaultResolver.LookupSRV(ctx, "", "", name)
			return records, err
		},
		LookupHost: func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		},
		Now:   time.Now,
		cache: cache{entries: map[string]cacheEntry{}},
	}
}

// Resolve implements [Resolver].
func (d *Discovery) Resolve(ctx context.Context, serverName string) (Target, error) {
	host, port, err := matrixid.SplitServerName(serverName)
	if err != nil {
		return Target{}, fmt.Errorf("federation: %q is not a server name: %w", serverName, err)
	}
	if d.LookupHost == nil || d.LookupSRV == nil {
		return Target{}, errors.New("federation: discovery has no DNS lookup")
	}

	// An IP literal needs no lookup, and a name with an explicit port is resolved
	// directly: both cases deliberately skip delegation, because the server name
	// already says exactly where to connect.
	if ip := net.ParseIP(host); ip != nil {
		if port == 0 {
			port = defaultFederationPort
		}
		return d.targetFor(ctx, serverName, ip, port, host)
	}
	if port != 0 {
		return d.resolveHost(ctx, serverName, host, port, host)
	}

	if cached, ok := d.cached(serverName); ok {
		if !cached.resolved {
			return Target{}, fmt.Errorf("federation: %q was resolved recently and could not be found", serverName)
		}
		return cached.target, nil
	}

	if delegated, ok, err := d.delegated(ctx, serverName, host); err != nil {
		d.rememberFailure(serverName)
		return Target{}, err
	} else if ok {
		return delegated, nil
	}

	// No delegation: SRV records, then the name itself on the federation port.
	for _, service := range []string{srvService, srvLegacy} {
		if target, ok, err := d.srv(ctx, serverName, host, service); err != nil {
			d.rememberFailure(serverName)
			return Target{}, err
		} else if ok {
			d.remember(serverName, target, wellKnownErrorTTL)
			return target, nil
		}
	}
	target, err := d.resolveHost(ctx, serverName, host, defaultFederationPort, host)
	if err != nil {
		d.rememberFailure(serverName)
		return Target{}, err
	}
	d.remember(serverName, target, wellKnownErrorTTL)
	return target, nil
}

// delegated follows a /.well-known/matrix/server document if the name publishes one.
// The second result says whether one was found; a broken document is not a refusal, it
// is the same as having no document at all.
func (d *Discovery) delegated(ctx context.Context, serverName, host string) (Target, bool, error) {
	url := "https://" + net.JoinHostPort(host, strconv.Itoa(defaultFederationPort)) + wellKnownPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Target{}, false, fmt.Errorf("federation: cannot ask %s about its delegation: %w", host, err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := d.HTTP.Do(request)
	if err != nil {
		// A server that does not answer the well-known question is a server that does
		// not delegate, unless the failure says otherwise.
		return Target{}, false, nil
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return Target{}, false, nil
	}
	ttl := d.cacheTTL(response.Header)
	if response.StatusCode != http.StatusOK {
		d.rememberFailureFor(serverName, ttl)
		return Target{}, false, nil
	}
	var document struct {
		Server string `json:"m.server"`
	}
	if err := json.Unmarshal(body, &document); err != nil || document.Server == "" {
		return Target{}, false, nil
	}
	delegatedHost, delegatedPort, err := matrixid.SplitServerName(document.Server)
	if err != nil {
		// An invalid delegation is ignored rather than trusted.
		return Target{}, false, nil
	}
	if delegatedHost == host {
		// A document that points back at the name that asked is a loop; the name is
		// reached the ordinary way.
		return Target{}, false, nil
	}

	// The certificate must be valid for the name that was asked for, not for the
	// delegate, except when the delegate is an IP literal, where the specification
	// requires a certificate for that address.
	certName := host
	if ip := net.ParseIP(delegatedHost); ip != nil {
		certName = delegatedHost
	}
	port := delegatedPort
	if port == 0 {
		// Without a port the delegate's own SRV records, then the federation port.
		for _, service := range []string{srvService, srvLegacy} {
			if target, ok, err := d.srv(ctx, serverName, delegatedHost, service); err != nil {
				return Target{}, false, err
			} else if ok {
				d.remember(serverName, target, ttl)
				return target, true, nil
			}
		}
		port = defaultFederationPort
	}
	target, err := d.resolveHost(ctx, serverName, delegatedHost, port, certName)
	if err != nil {
		return Target{}, false, err
	}
	d.remember(serverName, target, ttl)
	return target, true, nil
}

// srv looks up one SRV service name for a host and picks the record to use.
func (d *Discovery) srv(ctx context.Context, serverName, host, service string) (Target, bool, error) {
	records, err := d.LookupSRV(ctx, service+host)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return Target{}, false, nil
		}
		return Target{}, false, fmt.Errorf("federation: the %s record for %s could not be read: %w", service, host, err)
	}
	record := pickSRV(records)
	if record == nil {
		return Target{}, false, nil
	}
	if record.Port == 0 {
		// A port of zero means the service is explicitly not available.
		return Target{}, false, nil
	}
	// The certificate is for the name that was asked for, never for the SRV target.
	target, err := d.resolveHost(ctx, serverName, strings.TrimSuffix(record.Target, "."), int(record.Port), host)
	if err != nil {
		return Target{}, false, err
	}
	return target, true, nil
}

// pickSRV chooses the record to use: lowest priority first, and among equal priorities
// the heaviest weight. The weights are a hint for spreading load across several
// destinations, and a homeserver making one call to one server name has one
// destination; retrying elsewhere when a chosen address fails is the queue's job.
func pickSRV(records []*net.SRV) *net.SRV {
	var best *net.SRV
	for _, record := range records {
		if best == nil || record.Priority < best.Priority ||
			(record.Priority == best.Priority && record.Weight > best.Weight) {
			best = record
		}
	}
	return best
}

// resolveHost turns a host into an address that is allowed to be dialed.
func (d *Discovery) resolveHost(ctx context.Context, serverName, host string, port int, certName string) (Target, error) {
	if ip := net.ParseIP(host); ip != nil {
		return d.targetFor(ctx, serverName, ip, port, certName)
	}
	addresses, err := d.LookupHost(ctx, host)
	if err != nil {
		return Target{}, fmt.Errorf("federation: %q cannot be resolved: %w", host, err)
	}
	var lastErr error
	for _, raw := range addresses {
		ip := net.ParseIP(raw)
		if ip == nil {
			continue
		}
		if err := d.Policy.Allow(ip); err != nil {
			lastErr = err
			continue
		}
		return d.targetFor(ctx, serverName, ip, port, certName)
	}
	if lastErr != nil {
		return Target{}, lastErr
	}
	return Target{}, fmt.Errorf("federation: %q resolved to no usable address", host)
}

// targetFor builds the target once an address has been accepted.
func (d *Discovery) targetFor(ctx context.Context, serverName string, ip net.IP, port int, certName string) (Target, error) {
	if err := d.Policy.Allow(ip); err != nil {
		return Target{}, err
	}
	return Target{
		ServerName: serverName,
		DialURL:    "https://" + net.JoinHostPort(ip.String(), strconv.Itoa(port)),
		HostHeader: certName,
	}, nil
}

// cacheTTL reads how long a well-known answer may be kept.
func (d *Discovery) cacheTTL(header http.Header) time.Duration {
	ttl := wellKnownDefaultTTL
	for _, directive := range strings.Split(header.Get("Cache-Control"), ",") {
		directive = strings.TrimSpace(directive)
		if !strings.HasPrefix(directive, "max-age=") {
			continue
		}
		seconds, err := strconv.Atoi(strings.TrimPrefix(directive, "max-age="))
		if err != nil || seconds < 0 {
			continue
		}
		ttl = time.Duration(seconds) * time.Second
	}
	if ttl > wellKnownMaxTTL {
		ttl = wellKnownMaxTTL
	}
	if ttl <= 0 {
		ttl = wellKnownErrorTTL
	}
	return ttl
}

func (d *Discovery) cached(serverName string) (cacheEntry, bool) {
	d.cache.mu.Lock()
	defer d.cache.mu.Unlock()
	entry, ok := d.cache.entries[serverName]
	if !ok || d.now().After(entry.expiry) {
		if ok {
			delete(d.cache.entries, serverName)
		}
		return cacheEntry{}, false
	}
	return entry, true
}

func (d *Discovery) remember(serverName string, target Target, ttl time.Duration) {
	d.cache.mu.Lock()
	defer d.cache.mu.Unlock()
	d.cache.entries[serverName] = cacheEntry{target: target, expiry: d.now().Add(ttl), resolved: true}
}

func (d *Discovery) rememberFailure(serverName string) {
	d.rememberFailureFor(serverName, wellKnownErrorTTL)
}

func (d *Discovery) rememberFailureFor(serverName string, ttl time.Duration) {
	d.cache.mu.Lock()
	defer d.cache.mu.Unlock()
	d.cache.entries[serverName] = cacheEntry{expiry: d.now().Add(ttl)}
}

func (d *Discovery) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// hostIsIP reports whether a host is a bare address rather than a name. Federation's
// policy decisions are made about addresses; its trust decisions are made about names,
// and the two have to be told apart before either is applied.
func hostIsIP(host string) bool { return net.ParseIP(host) != nil }
