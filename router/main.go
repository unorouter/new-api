package router

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

func SetRouter(router *gin.Engine, assets WebAssets) {
	var engine *fuego.Engine
	if os.Getenv("ENABLE_OPENAPI") == "true" {
		engine = newOpenAPIEngine()
	}

	SetApiRouter(router, engine)
	SetDashboardRouter(router, engine)
	SetRelayRouter(router, engine)
	SetTaskPluginProtocolRouter(router)
	SetVideoRouter(router, engine)
	SetTaskRouter(router)
	pluginDispatcher := SetPluginRouter(router)
	SetOAuthServerRouter(router, engine)
	SetNotifyRouter(router, engine)
	registerOpenAPIRoutes(engine, router)

	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if common.IsMasterNode && frontendBaseUrl != "" {
		frontendBaseUrl = ""
		common.SysLog("FRONTEND_BASE_URL is ignored on master node")
	}
	if frontendBaseUrl == "" {
		SetWebRouter(router, assets, pluginDispatcher)
	} else {
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(
			pluginDispatcher,
			middleware.RouteTag("web"),
			func(c *gin.Context) {
				c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
			},
		)
	}
}
