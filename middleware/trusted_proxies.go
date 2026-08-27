package middleware

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

var defaultTrustedProxyCIDRs = []string{
	"127.0.0.0/8",
	"::1",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

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
			return
		}
	}
	engine.RemoteIPHeaders = []string{"CF-Connecting-IP", "X-Forwarded-For", "X-Real-IP"}
}

func ConfigureTrustedProxies(engine *gin.Engine) error {
	configureRemoteIPHeaders(engine)
	rawTrustedProxies := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if rawTrustedProxies == "" {
		log.Print("WARNING: TRUSTED_PROXIES is unset or blank; trusting loopback, RFC 1918, and IPv6 ULA proxy addresses for compatibility. Set TRUSTED_PROXIES=none to trust no proxies, or configure explicit proxy IPs/CIDRs to replace these defaults.")
		return engine.SetTrustedProxies(defaultTrustedProxyCIDRs)
	}
	if strings.EqualFold(rawTrustedProxies, "none") {
		return engine.SetTrustedProxies(nil)
	}

	parts := strings.Split(rawTrustedProxies, ",")
	trustedProxies := make([]string, 0, len(parts))
	for _, part := range parts {
		trustedProxy := strings.TrimSpace(part)
		if trustedProxy == "" {
			continue
		}
		if strings.EqualFold(trustedProxy, "none") {
			return errors.New("TRUSTED_PROXIES=none must be used alone")
		}
		trustedProxies = append(trustedProxies, trustedProxy)
	}
	if len(trustedProxies) == 0 {
		return errors.New("TRUSTED_PROXIES does not contain an IP address or CIDR")
	}
	if err := engine.SetTrustedProxies(trustedProxies); err != nil {
		return fmt.Errorf("invalid TRUSTED_PROXIES: %w", err)
	}
	return nil
}
