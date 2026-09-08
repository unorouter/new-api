package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func runNoPATForPrivileged(t *testing.T, role int, viaPAT bool, service string) int {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/token/", nil)
	c.Set("role", role)
	c.Set("use_access_token", viaPAT)
	if service != "" {
		c.Set(service, true)
	}
	NoPATForPrivileged()(c)
	if !c.IsAborted() {
		return http.StatusOK
	}
	return w.Code
}

// Only a privileged account presenting a PAT is refused; sessions, ordinary
// users and the service tokens keep working.
func TestNoPATForPrivilegedRefusesOnlyPrivilegedPATs(t *testing.T) {
	assert.Equal(t, http.StatusForbidden, runNoPATForPrivileged(t, common.RoleRootUser, true, ""), "root PAT")
	assert.Equal(t, http.StatusForbidden, runNoPATForPrivileged(t, common.RoleAdminUser, true, ""), "admin PAT")
	assert.Equal(t, http.StatusOK, runNoPATForPrivileged(t, common.RoleCommonUser, true, ""), "ordinary user PAT")
	assert.Equal(t, http.StatusOK, runNoPATForPrivileged(t, common.RoleRootUser, false, ""), "root session")
	assert.Equal(t, http.StatusOK, runNoPATForPrivileged(t, common.RoleAdminUser, true, botAuthContextKey), "bot service token")
	assert.Equal(t, http.StatusOK, runNoPATForPrivileged(t, common.RoleAdminUser, true, syncAuthContextKey), "sync service token")
}
