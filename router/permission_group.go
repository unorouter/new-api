package router

import (
	"path"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

// permissionGroup declares the access token rule of every route registered on
// a group guarded by RequirePermission, as upstream's handlePermissionRoute does.
type permissionGroup struct {
	*gin.RouterGroup
	permission authz.Permission
}

func newPermissionGroup(parent *gin.RouterGroup, permission authz.Permission, handlers ...gin.HandlerFunc) permissionGroup {
	handlers = append([]gin.HandlerFunc{middleware.RequirePermission(permission)}, handlers...)
	return permissionGroup{RouterGroup: parent.Group("", handlers...), permission: permission}
}

func (g permissionGroup) Handle(method, relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	middleware.DeclareAccessTokenPermissionRoute(method, joinRoutePath(g.BasePath(), relativePath), g.permission)
	return g.RouterGroup.Handle(method, relativePath, handlers...)
}

func (g permissionGroup) GET(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return g.Handle("GET", relativePath, handlers...)
}

func (g permissionGroup) POST(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return g.Handle("POST", relativePath, handlers...)
}

func (g permissionGroup) PUT(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return g.Handle("PUT", relativePath, handlers...)
}

func (g permissionGroup) PATCH(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return g.Handle("PATCH", relativePath, handlers...)
}

func (g permissionGroup) DELETE(relativePath string, handlers ...gin.HandlerFunc) gin.IRoutes {
	return g.Handle("DELETE", relativePath, handlers...)
}

// joinRoutePath mirrors gin's own joining, which keeps a trailing slash.
func joinRoutePath(base, relative string) string {
	if relative == "" {
		return base
	}
	joined := path.Join(base, relative)
	if relative[len(relative)-1] == '/' && joined[len(joined)-1] != '/' {
		return joined + "/"
	}
	return joined
}
