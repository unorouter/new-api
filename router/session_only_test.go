package router

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A route that changes what can log into an account must require an interactive
// session, because a personal access token is a bearer secret with no second
// factor: letting one reset a password, mint its own replacement or detach an
// identity means the weakest credential can disable the strongest. That is the
// move that took root on 2026-08-26.
//
// The protection is a property of which ROUTER a handler is registered on, so it
// disappears silently when someone adds a route to the wrong group -- nothing
// fails to compile and no test notices. It has already happened once in this
// repo: an upstream sync dropped a per-route permission wrapper and every
// channel endpoint fell back to a flat admin check (see
// channel_permissions_test.go). An audit of the takeover response found six
// routes in exactly this state.
//
// So this reads the registrations rather than trusting review: it derives which
// routers carry SessionOnly, then asserts every credential-changing handler is
// on one of them.
var credentialChangingHandlers = []string{
	"controller.CreateUser",
	"controller.UpdateUser",
	"controller.DeleteUser",
	"controller.AdminResetPasskey",
	"controller.AdminDisable2FA",
	"controller.AdminClearUserBinding",
	"controller.UnbindCustomOAuthByAdmin",
	"controller.GenerateAccessToken",
	"controller.SelfClearBinding",
	"controller.UnbindCustomOAuth",
	"controller.EmailBind",
	"controller.CreateLogCleanupSystemTask",
}

func routerSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("api-router.go")
	require.NoError(t, err, "router source must be readable")
	return string(b)
}

// sessionOnlyRouters returns the dto.Router variable names whose group is built
// with middleware.SessionOnly().
func sessionOnlyRouters(src string) map[string]bool {
	out := map[string]bool{}
	re := regexp.MustCompile(`(?m)^\s*(\w+)\s*:=\s*dto\.NewRouter\([^\n]*SessionOnly\(\)`)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out[m[1]] = true
	}
	return out
}

func TestCredentialChangingRoutesRequireASession(t *testing.T) {
	src := routerSource(t)
	gated := sessionOnlyRouters(src)
	require.NotEmpty(t, gated, "no SessionOnly router found: the parser broke, not the routes")

	for _, handler := range credentialChangingHandlers {
		t.Run(handler, func(t *testing.T) {
			var found bool
			for _, line := range strings.Split(src, "\n") {
				if !strings.Contains(line, handler+")") && !strings.Contains(line, handler+",") {
					continue
				}
				found = true
				// Registered with SessionOnly inline on the route itself.
				if strings.Contains(line, "SessionOnly()") {
					return
				}
				// Registered on a router whose group carries SessionOnly.
				for name := range gated {
					if strings.Contains(line, "("+name+",") {
						return
					}
				}
				assert.Fail(t, "credential-changing route is not behind SessionOnly",
					"%s is registered on an ungated group:\n  %s", handler, strings.TrimSpace(line))
				return
			}
			assert.True(t, found, "%s is no longer registered; drop it from the list or restore the route", handler)
		})
	}
}

// TestManageUserRefusesPATForDestructiveActions guards a rule that cannot be
// expressed as route membership. /user/manage stays on a bot-reachable group
// because the Discord bot needs it, so a PAT is refused inside the handler
// instead, and nothing above would notice if that check were dropped.
//
// The 2026-08-26 intruder drove exactly these actions through this route with a
// stolen admin PAT. The guard that existed then only covered the bot token.
func TestManageUserRefusesPATForDestructiveActions(t *testing.T) {
	b, err := os.ReadFile("../controller/user.go")
	require.NoError(t, err, "controller source must be readable")
	src := string(b)

	require.Contains(t, src, "patDeniedManageActions[req.Action] && middleware.AuthenticatedViaPAT(ginCtx)",
		"ManageUser must refuse a personal access token for account-altering actions")

	for _, action := range []string{"delete", "promote", "demote", "disable"} {
		require.Regexp(t, `(?s)patDeniedManageActions = map\[string\]bool\{[^}]*"`+action+`":`, src,
			"action %q must stay in patDeniedManageActions", action)
	}
}
