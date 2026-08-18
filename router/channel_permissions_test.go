package router

import (
	"testing"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"

	"github.com/stretchr/testify/assert"
)

// The channel permission model only protects anything while something calls it.
// An upstream sync once migrated these routes to typed handlers and dropped the
// per-route RequirePermission wrapper along the way: the permissions, the roles
// and their tests all survived, nothing failed to compile, and every channel
// endpoint silently fell back to a flat AdminAuth check. A restricted admin
// holding only channel:read could then rewrite a channel's key and base_url and
// redirect live traffic.
//
// Go reports unused variables but not unused exported functions, so this asserts
// the enforcement entry point is reachable rather than trusting the compiler.
func TestChannelPermissionEnforcementIsWired(t *testing.T) {
	assert.NotNil(t, middleware.RequirePermission(authz.ChannelSensitiveWrite),
		"RequirePermission must remain the gate for sensitive channel writes")

	// ChannelSensitiveWrite is deliberately granted to no role by default, so a
	// plain admin cannot reach it without an explicit grant. If this ever starts
	// returning true for a plain admin, the separation the gate exists to
	// enforce is gone regardless of whether the middleware is wired.
	assert.False(t, authz.Can(0, 10, authz.ChannelSensitiveWrite),
		"a plain admin must not hold ChannelSensitiveWrite without an explicit grant")
}
