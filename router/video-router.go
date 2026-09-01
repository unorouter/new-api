package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

func SetVideoRouter(router *gin.Engine, engine *fuego.Engine) {
	// GET /v1/videos/:task_id/content is registered by SetTaskPluginProtocolRouter,
	// which carries prod's TokenOrUserAuth so the dashboard can still play video.

	// Task-plugin endpoint: plugin-backed channels answer /video/generations through
	// the plugin dispatcher rather than a built-in adaptor.
	videoSharedRouter := router.Group("/v1")
	videoSharedRouter.Use(middleware.RouteTag("relay"))
	videoSharedRouter.Use(middleware.TokenAuth())
	videoSharedRouter.Use(middleware.SystemPerformanceCheck())
	videoSharedRouter.POST(
		"/video/generations",
		middleware.PinTaskPluginEndpoint(),
		middleware.TaskPluginEndpointOnly(middleware.ModelRequestRateLimit()),
		middleware.PrepareTaskPluginEndpoint(),
		middleware.Distribute(),
		func(c *gin.Context) {
			controller.RelayTaskPluginEndpoint(c, controller.RelayTask)
		},
	)

	videoV1Router := router.Group("/v1")
	videoV1Router.Use(middleware.RouteTag("relay"))
	videoV1Router.Use(middleware.TokenAuth(), middleware.Distribute())
	video := dto.NewRouter(engine, videoV1Router, "Video", secToken())
	{
		// POST /video/generations is registered on videoSharedRouter above (the
		// task-plugin dispatcher falls back to RelayTask), so it must not be
		// registered here too: gin panics on a duplicate method+path.
		video.GinGet("/video/generations/:task_id", controller.RelayTaskFetch, dto.GinResp[dto.TaskResponseDoc]())
		video.GinPost("/videos/:video_id/remix", controller.RelayTask, dto.GinResp[dto.TaskResponseDoc]())
	}
	// openai compatible API video routes
	// docs: https://platform.openai.com/docs/api-reference/videos/create
	{
		// POST /v1/videos and GET /v1/videos/:task_id are registered by
		// SetTaskPluginProtocolRouter (openai_video create/retrieve).
	}
}
