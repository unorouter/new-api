package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/dto"

	"github.com/gin-gonic/gin"
	"github.com/go-fuego/fuego"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionRejectsInvalidGeminiSafetyThreshold(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPut, "/api/option/", nil)

	ctx := fuego.NewMockContext[dto.OptionUpdateRequest, any](dto.OptionUpdateRequest{
		Key:   "gemini.safety_settings",
		Value: `{"default":"BLOCK_SOME"}`,
	}, nil)
	ctx.CommonCtx = ginCtx

	payload, err := UpdateOption(ctx)

	require.NoError(t, err)
	assert.False(t, payload.Success)
	assert.Contains(t, payload.Message, "BLOCK_SOME")
}
