package middleware

import (
	"log"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// configureRemoteIPHeaders picks which header gin reads the client address from.
//
// gin defaults to X-Forwarded-For then X-Real-IP. Both are APPEND-style: a client
// may send its own value and the proxy adds to it, so gin walks the chain right to
// left and stops at the first untrusted hop. That only works when the trusted set
// is exactly the proxies. Ours cannot be: cloudflared pods take ephemeral addresses
// out of the pod CIDR, so trusting the CIDR also trusts every other workload in the
// cluster, and any of them can forge the source address (verified 2026-08-27 -- a
// request from the BFF pod carrying "X-Forwarded-For: 8.8.8.8" was audited as
// 8.8.8.8).
//
// CF-Connecting-IP does not have that shape. Cloudflare's edge OVERWRITES it on
// every request rather than appending, so a value a client supplies is discarded
// before the tunnel ever sees it. The tunnel is the only route to this service
// (api.unorouter.com maps straight at it), which makes the header authoritative
// here. Set CLIENT_IP_HEADERS to override for a deployment that does not sit
// behind Cloudflare.
// configuredClientIPHeaders records what configureRemoteIPHeaders handed gin, so
// callers that need to know whether a forwarding header was actually sent read the
// same list gin resolves against instead of keeping their own copy. controller's
// copy said X-Forwarded-For long after gin had been switched to CF-Connecting-IP,
// which silently disabled the per-IP registration cap.
var configuredClientIPHeaders = []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP"}

// ClientIPHeaders returns the headers gin reads the client address from.
func ClientIPHeaders() []string {
	return configuredClientIPHeaders
}

func configureRemoteIPHeaders(engine *gin.Engine) {
	if raw := strings.TrimSpace(os.Getenv("CLIENT_IP_HEADERS")); raw != "" {
		headers := make([]string, 0, 2)
		for _, part := range strings.Split(raw, ",") {
			if h := strings.TrimSpace(part); h != "" {
				headers = append(headers, h)
			}
		}
		if len(headers) > 0 {
			engine.RemoteIPHeaders = headers
			configuredClientIPHeaders = headers
			return
		}
	}
	engine.RemoteIPHeaders = []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP"}
	configuredClientIPHeaders = engine.RemoteIPHeaders
}

func ConfigureTrustedProxies(engine *gin.Engine) error {
	configureRemoteIPHeaders(engine)
	trustedProxies, usedDefaults, err := common.ResolveTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return err
	}
	if usedDefaults {
		log.Print("WARNING: TRUSTED_PROXIES is unset or blank; trusting loopback, RFC 1918, and IPv6 ULA proxy addresses for compatibility. Set TRUSTED_PROXIES=none to trust no proxies, or configure explicit proxy IPs/CIDRs to replace these defaults.")
	}
	return common.ConfigureTrustedProxies(engine, trustedProxies)
}
