package notify

import (
	"net/url"
	"strings"
)

var webPushHosts = map[string]bool{
	"fcm.googleapis.com":                true,
	"android.googleapis.com":            true,
	"updates.push.services.mozilla.com": true,
	"web.push.apple.com":                true,
}

var webPushHostSuffixes = []string{".notify.windows.com", ".push.apple.com"}

// ValidWebPushEndpoint admits only the browser vendors' push services. The
// endpoint is caller supplied and the server POSTs to it, so anything wider is a
// blind request forger (2026-11-11: the Kubernetes API, loopback and the
// metadata service were all registered as push endpoints).
func ValidWebPushEndpoint(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" && port != "443" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if webPushHosts[host] {
		return true
	}
	for _, suffix := range webPushHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
