package server

// The gate for the per-person read API, GET /api/v1/me/…, called by the
// church MCP server with a User Management delegated token
// ("Authorization: Bearer umd_…"). The contract is APP_CONTRACTS.md in
// ../mcp_server (section 2). The endpoints themselves are in api_me.go.
//
// What this does, in order:
//   - the API is off (plain-text 404, which the MCP server reads as "not
//     switched on") unless the User Management connection is set up;
//   - Cache-Control: private, no-store; no cookies, sessions or CSRF; no
//     CORS headers (callers are servers, not browsers);
//   - the bearer token is checked with User Management through
//     internal/umtoken (good answers cached up to 60 s by token hash);
//     a refused token is 401 {"error":"invalid_token"}, User Management
//     down is 503;
//   - only tokens delegated through a known app (meAcceptedVia) pass;
//   - the local user is found or made by sso_subject with the role from
//     the token, exactly as single sign-on does, and handed to the handler.
//
// REUSE IN ANOTHER GO APP: copy this file as is and internal/umtoken
// verbatim (fix the import paths), then provide in package server:
//
//  1. a Server field `meTokens *umtoken.Verifier`, set to umtoken.New()
//     in New();
//  2. func (s *Server) meUMConfig() (umtoken.Config, bool): the User
//     Management connection's server-to-server URL, this app's client ID
//     and secret; false when it isn't connected (the /me API is then off);
//  3. func (s *Server) meLocalUser(u umtoken.User) (*store.User, error):
//     find or create the local user by u.Sub and set username, name and
//     role (mapped from u.Role) the same way SSO sign-in does;
//  4. func (u *store.User) and its Active field (false = 403), apiError(w,
//     status, msg) writing {"error":msg} as JSON, and s.log (*slog.Logger);
//  5. /api/ paths served without the session and CSRF middleware;
//  6. in the route setup: s.meRoute(mux, "GET /api/v1/me/…", handler) for
//     each endpoint, then s.meCatchAll(mux) once, so an unknown /me path is
//     a plain 404 rather than the JSON "no such endpoint". Patterns must
//     name GET: a method-less /api/v1/me/ pattern would clash with an
//     "OPTIONS /api/" one in http.ServeMux.
//
// Handlers get the user with meUser(r) and must apply exactly the same
// visibility as the app's web pages for that user.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
	"github.com/tylergoza/production-planner/internal/umtoken"
)

// meAcceptedVia are the apps whose delegated tokens are taken. User
// Management already refuses to delegate to an app that doesn't accept
// the delegate; this is a second, local check.
var meAcceptedVia = map[string]bool{"mcp": true, "website": true}

// meCheckTimeout caps how long a request waits on User Management.
const meCheckTimeout = 5 * time.Second

type meCtxKey struct{}

// meUser is the person the /me request acts for.
func meUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(meCtxKey{}).(*store.User)
	return u
}

// meRoute registers one /me endpoint behind the gate.
func (s *Server) meRoute(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	mux.Handle(pattern, s.meGate(h))
}

// meCatchAll answers unknown GET /api/v1/me/… paths with a plain 404 (no
// JSON body): "that endpoint isn't built", as the contract asks.
func (s *Server) meCatchAll(mux *http.ServeMux) {
	plain := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		http.NotFound(w, r)
	}
	mux.HandleFunc("GET /api/v1/me/", plain)
	mux.HandleFunc("OPTIONS /api/v1/me/", plain)
}

func (s *Server) meGate(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		cfg, on := s.meUMConfig()
		if !on {
			http.NotFound(w, r) // plain: the API is off
			return
		}
		refuse := func() {
			w.Header().Set("WWW-Authenticate", `Bearer realm="me", error="invalid_token"`)
			apiError(w, http.StatusUnauthorized, "invalid_token")
		}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		raw = strings.TrimSpace(raw)
		if !ok || !strings.HasPrefix(raw, "umd_") {
			refuse()
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), meCheckTimeout)
		defer cancel()
		ans, err := s.meTokens.Check(ctx, cfg, raw)
		switch {
		case errors.Is(err, umtoken.ErrRefused):
			refuse()
			return
		case err != nil:
			s.log.Warn("me api: token check failed", "err", err)
			w.Header().Set("Retry-After", "5")
			apiError(w, http.StatusServiceUnavailable, "User Management can't be reached right now")
			return
		}
		if !meAcceptedVia[ans.Via] {
			apiError(w, http.StatusForbidden, "this app doesn't take tokens delegated to "+ans.Via)
			return
		}
		u, err := s.meLocalUser(ans.User)
		if err != nil {
			s.log.Error("me api: local user", "err", err)
			apiError(w, http.StatusInternalServerError, "something went wrong")
			return
		}
		if u == nil || !u.Active {
			apiError(w, http.StatusForbidden, "your access to this app is turned off")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), meCtxKey{}, u)))
	})
}
