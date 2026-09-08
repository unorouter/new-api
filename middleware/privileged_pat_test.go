package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A PAT on a privileged row must not authenticate, however the row was written:
// the value that was replayed from Tor on 2026-09-08 had no mint event at all.
func TestPrivilegedAccessTokenIsRefusedAtAuth(t *testing.T) {
	setupDashboardAuthMiddlewareTest(t)

	seed := func(role int, token string) {
		u := model.User{Username: "u" + token[:9], Password: "x", Role: role, Status: common.UserStatusEnabled, AccessToken: &token, AffCode: token[:9]}
		require.NoError(t, model.DB.Create(&u).Error)
	}
	adminToken := "pat-admin-000000000000000000000"
	userToken := "pat-user-0000000000000000000000"
	seed(common.RoleAdminUser, adminToken)
	seed(common.RoleCommonUser, userToken)

	classify := func(token string) (*model.UserBase, error) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/api/user/self", nil)
		c.Request.Header.Set("Authorization", "Bearer "+token)
		u, _, _, err := classifyDashboardCredential(c)
		return u, err
	}

	u, err := classify(userToken)
	assert.NoError(t, err, "an ordinary user's PAT still authenticates")
	assert.NotNil(t, u)

	u, err = classify(adminToken)
	assert.Error(t, err, "an admin's PAT must be refused")
	assert.Nil(t, u)
}
