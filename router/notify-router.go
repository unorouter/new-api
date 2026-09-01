package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/notify"
	relaydto "github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/go-fuego/fuego/option"
)

// SetNotifyRouter mounts the public notification surface: the WS endpoint at
// /ws and the anonymous web push subscription API under /api/notify.
func SetNotifyRouter(router *gin.Engine, engine *fuego.Engine) {
	router.GET("/ws", middleware.RouteTag("api"), notify.HandleWebSocket)

	group := router.Group("/api/notify")
	group.Use(middleware.RouteTag("api"))
	group.Use(middleware.CORS())
	group.Use(middleware.GlobalAPIRateLimit())
	{
		notifyRouter := dto.NewRouter(engine, group, "Notify", secPublic())
		notifyRouter.GinGet("/vapid", controller.GetNotifyVapidKey, dto.GinResp[relaydto.NotifyVapidKeyResponse]())
		notifyRouter.GinGet("/events", controller.GetNotifyEvents,
			option.Query("since", "Only return events newer than this unix timestamp"),
			option.Query("topics", "Comma-separated topic filter"),
			dto.GinResp[relaydto.NotifyEventsResponse]())
		notifyRouter.GinGet("/room", controller.GetRoomHistory,
			option.Query("room", "Room id from the invite link"))

		critical := dto.NewRouter(engine, group.Group("", middleware.CriticalRateLimit()), "Notify", secPublic())
		critical.GinPost("/subscription", controller.SubscribeNotifyPush,
			dto.GinBody[relaydto.NotifySubscriptionRequest](),
			dto.GinResp[relaydto.NotifySubscriptionResponse]())
		critical.GinDelete("/subscription", controller.UnsubscribeNotifyPush,
			dto.GinBody[relaydto.NotifyUnsubscribeRequest](),
			dto.GinResp[relaydto.NotifyUnsubscribeResponse]())
	}
}
