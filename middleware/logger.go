package middleware

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const RouteTagKey = "route_tag"

func RouteTag(tag string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(RouteTagKey, tag)
		c.Next()
	}
}

func SetUpLogger(server *gin.Engine) {
	server.Use(redactTaskArtifactAccessQuery())
	server.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Output: common.LogWriter,
		Formatter: func(param gin.LogFormatterParams) string {
			var requestID string
			if param.Keys != nil {
				requestID, _ = param.Keys[common.RequestIdKey].(string)
			}
			tag, _ := param.Keys[RouteTagKey].(string)
			if tag == "" {
				tag = "web"
			}
			path := param.Path
			// OAuth callbacks carry one-time codes and state in the query string.
			// Redact the log value only; the handler still needs the original query.
			if strings.HasPrefix(path, "/api/oauth/") || strings.HasPrefix(path, "/oauth/") {
				path, _, _ = strings.Cut(path, "?")
			} else if param.Request.URL.RawQuery != "" {
				query := param.Request.URL.Query().Encode()
				path = param.Request.URL.EscapedPath() + relaycommon.SanitizeURLForLog("?"+query)
			} else {
				// param.Path keeps the query the request arrived with, including a
				// credential a handler took out of it, as the Gemini API routes do
				// with ?key=.
				path = param.Request.URL.Path
			}
			line := fmt.Sprintf("[GIN] %s | %s | %s | %3d | %13v | %15s | %7s %s",
				param.TimeStamp.Format("2006/01/02 - 15:04:05"),
				tag,
				requestID,
				param.StatusCode,
				param.Latency,
				param.ClientIP,
				param.Method,
				path,
			)
			return line + edgeFingerprint(param) + "\n"
		},
	}))
}

// The x-uno-* headers come from a Cloudflare request header transform, so only edge traffic carries them.
func edgeFingerprint(param gin.LogFormatterParams) string {
	h := param.Request.Header
	httpVersion := h.Get("X-Uno-Http")
	if httpVersion == "" {
		return ""
	}
	userID := 0
	if param.Keys != nil {
		userID, _ = param.Keys["id"].(int)
	}
	ua := h.Get("User-Agent")
	if len(ua) > 200 {
		ua = ua[:200]
	}
	return fmt.Sprintf(" | fp c=%s l=%s e=%s h=%s asn=%s u=%d ua=%s",
		h.Get("X-Uno-Tls-Ciphers"), h.Get("X-Uno-Tls-Hello-Len"), h.Get("X-Uno-Tls-Ext"), httpVersion,
		h.Get("X-Uno-Asn"), userID, strconv.Quote(ua))
}
