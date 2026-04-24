package service

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/gorilla/sessions"
)

// Authentication + consent flow for the zitadel/oidc provider.
//
// zitadel's contract:
//   1. /oauth/v1/authorize parses + validates the request, writes an
//      OAuthAuthRequest row, then redirects the browser to
//      Client.LoginURL(authReqID).
//   2. The login UI (our Next.js consent page) drives whatever interaction we
//      want - login if no session, then consent screen.
//   3. The consent UI POSTs `consented=true` to a server route in *this* process
//      (FinalizeAuthRequest). We mark the request done, then redirect the
//      browser to /oauth/v1/authorize/callback?id=<authReqID>.
//   4. zitadel's AuthorizeCallback handler reads the now-Done request, issues
//      the authorization code, and 302s the agent to the client's redirect_uri.
//
// readUserIDFromCookie is the signed-cookie reader shared with the dashboard
// session middleware. session_secret is shared with the dashboard cookie auth.

const sessionCookieName = "session"

// FinalizeAuthRequest marks an OAuthAuthRequest as approved so that a
// subsequent GET /oauth/v1/authorize/callback?id=... emits an auth code.
//
// Caller is responsible for verifying that the user behind `subject` actually
// owns the request and accepted the consent.
func FinalizeAuthRequest(ctx context.Context, storage *OAuthStorage, authReqID string, subject string, scopes []string) error {
	return storage.MarkAuthRequestDone(ctx, authReqID, subject, time.Now().UTC(), scopes)
}

// readUserIDFromCookie opens the same signed cookie store new-api uses for
// dashboard sessions (see main.go `sessions.Sessions("session", store)`).
// Returns the `id` session value as int, or 0 when no valid session.
func readUserIDFromCookie(r *http.Request) (int, error) {
	store := sessions.NewCookieStore([]byte(common.SessionSecret))
	sess, err := store.Get(r, sessionCookieName)
	if err != nil {
		return 0, err
	}
	raw, ok := sess.Values["id"]
	if !ok {
		return 0, nil
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	}
	return 0, nil
}

// ReadUserIDStringFromCookie is the string-typed convenience used by handlers
// that need the subject for the auth request (zitadel uses string subjects).
func ReadUserIDStringFromCookie(r *http.Request) (string, error) {
	id, err := readUserIDFromCookie(r)
	if err != nil || id == 0 {
		return "", err
	}
	return strconv.Itoa(id), nil
}
