package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func runBotAuth(t *testing.T, token, remoteAddr string) (bool, int) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/discord_grant", nil)
	c.Request.RemoteAddr = remoteAddr
	c.Request.Header.Set("Authorization", "Bearer "+token)
	BotAuth()(c)
	return c.GetBool(botAuthContextKey), w.Code
}

// The correct bot token authenticates only from inside the cluster; from a
// foreign address it must not become the bot, whatever else the fallback does.
func TestBotAuthRequiresTrustedNetwork(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)
	t.Setenv("BOT_SERVICE_TOKEN", "bot-secret-for-test")

	asBot, _ := runBotAuth(t, "bot-secret-for-test", "10.42.3.7:41000")
	assert.True(t, asBot, "in-cluster caller with the right token is the bot")

	asBot, code := runBotAuth(t, "bot-secret-for-test", "185.220.101.166:41000")
	assert.False(t, asBot, "a Tor exit with the right token is not the bot")
	assert.NotEqual(t, http.StatusOK, code, "the fallback must not silently succeed")

	asBot, _ = runBotAuth(t, "wrong", "10.42.3.7:41000")
	assert.False(t, asBot, "a wrong token is never the bot")
}
