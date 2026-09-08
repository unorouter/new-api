package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The strict form guards the service credentials, so an origin we cannot place
// inside a trusted prefix must not open the gate. The lenient form keeps the
// opposite reading because it only labels audit rows, where "unknown" means our
// own plumbing rather than a stranger.
func TestTrustedNetworkUnknownOriginSplitsByStrictness(t *testing.T) {
	trustedNetworksOnce.Do(loadTrustedNetworks)
	require.NotEmpty(t, trustedNetworks, "built-in prefixes must load without TRUSTED_NETWORKS set")

	for _, unknown := range []string{"", "   ", "unknown", "not-an-ip", "10.42.0.1extra"} {
		assert.True(t, IsTrustedNetwork(unknown), "audit stamping keeps the benefit of the doubt for %q", unknown)
		assert.False(t, IsTrustedNetworkStrict(unknown), "a credential gate must refuse %q", unknown)
	}
}

// Both forms must agree on addresses they can actually read, otherwise the
// in-cluster callers the gates exist for would start failing.
func TestTrustedNetworkAgreesOnParsableAddresses(t *testing.T) {
	cases := map[string]bool{
		"10.42.3.19":       true, // k3s pod CIDR
		"127.0.0.1":        true, // loopback, the port-forward path
		"::1":              true,
		"::ffff:10.42.0.7": true,  // v4-mapped form of a pod address
		"185.220.101.1":    false, // Tor exit
		"116.203.88.111":   false, // our own node's public address is not the pod CIDR
	}
	for ip, want := range cases {
		assert.Equal(t, want, IsTrustedNetwork(ip), "lenient verdict for %s", ip)
		assert.Equal(t, want, IsTrustedNetworkStrict(ip), "strict verdict for %s", ip)
	}
}
