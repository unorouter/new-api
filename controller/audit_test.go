package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An audit write must never be what fails the request it is recording. Both
// helpers reach into c.Request (directly and via ClientIP), and a context
// without one is ordinary in handler tests, so an unguarded read turned every
// newly-audited handler into a panic.
func TestRecordUserSecurityAuditSurvivesIncompleteContext(t *testing.T) {
	bare, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, bare.Request, "fixture must model the no-Request case")

	assert.NotPanics(t, func() {
		recordUserSecurityAudit(bare, 1, "token.create", map[string]interface{}{"id": 1})
	})
	assert.NotPanics(t, func() {
		recordUserSecurityAudit(nil, 1, "token.create", nil)
	})

	withReq, _ := gin.CreateTestContext(httptest.NewRecorder())
	withReq.Request = httptest.NewRequest(http.MethodPost, "/api/token/", nil)
	assert.NotPanics(t, func() {
		recordUserSecurityAudit(withReq, 1, "token.create", map[string]interface{}{"id": 1})
	})
}

// The English fallback is what a log consumer reads when no localized template
// applies, so a newly-audited action with no entry silently degrades to a bare
// action string with none of its params.
func TestAuditContentTemplatesCoverNewlyAuditedActions(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"token.create":       {"id": 27952, "name": "backup-0827"},
		"token.delete":       {"id": 27952, "name": "backup-0827"},
		"token.delete_batch": {"count": 3},
		"user.email_bind":    {"from": "old@example.com", "to": "new@example.com"},
		"user.oauth_bind":    {"provider": "discord", "provider_user_id": "123"},
		"user.oauth_unbind":  {"provider": "discord"},
	}
	for action, params := range cases {
		t.Run(action, func(t *testing.T) {
			rendered := auditContentEN(action, params)
			assert.NotEqual(t, action, rendered, "action has no English template registered")
			assert.NotContains(t, rendered, "${", "template left an unfilled placeholder")
		})
	}
}

// The old address only exists in the row about to be overwritten, so a bind
// that records just the new one cannot answer whether it was a redirect.
func TestEmailBindAuditRecordsBothAddresses(t *testing.T) {
	rendered := auditContentEN("user.email_bind", map[string]interface{}{
		"from": "owner@example.com",
		"to":   "attacker@example.com",
	})
	assert.Contains(t, rendered, "owner@example.com")
	assert.Contains(t, rendered, "attacker@example.com")
}
