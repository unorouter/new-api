package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/go-fuego/fuego/option"
)

func SetVideoRouter(router *gin.Engine, engine *fuego.Engine) {
	// GET /v1/videos/:task_id/content is registered by SetTaskPluginProtocolRouter,
	// which carries prod's TokenOrUserAuth so the dashboard can still play video.

	// Unauthenticated: the signed token is the capability.
	mediaRouter := router.Group("/v1/media")
	mediaRouter.Use(middleware.RouteTag("relay"))
	mediaRouter.GET("/:token", controller.MediaLinkContent)
	mediaRouter.HEAD("/:token", controller.MediaLinkContent)

	// Task-plugin endpoint: plugin-backed channels answer /video/generations through
	// the plugin dispatcher rather than a built-in adaptor.
	videoSharedRouter := router.Group("/v1")
	videoSharedRouter.Use(middleware.RouteTag("relay"))
	videoSharedRouter.Use(middleware.TokenAuth())
	videoSharedRouter.Use(middleware.SystemPerformanceCheck())
	videoSubmit := dto.NewRouter(engine, videoSharedRouter.Group("",
		// Limited on every request, not only plugin-claimed ones: free image models are
		// unclaimed and ran unlimited here while /v1/images held them to the free tier.
		middleware.ModelRequestRateLimit(),
		middleware.PinTaskPluginEndpoint(),
		middleware.PrepareTaskPluginEndpoint(),
		middleware.Distribute(),
	), "Video", secToken())
	videoSubmit.GinPost("/video/generations", func(c *gin.Context) {
		controller.RelayTaskPluginEndpoint(c, controller.RelayTask)
	}, dto.GinResp[dto.TaskResponseDoc]())

	videoV1Router := router.Group("/v1")
	videoV1Router.Use(middleware.RouteTag("relay"))
	videoV1Router.Use(middleware.TokenAuth(), middleware.Distribute())
	video := dto.NewRouter(engine, videoV1Router, "Video", secToken())
	{
		// POST /video/generations is registered on videoSharedRouter above (the
		// task-plugin dispatcher falls back to RelayTask), so it must not be
		// registered here too: gin panics on a duplicate method+path.
		// Pinned: the derived id follows whichever route claims the handler name first, and the BFF client is generated from it.
		video.GinGet("/video/generations/:task_id", controller.RelayTaskFetch, dto.GinResp[dto.TaskResponseDoc](), option.OperationID("getV1VideoGenerationsTaskId"))
		video.GinPost("/videos/:video_id/remix", controller.RelayTask, dto.GinResp[dto.TaskResponseDoc](), option.OperationID("postV1VideosVideoIdRemix"))
	}
	// openai compatible API video routes
	// docs: https://platform.openai.com/docs/api-reference/videos/create
	{
		// POST /v1/videos and GET /v1/videos/:task_id are registered by
		// SetTaskPluginProtocolRouter (openai_video create/retrieve).
	}
}
