package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

// registerAuthzRoutes mounts the authorization API under its own /authz
// namespace. GET /authz/catalog returns the permission schema (resources,
// actions, and role baselines) used by the client permission editor.
func registerAuthzRoutes(apiRouter *gin.RouterGroup) {
	authzRoute := apiRouter.Group("/authz")
	{
		// Read-only permission schema. Mods load the Users page (which mounts the
		// user drawer and fetches the catalog), so gate the read with ModAuth;
		// any future authz WRITE routes must use AdminAuth explicitly.
		authzRoute.GET("/catalog", middleware.ModAuth(), controller.GetPermissionCatalog)
	}
}
