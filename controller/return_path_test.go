package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func ctxWithReturnBase(base string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", nil)
	if base != "" {
		c.Request.Header.Set(ReturnBaseHeader, base)
	}
	return c
}

func TestPaymentReturnPathUsesDefaultDashboardRoutes(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://dashboard.example.com/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	assert.Equal(
		t,
		"https://dashboard.example.com/wallet?pay=success",
		paymentReturnPath(nil, "/wallet?pay=success"),
	)
	assert.Equal(
		t,
		"https://dashboard.example.com/usage-logs",
		paymentReturnPath(nil, "/usage-logs"),
	)
}

// With a separate frontend, a paid user must land back on that site. Returning
// a console path would drop them on this service's own dashboard, which is a
// different origin they never signed into and where the route may not exist.
func TestPaymentReturnPathUsesFrontendRoutes(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	previousFrontend := system_setting.FrontendAddress
	system_setting.ServerAddress = "https://api.example.com"
	system_setting.FrontendAddress = "https://example.com/"
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		system_setting.FrontendAddress = previousFrontend
	})

	for _, tc := range []struct{ suffix, want string }{
		{"/console/log", "https://example.com/logs"},
		{"/usage-logs", "https://example.com/logs"},
		{"/console/subscription", "https://example.com/billing"},
		{"/console/topup", "https://example.com/billing"},
		{"/wallet?pay=success", "https://example.com/billing"},
		{"/wallet?show_history=true", "https://example.com/billing"},
	} {
		assert.Equal(t, tc.want, paymentReturnPath(nil, tc.suffix), tc.suffix)
	}
}

// A payment started from the console must return to the console, not to the
// configured frontend: the user was never on that site.
func TestPaymentReturnPathHonorsCallerOrigin(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	previousFrontend := system_setting.FrontendAddress
	system_setting.ServerAddress = "https://api.example.com"
	system_setting.FrontendAddress = "https://example.com"
	t.Cleanup(func() {
		system_setting.ServerAddress = previousAddress
		system_setting.FrontendAddress = previousFrontend
	})

	assert.Equal(t, "https://api.example.com/console/log",
		paymentReturnPath(ctxWithReturnBase("https://api.example.com"), "/console/log"))
	assert.Equal(t, "https://example.com/logs",
		paymentReturnPath(ctxWithReturnBase("https://example.com"), "/console/log"))
	// No header: a direct API caller gets the user-facing default.
	assert.Equal(t, "https://example.com/logs",
		paymentReturnPath(ctxWithReturnBase(""), "/console/log"))
	// An origin we never configured is ignored rather than redirected to.
	assert.Equal(t, "https://example.com/logs",
		paymentReturnPath(ctxWithReturnBase("https://evil.example.net"), "/console/log"))
}
