package federation

import (
	"fmt"
	"net"
)

// IPpolicy decides which addresses this server will dial when it goes looking for
// another homeserver.
//
// It exists because server discovery is a chain of things a stranger can influence: a
// delegation document, an SRV record, a DNS answer. Without a policy, a name on the
// Matrix network is enough to make this server open a connection to whatever the owner
// of that name says — including an address inside this server's own network. That is
// SSRF with the DNS done for you.
//
// The default denies loopback, the unspecified address, link-local (which is where the
// cloud instance metadata services live), carrier-grade NAT, and multicast. Private
// ranges are denied too, as Synapse's default does: federating with a host named
// publicly is the normal case, and a site that federates over a VPN or a private
// network can say so in configuration rather than every deployment inheriting an
// open door.
type IPpolicy struct {
	allow []*net.IPNet
	deny  []*net.IPNet
}

// NewIPpolicy parses allow and deny CIDR lists. A malformed entry is an error at
// startup rather than a silently missing rule, because a rule that vanished is the
// difference between a guard and an open door.
func NewIPpolicy(allow, deny []string) (IPpolicy, error) {
	policy := IPpolicy{}
	for _, cidr := range allow {
		network, err := parseNetwork(cidr)
		if err != nil {
			return IPpolicy{}, fmt.Errorf("federation: the allowed network %q is unusable: %w", cidr, err)
		}
		policy.allow = append(policy.allow, network)
	}
	for _, cidr := range deny {
		network, err := parseNetwork(cidr)
		if err != nil {
			return IPpolicy{}, fmt.Errorf("federation: the denied network %q is unusable: %w", cidr, err)
		}
		policy.deny = append(policy.deny, network)
	}
	return policy, nil
}

// DefaultIPpolicy is the deny-by-default policy.
func DefaultIPpolicy() (IPpolicy, error) {
	return NewIPpolicy(nil, []string{
		"127.0.0.0/8",
		"::1/128",
		"0.0.0.0/8",
		"::/128",
		"169.254.0.0/16",
		"fe80::/10",
		"100.64.0.0/10",
		"224.0.0.0/4",
		"ff00::/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",
	})
}

// Allow refuses an address the policy denies, or that no rule allows when an allow
// list is configured.
func (p IPpolicy) Allow(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("federation: cannot dial a missing address")
	}
	for _, network := range p.deny {
		if network.Contains(ip) {
			return fmt.Errorf("federation: refusing to connect to %s, which is in the denied network %s", ip, network)
		}
	}
	if len(p.allow) == 0 {
		return nil
	}
	for _, network := range p.allow {
		if network.Contains(ip) {
			return nil
		}
	}
	return fmt.Errorf("federation: refusing to connect to %s, which no allowed network covers", ip)
}

// parseNetwork accepts a CIDR block or a bare address, which means that address alone.
func parseNetwork(cidr string) (*net.IPNet, error) {
	if _, network, err := net.ParseCIDR(cidr); err == nil {
		return network, nil
	}
	ip := net.ParseIP(cidr)
	if ip == nil {
		return nil, fmt.Errorf("%q is neither an address nor a CIDR block", cidr)
	}
	mask := net.CIDRMask(32, 32)
	if ip.To4() == nil {
		mask = net.CIDRMask(128, 128)
	}
	return &net.IPNet{IP: ip, Mask: mask}, nil
}
