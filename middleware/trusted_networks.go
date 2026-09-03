package middleware

import (
	"net/netip"
	"os"
	"strings"
	"sync"
)

// The networks the operator and the cluster itself act from. Read once from
// TRUSTED_NETWORKS (comma-separated CIDRs, materialised from OpenBao
// secret/newapi-env), so the list has exactly one home instead of being
// re-derived in three places: the pod CIDR that isInternalClient hardcoded, the
// operator prefixes the security queries inferred from root 2FA logins, and the
// node egress addresses those queries inferred from service-token history.
//
// Audit rows are stamped with the answer at write time. Reading it back from the
// row later is what lets a query say "foreign" without guessing, and it keeps
// working after the cluster moves nodes, because the list moves with it.
//
// Loopback and the k3s pod CIDR are always trusted even when the variable is
// unset, so the in-cluster callers that were exempt before stay exempt.
var (
	trustedNetworksOnce sync.Once
	trustedNetworks     []netip.Prefix
)

var builtinTrustedNetworks = []string{"10.42.0.0/16", "127.0.0.1/32", "::1/128"}

func loadTrustedNetworks() {
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			return
		}
		if p, err := netip.ParsePrefix(raw); err == nil {
			trustedNetworks = append(trustedNetworks, p)
			seen[raw] = true
			return
		}
		// A bare address is accepted as a /32 or /128 so the list can hold
		// single hosts without the operator having to remember the suffix.
		if a, err := netip.ParseAddr(raw); err == nil {
			trustedNetworks = append(trustedNetworks, netip.PrefixFrom(a, a.BitLen()))
			seen[raw] = true
		}
	}
	for _, raw := range builtinTrustedNetworks {
		add(raw)
	}
	for _, raw := range strings.Split(os.Getenv("TRUSTED_NETWORKS"), ",") {
		add(raw)
	}
}

// IsTrustedNetwork reports whether ip falls inside the operator or cluster
// networks. Unparseable input, including the "unknown" gin returns when no
// client address is available, counts as trusted: it is our own plumbing, not
// a stranger.
func IsTrustedNetwork(ip string) bool {
	trustedNetworksOnce.Do(loadTrustedNetworks)
	ip = strings.TrimSpace(ip)
	if ip == "" || ip == "unknown" {
		return true
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return true
	}
	a = a.Unmap()
	for _, p := range trustedNetworks {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
