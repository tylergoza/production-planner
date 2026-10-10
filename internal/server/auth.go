package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
)

const minPasswordLen = 10

func randomToken() string       { return store.RandomToken() }
func urlEscape(s string) string { return url.QueryEscape(s) }

// Login ------------------------------------------------------------------

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.ssoSignIn() {
		s.handleSSOStart(w, r)
		return
	}
	if n, _ := s.store.CountUsers(); n == 0 && !s.sso.Enabled() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if currentUser(r) != nil {
		http.Redirect(w, r, safeRedirect(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", map[string]any{"Title": "Sign in", "Next": r.URL.Query().Get("next"), "SSO": s.sso.Enabled()})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username, password, next := formStr(r, "username"), r.PostFormValue("password"), r.PostFormValue("next")
	ip := s.clientIP(r)
	if s.ssoSignIn() {
		http.Redirect(w, r, "/login?next="+urlEscape(safeRedirect(next)), http.StatusSeeOther)
		return
	}
	fail := func(msg string) {
		s.render(w, r, http.StatusUnauthorized, "login", map[string]any{
			"Title": "Sign in", "Next": next, "Username": username, "Error": msg, "SSO": s.sso.Enabled(),
		})
	}
	if !s.limiter.allow(ip) {
		fail("Too many sign-in attempts. Please wait a few minutes and try again.")
		return
	}
	user, ok := s.store.Authenticate(username, password)
	if !ok {
		s.limiter.fail(ip)
		fail("Incorrect username or password.")
		return
	}
	s.limiter.reset(ip)
	if err := s.startSession(w, r, user.ID, ""); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, safeRedirect(next), "Welcome back, "+user.Name()+".")
}

// startSession signs the browser in. grant is the SSO grant behind it, or
// "" for a local password login.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64, grant string) error {
	// Drop any existing session so a fresh token is issued on login.
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(c.Value)
	}
	var sess *store.Session
	var err error
	if grant != "" {
		sess, err = s.store.CreateSSOSession(userID, grant, s.cfg.SessionTTL)
	} else {
		sess, err = s.store.CreateSession(userID, s.cfg.SessionTTL)
	}
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: sess.Token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r), MaxAge: int(s.cfg.SessionTTL.Seconds()),
	})
	return nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Signing out of an SSO session signs out of User Management too
	// (server to server, so there's no confirm page or logout CSRF there),
	// and so out of the other apps on their next grant check.
	if sess, _ := r.Context().Value(ctxSession).(*store.Session); sess != nil && sess.Grant != "" && s.sso.Enabled() {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if err := s.sso.Logout(ctx, sess.Grant); err != nil {
			s.log.Warn("sign out of User Management failed", "user", sess.User.Username, "err", err)
		}
		cancel()
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	if s.ssoSignIn() {
		// Not /login: that would go straight back to User Management.
		http.Redirect(w, r, "/signed-out", http.StatusSeeOther)
		return
	}
	s.redirect(w, r, "/", "You have been signed out.")
}

// First-run setup ----------------------------------------------------------

// First-run setup is for the local login only: with SSO, people (and the
// first admin) come from User Management.

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	if n, _ := s.store.CountUsers(); n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "setup", map[string]any{"Title": "Welcome", "SiteNameValue": s.SiteName()})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	if n, _ := s.store.CountUsers(); n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	username, display, siteName := formStr(r, "username"), formStr(r, "display_name"), formStr(r, "site_name")
	password := r.PostFormValue("password")
	errs := validateNewUser(username, password, r.PostFormValue("password_confirm"))
	if len(errs) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "setup", map[string]any{
			"Title": "Welcome", "Errors": errs, "Username": username, "DisplayName": display, "SiteNameValue": siteName,
		})
		return
	}
	if siteName != "" {
		s.store.SetSetting("site_name", siteName)
		s.reloadSettings()
	}
	id, err := s.store.CreateUser(username, display, password, true)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.startSession(w, r, id, ""); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/productions/new", "Your administrator account is ready. Start by adding a production.")
}

func validateNewUser(username, password, confirm string) []string {
	var errs []string
	if len(username) < 2 || strings.ContainsAny(username, " \t") {
		errs = append(errs, "Username must be at least 2 characters with no spaces.")
	}
	errs = append(errs, validatePassword(password, confirm)...)
	return errs
}

func validatePassword(password, confirm string) []string {
	var errs []string
	if len(password) < minPasswordLen {
		errs = append(errs, "Password must be at least 10 characters.")
	}
	if password != confirm {
		errs = append(errs, "Passwords do not match.")
	}
	return errs
}

// Account ----------------------------------------------------------------

// accountData is what the account page needs besides errors: with SSO,
// a link to the account page in User Management; the local password form
// only for people who have a password and while it's how they sign in.
func (s *Server) accountData(r *http.Request, errs []string) map[string]any {
	data := map[string]any{
		"Title":        "My account",
		"Errors":       errs,
		"ShowPassword": !s.ssoSignIn() && s.store.HasPassword(currentUser(r).ID),
	}
	if s.sso.Enabled() {
		data["SSOAccountURL"] = s.sso.PublicURL + "/account"
	}
	return data
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "account", s.accountData(r, nil))
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !s.store.HasPassword(u.ID) {
		msg := "Your account has no password in this app."
		if s.sso.Enabled() {
			msg += " Change your password in User Management."
		}
		s.render(w, r, http.StatusUnprocessableEntity, "account", s.accountData(r, []string{msg}))
		return
	}
	if _, ok := s.store.Authenticate(u.Username, r.PostFormValue("current_password")); !ok {
		s.render(w, r, http.StatusUnprocessableEntity, "account", s.accountData(r, []string{"Current password is incorrect."}))
		return
	}
	if errs := validatePassword(r.PostFormValue("password"), r.PostFormValue("password_confirm")); len(errs) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "account", s.accountData(r, errs))
		return
	}
	if err := s.store.SetPassword(u.ID, r.PostFormValue("password")); err != nil {
		s.serverError(w, r, err)
		return
	}
	// SetPassword ends all sessions; start a fresh one for this device.
	if err := s.startSession(w, r, u.ID, ""); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/account", "Password updated. Other devices have been signed out.")
}

// User admin -------------------------------------------------------------

// With SSO on, /admin/users is the app-admin Users page (sso_users.go) and
// people are added, renamed and given passwords in User Management, so the
// local add/edit/delete pages are gone.

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.handleSSOUsers(w, r)
		return
	}
	users, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "users/index", map[string]any{"Title": "Users", "Users": users})
}

func (s *Server) handleUserNew(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, "users/form", map[string]any{"Title": "Add user", "Form": store.User{}})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	u := store.User{Username: formStr(r, "username"), DisplayName: formStr(r, "display_name"), IsAdmin: r.PostFormValue("is_admin") == "1"}
	errs := validateNewUser(u.Username, r.PostFormValue("password"), r.PostFormValue("password_confirm"))
	if len(errs) == 0 {
		if _, err := s.store.CreateUser(u.Username, u.DisplayName, r.PostFormValue("password"), u.IsAdmin); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				errs = append(errs, "That username is already taken.")
			} else {
				s.serverError(w, r, err)
				return
			}
		}
	}
	if len(errs) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "users/form", map[string]any{"Title": "Add user", "Form": u, "Errors": errs})
		return
	}
	s.redirect(w, r, "/admin/users", "User "+u.Username+" added.")
}

func (s *Server) handleUserEdit(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	u, err := s.store.GetUser(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "users/form", map[string]any{"Title": "Edit " + u.Username, "Form": *u})
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	u, err := s.store.GetUser(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	wasAdmin := u.IsAdmin
	u.DisplayName, u.IsAdmin = formStr(r, "display_name"), r.PostFormValue("is_admin") == "1"
	var errs []string
	// CountAdmins counts active admins only, so only demoting an active
	// admin can leave none. (An inactive one, left over from SSO, can be
	// demoted freely; the admin doing it is active and still counts.)
	if wasAdmin && !u.IsAdmin && u.Active {
		if n, _ := s.store.CountAdmins(); n <= 1 {
			errs = append(errs, "At least one administrator is required.")
		}
	}
	password := r.PostFormValue("password")
	if password != "" {
		errs = append(errs, validatePassword(password, r.PostFormValue("password_confirm"))...)
	}
	if len(errs) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "users/form", map[string]any{"Title": "Edit " + u.Username, "Form": *u, "Errors": errs})
		return
	}
	if err := s.store.UpdateUser(u.ID, u.DisplayName, u.IsAdmin); err != nil {
		s.serverError(w, r, err)
		return
	}
	if password != "" {
		if err := s.store.SetPassword(u.ID, password); err != nil {
			s.serverError(w, r, err)
			return
		}
		if u.ID == currentUser(r).ID {
			grant := ""
			if sess, _ := r.Context().Value(ctxSession).(*store.Session); sess != nil {
				grant = sess.Grant
			}
			s.startSession(w, r, u.ID, grant)
		}
	}
	s.redirect(w, r, "/admin/users", "User "+u.Username+" updated.")
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if s.sso.Enabled() {
		s.notFound(w, r)
		return
	}
	id := pathID(r)
	if id == currentUser(r).ID {
		s.setFlash(w, r, "error", "You can't delete your own account.")
		http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
		return
	}
	if err := s.store.DeleteUser(id); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/users", "User deleted.")
}

// Login rate limiting ------------------------------------------------------

type loginLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, hits: map[string][]time.Time{}}
}

func (l *loginLimiter) prune(key string, now time.Time) []time.Time {
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = kept
	return kept
}

func (l *loginLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(key, time.Now())) < l.max
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.hits[key] = append(l.prune(key, now), now)
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
