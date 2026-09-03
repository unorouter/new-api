package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// router/api-router.go is resolved `--ours` on every upstream sync, so routes
// upstream adds to its own copy are silently missing until a page 404s (the
// task-plugin admin page did, 2026-09-01). Pin every upstream route prod has
// folded in by hand; when this fails after a sync, fold the new ones in too.
func TestUpstreamAdminRoutesAreRegistered(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	SetApiRouter(engine, newOpenAPIEngine())

	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"GET /api/plugin/task",
		"POST /api/plugin/task",
		"PUT /api/plugin/task",
		"GET /api/plugin/task/runtime/status",
		"GET /api/plugin/task/marketplace/sources",
		"PUT /api/plugin/task/marketplace/sources",
		"GET /api/plugin/task/:key",
		"GET /api/plugin/task/:key/versions",
		"POST /api/plugin/task/:key/activate",
		"POST /api/plugin/task/:key/status",
		"POST /api/plugin/task/:key/dryrun",
		"DELETE /api/plugin/task/:key/versions/:version",
		"GET /api/task_plugin_options",
		"GET /api/task/:task_id/artifacts",
		"GET /api/user/login/encryption-key",
		"GET /api/authz/catalog",
		"GET /api/channel/:id/codex/usage/reset-credits",
		"POST /api/channel/:id/codex/usage/reset",
	} {
		require.Truef(t, registered[want], "route %s is not registered", want)
	}
	_ = http.MethodGet
}
