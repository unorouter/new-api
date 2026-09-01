package controller

import (
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

// ReturnBaseHeader lets a frontend say where checkout should send the user
// back to. Without it a payment started on this service's own console would
// return to the configured FrontendAddress, i.e. a different site than the one
// the user was on.
const ReturnBaseHeader = "X-Return-Base"

// paymentReturnBase resolves the origin a payment should return to, preferring
// what the caller asked for. The header is only honored when it matches a
// configured address: it lands in a redirect the user follows after paying, so
// an arbitrary value would be an open redirect.
func paymentReturnBase(c *gin.Context) string {
	fallback := strings.TrimRight(system_setting.UserLinkBase(), "/")
	if c == nil {
		return fallback
	}
	want := strings.TrimRight(c.GetHeader(ReturnBaseHeader), "/")
	if want == "" {
		return fallback
	}
	for _, allowed := range []string{system_setting.FrontendAddress, system_setting.ServerAddress} {
		if allowed == "" {
			continue
		}
		if sameOrigin(want, strings.TrimRight(allowed, "/")) {
			return want
		}
	}
	return fallback
}

func sameOrigin(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return ua.Scheme == ub.Scheme && ua.Host == ub.Host
}

// paymentReturnPath builds the URL a provider sends the user back to after
// checkout. suffix is a path on THIS service's console; when the return base is
// a separate frontend its equivalent is used instead, since the console routes
// do not exist there.
func paymentReturnPath(c *gin.Context, suffix string) string {
	base := paymentReturnBase(c)
	if system_setting.FrontendAddress == "" ||
		!sameOrigin(base, strings.TrimRight(system_setting.FrontendAddress, "/")) {
		return base + suffix
	}
	return base + frontendReturnPath(suffix)
}

// Console paths mapped to their frontend equivalents. An unmapped suffix falls
// back to the wallet, which is where a user who just paid expects to land.
func frontendReturnPath(suffix string) string {
	path, _, _ := strings.Cut(suffix, "?")
	switch path {
	case "/console/log", "/usage-logs":
		return "/logs"
	case "/console/subscription", "/console/topup", "/wallet":
		return "/billing"
	}
	return "/billing"
}
