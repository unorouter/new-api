package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// publicClientIp decides what address gets persisted as a register IP, which
// gates per-IP account uniqueness. Contract: only a genuinely forwarded, public
// client address is returned; a lost header or an untrusted chain yields "",
// never the socket peer (our own tunnel/pod egress address).
func TestPublicClientIp(t *testing.T) {
	cases := []struct {
		name         string
		trustedProxy string
		remoteAddr   string
		forwardedFor string
		realIp       string
		expected     string
	}{
		{
			name:         "forwarded public ip through trusted proxy is stored",
			trustedProxy: "10.0.0.0/8",
			remoteAddr:   "10.42.0.5:1234",
			forwardedFor: "203.0.113.7",
			expected:     "203.0.113.7",
		},
		{
			name:         "x-real-ip is honored when x-forwarded-for is absent",
			trustedProxy: "10.0.0.0/8",
			remoteAddr:   "10.42.0.5:1234",
			realIp:       "203.0.113.9",
			expected:     "203.0.113.9",
		},
		{
			name:         "no forwarding header falls back to nothing, never the peer",
			trustedProxy: "10.0.0.0/8",
			remoteAddr:   "198.51.100.7:1234",
			expected:     "",
		},
		{
			name:         "untrusted proxy cannot inject a client ip",
			trustedProxy: "192.168.0.0/16",
			remoteAddr:   "198.51.100.7:1234",
			forwardedFor: "203.0.113.7",
			expected:     "",
		},
		{
			name:         "private forwarded address is not a public identity",
			trustedProxy: "10.0.0.0/8",
			remoteAddr:   "10.42.0.5:1234",
			forwardedFor: "10.42.0.201",
			expected:     "",
		},
		{
			name:         "loopback forwarded address is rejected",
			trustedProxy: "10.0.0.0/8",
			remoteAddr:   "10.42.0.5:1234",
			forwardedFor: "127.0.0.1",
			expected:     "",
		},
		{
			name:         "garbage header value is rejected",
			trustedProxy: "10.0.0.0/8",
			remoteAddr:   "10.42.0.5:1234",
			forwardedFor: "not-an-ip",
			expected:     "",
		},
		{
			name:         "ipv6 client is preserved",
			trustedProxy: "10.0.0.0/8",
			remoteAddr:   "10.42.0.5:1234",
			forwardedFor: "2001:db8::1",
			expected:     "2001:db8::1",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			require.NoError(t, engine.SetTrustedProxies([]string{tc.trustedProxy}))

			var got string
			engine.GET("/", func(c *gin.Context) {
				got = publicClientIp(c)
			})

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tc.forwardedFor)
			}
			if tc.realIp != "" {
				req.Header.Set("X-Real-IP", tc.realIp)
			}
			engine.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tc.expected, got)
		})
	}
}
