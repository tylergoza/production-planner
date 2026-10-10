package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tylergoza/production-planner/internal/sso"
	"github.com/tylergoza/production-planner/internal/store"
)

// Single sign-on through User Management ------------------------------------
//
// With SSO_URL set, /login sends people to User Management and they come
// back to /auth/callback with a code, which is swapped for a grant. The
// grant is kept in the (server-side) session row and checked again at most
// every grantCheckEvery, so turning someone off there signs them out here.

const (
	ssoCookie = "pp_sso"
	// ssoCookieTTL is how long someone has to finish signing in.
	ssoCookieTTL = 10 * time.Minute
	// grantCheckEvery is how stale a session's last grant check may get.
	grantCheckEvery = 5 * time.Minute
	// grantGrace is how long a session keeps working on its last good
	// check while User Management can't be reached.
	grantGrace = time.Hour
	// grantCheckTimeout caps how long a page waits on User Management.
	grantCheckTimeout = 3 * time.Second

	// localLoginSetting is the break-glass switch (the local-login
	// command): "on" brings the password form back even with SSO_URL set.
	localLoginSetting = "local_login"
)

// ssoSignIn reports whether /login goes to User Management: SSO is set up
// and the break-glass local login isn't turned on.
func (s *Server) ssoSignIn() bool {
	return s.sso.Enabled() && s.store.Setting(localLoginSetting, "") != "on"
}

// baseURL is this app's public address, for the redirect URI registered in
// User Management: BASE_URL when set, otherwise worked out from the request.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if s.cfg.TrustProxy {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host, _, _ = strings.Cut(h, ",")
			host = strings.TrimSpace(host)
		}
	}
	return scheme + "://" + host
}

func (s *Server) redirectURI(r *http.Request) string { return s.baseURL(r) + "/auth/callback" }

// handleSSOStart sends the browser to User Management to sign in. State,
// PKCE verifier and where to go afterwards wait in a short-lived cookie.
// It's SameSite=Lax so it comes back with the top-level redirect to
// /auth/callback.
func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	if !s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	next := safeRedirect(r.URL.Query().Get("next"))
	if currentUser(r) != nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	// Live refreshes get a 401 from requireUser and shouldn't land here,
	// but if one does: a background fetch can't follow a redirect to User
	// Management, and starting a sign-in would replace the pp_sso cookie
	// of one under way in another tab.
	if r.Header.Get("X-Live-Refresh") != "" {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	auth, err := s.sso.Authorize(s.redirectURI(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: ssoCookie, Value: auth.State + "." + auth.Verifier + "." + base64.RawURLEncoding.EncodeToString([]byte(next)),
		Path: "/auth/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r),
		MaxAge: int(ssoCookieTTL.Seconds()),
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, auth.URL, http.StatusSeeOther)
}

// readSSOCookie splits the pp_sso cookie into its parts.
func readSSOCookie(r *http.Request) (state, verifier, next string, ok bool) {
	c, err := r.Cookie(ssoCookie)
	if err != nil {
		return "", "", "", false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", "", false
	}
	return parts[0], parts[1], safeRedirect(string(b)), true
}

// handleSSOCallback is where User Management sends people back with a code.
func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	if !s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// The cookie is single use, whatever happens next.
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Path: "/auth/", MaxAge: -1})
	fail := func(status int, title, msg string) {
		s.render(w, r, status, "signin_problem", map[string]any{"Title": title, "Message": msg})
	}

	q := r.URL.Query()
	state, verifier, next, ok := readSSOCookie(r)
	if !ok || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
		s.log.Warn("sso callback: state missing or doesn't match", "ip", s.clientIP(r), "cookie", ok)
		fail(http.StatusBadRequest, "Sign-in expired",
			"That sign-in took too long or was started in another tab. Please try again.")
		return
	}
	if e := q.Get("error"); e != "" {
		s.log.Warn("sso callback: error from User Management", "error", e, "description", q.Get("error_description"))
		msg := "User Management couldn't sign you in to this app. Please try again, or ask a user admin for help."
		if e == "access_denied" {
			msg = "Your account doesn't have access to this app. Ask a user admin if you need it."
		}
		fail(http.StatusBadRequest, "Couldn't sign in", msg)
		return
	}
	code := q.Get("code")
	if code == "" {
		fail(http.StatusBadRequest, "Couldn't sign in", "The sign-in link was incomplete. Please try again.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	grant, u, err := s.sso.Exchange(ctx, code, s.redirectURI(r), verifier)
	if err != nil {
		s.log.Warn("sso callback: code exchange failed", "err", err)
		var ae *sso.APIError
		switch {
		case errors.As(err, &ae):
			fail(http.StatusBadRequest, "Couldn't sign in", ae.Message())
		case errors.Is(err, sso.ErrUnreachable):
			fail(http.StatusBadGateway, "Couldn't sign in",
				"User Management can't be reached right now. Please try again in a few minutes.")
		default:
			fail(http.StatusBadGateway, "Couldn't sign in", "Something went wrong signing you in. Please try again.")
		}
		return
	}
	if grant == "" || u.Sub == "" {
		s.log.Error("sso callback: token response without grant or subject")
		fail(http.StatusBadGateway, "Couldn't sign in", "Something went wrong signing you in. Please try again.")
		return
	}
	user, err := s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, u.Role == "admin")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, user.ID, grant); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, next, "Welcome back, "+user.Name()+".")
}

// handleSignedOut is where people land after signing out with SSO on. It
// doesn't go straight back to /login, which would send them on to User
// Management.
func (s *Server) handleSignedOut(w http.ResponseWriter, r *http.Request) {
	if currentUser(r) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "signed_out", map[string]any{"Title": "Signed out"})
}

// Grant checks ------------------------------------------------------------

// grantResult is the outcome of checking a session's grant.
type grantResult struct {
	user *store.User // the refreshed user; nil means signed out
}

// grantChecks makes concurrent requests on the same session share one
// call to User Management instead of each making their own.
type grantChecks struct {
	mu       sync.Mutex
	inFlight map[string]*grantCall
}

type grantCall struct {
	done chan struct{}
	res  grantResult
}

func newGrantChecks() *grantChecks { return &grantChecks{inFlight: map[string]*grantCall{}} }

// do runs fn for token unless a check for it is already running, in which
// case it waits for that one's result.
func (g *grantChecks) do(token string, fn func() grantResult) grantResult {
	g.mu.Lock()
	if c, ok := g.inFlight[token]; ok {
		g.mu.Unlock()
		<-c.done
		return c.res
	}
	c := &grantCall{done: make(chan struct{})}
	g.inFlight[token] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.inFlight, token)
		g.mu.Unlock()
		close(c.done)
	}()
	c.res = fn()
	return c.res
}

// checkGrant re-confirms an SSO session with User Management if it's due,
// and returns the user to carry on as, or nil to treat the request as
// signed out (the session is deleted then).
func (s *Server) checkGrant(sess *store.Session) *store.User {
	if sess.Grant == "" || !s.sso.Enabled() || time.Since(sess.GrantCheckedAt) < grantCheckEvery {
		return &sess.User
	}
	return s.grants.do(sess.Token, func() grantResult {
		// Not the request's context: a client hanging up shouldn't turn
		// into a failed check that the other waiters share.
		ctx, cancel := context.WithTimeout(context.Background(), grantCheckTimeout)
		defer cancel()
		u, err := s.sso.CheckGrant(ctx, sess.Grant)
		switch {
		case err == nil && u.Sub != sess.User.SSOSubject:
			s.log.Error("grant check: grant is for someone else; signing out", "user", sess.User.Username)
		case err == nil:
			fresh, err := s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, u.Role == "admin")
			if err != nil {
				s.log.Error("grant check: update user", "user", sess.User.Username, "err", err)
				return grantResult{user: &sess.User}
			}
			if err := s.store.MarkGrantChecked(sess.Token); err != nil {
				s.log.Error("grant check: mark checked", "err", err)
			}
			return grantResult{user: fresh}
		case errors.Is(err, sso.ErrGrantRevoked):
			s.log.Info("grant check: sign-in ended in User Management; signing out", "user", sess.User.Username)
		default:
			// Down, or an answer we can't use: keep going on the last good
			// check for a while, so restarting the service signs no one out.
			if time.Since(sess.GrantCheckedAt) < grantGrace {
				s.log.Warn("grant check failed; keeping session for now", "user", sess.User.Username,
					"last_ok", sess.GrantCheckedAt, "err", err)
				return grantResult{user: &sess.User}
			}
			s.log.Warn("grant check failed past the grace period; signing out", "user", sess.User.Username,
				"last_ok", sess.GrantCheckedAt, "err", err)
		}
		s.store.DeleteSession(sess.Token)
		return grantResult{}
	}).user
}
