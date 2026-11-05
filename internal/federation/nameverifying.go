package federation

import (
	"crypto/tls"
	"net/http"
	"sync"
)

// nameVerifyingClients hands out HTTP clients that verify a server's certificate against the
// server's *name* while connecting to the address that name resolved to.
//
// The specification's server-name resolution says so plainly: when a server name carries a
// hostname, the certificate presented must be valid for that hostname and the TLS server name is
// the hostname, whichever address it happens to resolve to. Go verifies against the host in the
// request URL, and the URL this project dials is built from the resolved address — so without this,
// a homeserver whose certificate names only its domain is rejected for not naming the IP someone
// happened to connect to. That is not a peer being picky: the IP proves nothing about who is on the
// other end, and the name is the whole point of the certificate.
//
// One client per destination name is kept rather than one per request, so that connection reuse
// survives the per-destination TLS configuration.
type nameVerifyingClients struct {
	base *http.Client

	mu     sync.Mutex
	byName map[string]*http.Client
}

// newNameVerifyingClients wraps base. A nil base gets a client with the timeouts a federation peer
// needs.
func newNameVerifyingClients(base *http.Client) *nameVerifyingClients {
	if base == nil {
		base = &http.Client{}
	}
	return &nameVerifyingClients{base: base, byName: map[string]*http.Client{}}
}

// clientFor returns the client to use for a resolved target. When the address dialed is an IP that
// a name resolved to, the returned client verifies against the name; otherwise the base client is
// enough and no per-destination client is built.
func (n *nameVerifyingClients) clientFor(resolved Target) *http.Client {
	if n.base == nil || resolved.HostHeader == "" ||
		!hostIsIP(uHost(resolved.DialURL)) || hostIsIP(resolved.HostHeader) {
		return n.base
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if existing, ok := n.byName[resolved.HostHeader]; ok {
		return existing
	}
	HTTP := *n.base
	var transport *http.Transport
	switch current := n.base.Transport.(type) {
	case *http.Transport:
		transport = current.Clone()
	case nil:
		// No transport of its own: the client is using the library default, which has to be
		// copied rather than assumed, or the name check silently does nothing.
		transport = http.DefaultTransport.(*http.Transport).Clone()
	default:
		// A transport this project does not understand is left alone. The request still goes
		// out; the certificate is then checked against whatever that transport does.
		return n.base
	}
	config := transport.TLSClientConfig
	if config == nil {
		config = &tls.Config{}
	}
	config = config.Clone()
	config.ServerName = resolved.HostHeader
	transport.TLSClientConfig = config
	HTTP.Transport = transport
	n.byName[resolved.HostHeader] = &HTTP
	return &HTTP
}
