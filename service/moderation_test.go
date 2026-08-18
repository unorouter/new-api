package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func moderationTestContext(t *testing.T, setting *types.UserSetting) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if setting != nil {
		common.SetContextKey(c, constant.ContextKeyUserSetting, *setting)
	}
	return c
}

// The exemption is the only bypass of the moderation gate, so an absent or
// unset grant must never read as exempt.
func TestModerationExempt(t *testing.T) {
	assert.False(t, ModerationExempt(nil), "nil context must not be exempt")
	assert.False(t, ModerationExempt(moderationTestContext(t, nil)),
		"context without a user setting must not be exempt")
	assert.False(t, ModerationExempt(moderationTestContext(t, &types.UserSetting{})),
		"default user setting must not be exempt")
	assert.False(t, ModerationExempt(moderationTestContext(t, &types.UserSetting{UnlimitedFreeModels: true})),
		"a different grant must not confer moderation exemption")
	assert.True(t, ModerationExempt(moderationTestContext(t, &types.UserSetting{ModerationExempt: true})))
}

// The reason string is user-facing (it lands in the usage log and the 400 body),
// so it must name the cause and never leak the provider's raw decision word.
func TestModerationDenyReason(t *testing.T) {
	assert.Empty(t, ModerationDenyReason(nil))
	assert.Empty(t, ModerationDenyReason(ErrModerationFailure),
		"an operational failure is not a denial")

	scored := ModerationDenyReason(&ModerationDenyError{Category: "sexual/minors", Score: 0.91, Threshold: 0.2})
	require.NotEmpty(t, scored)
	assert.Contains(t, scored, "Inappropriate prompt")
	assert.Contains(t, scored, "sexual/minors", "the triggering category is actionable, keep it")

	// Decision-based providers (Creem) carry a bare word like "deny" as the
	// category, which meant nothing to users and read like a provider fault.
	decision := ModerationDenyReason(&ModerationDenyError{Category: "deny"})
	require.NotEmpty(t, decision)
	assert.Contains(t, decision, "Inappropriate prompt")
	assert.NotContains(t, decision, "deny")
}
