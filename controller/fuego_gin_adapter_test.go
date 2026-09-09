package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
)

// The adapters below let upstream's gin-style tests drive prod's fuego handlers
// through a real gin router: path params, the gin context (auth values), and
// the request all flow into the mock fuego context, and the typed result is
// written back as the same JSON envelope the fuego runtime produces.

func fuegoBind[B, P any](c *gin.Context, body B, params P) *fuego.MockContext[B, P] {
	ctx := fuego.NewMockContext[B, P](body, params)
	ctx.CommonCtx = c
	ctx.SetRequest(c.Request)
	for _, param := range c.Params {
		ctx.PathParams[param.Key] = param.Value
	}
	return ctx
}

func fuegoWrite[T any](c *gin.Context, resp T, err error) {
	if c.Writer.Written() {
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func fuegoNoBody[T any](h func(c fuego.ContextNoBody) (T, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		resp, err := h(fuegoBind[any, any](c, nil, nil))
		fuegoWrite(c, resp, err)
	}
}

func fuegoWithParams[T, P any](h func(c fuego.ContextWithParams[P]) (T, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var params P
		resp, err := h(fuegoBind[any, P](c, nil, params))
		fuegoWrite(c, resp, err)
	}
}

func fuegoWithBody[T, B any](h func(c fuego.ContextWithBody[B]) (T, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body B
		if c.Request != nil && c.Request.Body != nil && c.Request.ContentLength != 0 {
			if err := common.DecodeJson(c.Request.Body, &body); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
				return
			}
		}
		resp, err := h(fuegoBind[B, any](c, body, nil))
		fuegoWrite(c, resp, err)
	}
}

func fuegoBodyParams[T, B, P any](h func(c fuego.Context[B, P]) (T, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body B
		var params P
		if c.Request != nil && c.Request.Body != nil && c.Request.ContentLength != 0 {
			if err := common.DecodeJson(c.Request.Body, &body); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
				return
			}
		}
		resp, err := h(fuegoBind[B, P](c, body, params))
		fuegoWrite(c, resp, err)
	}
}
