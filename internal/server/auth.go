package server

import (
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
	if n, _ := s.store.CountUsers(); n == 0 {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if currentUser(r) != nil {
		http.Redirect(w, r, safeRedirect(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", map[string]any{"Title": "Sign in", "Next": r.URL.Query().Get("next")})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username, password, next := formStr(r, "username"), r.PostFormValue("password"), r.PostFormValue("next")
	ip := s.clientIP(r)
	fail := func(msg string) {
		s.render(w, r, http.StatusUnauthorized, "login", map[string]any{
			"Title": "Sign in", "Next": next, "Username": username, "Error": msg,
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
	if err := s.startSession(w, r, user.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, safeRedirect(next), "Welcome back, "+user.Name()+".")
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	// Drop any existing session so a fresh token is issued on login.
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(c.Value)
	}
	sess, err := s.store.CreateSession(userID, s.cfg.SessionTTL)
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
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	s.redirect(w, r, "/", "You have been signed out.")
}

// First-run setup ----------------------------------------------------------

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	if n, _ := s.store.CountUsers(); n > 0 {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "setup", map[string]any{"Title": "Welcome", "SiteNameValue": s.SiteName()})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
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
	if err := s.startSession(w, r, id); err != nil {
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

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "account", map[string]any{"Title": "My account"})
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if _, ok := s.store.Authenticate(u.Username, r.PostFormValue("current_password")); !ok {
		s.render(w, r, http.StatusUnprocessableEntity, "account", map[string]any{"Title": "My account", "Errors": []string{"Current password is incorrect."}})
		return
	}
	if errs := validatePassword(r.PostFormValue("password"), r.PostFormValue("password_confirm")); len(errs) > 0 {
		s.render(w, r, http.StatusUnprocessableEntity, "account", map[string]any{"Title": "My account", "Errors": errs})
		return
	}
	if err := s.store.SetPassword(u.ID, r.PostFormValue("password")); err != nil {
		s.serverError(w, r, err)
		return
	}
	// SetPassword ends all sessions; start a fresh one for this device.
	if err := s.startSession(w, r, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/account", "Password updated. Other devices have been signed out.")
}

// User admin -------------------------------------------------------------

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "users/index", map[string]any{"Title": "Users", "Users": users})
}

func (s *Server) handleUserNew(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "users/form", map[string]any{"Title": "Add user", "Form": store.User{}})
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
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
	u, err := s.store.GetUser(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "users/form", map[string]any{"Title": "Edit " + u.Username, "Form": *u})
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.GetUser(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	wasAdmin := u.IsAdmin
	u.DisplayName, u.IsAdmin = formStr(r, "display_name"), r.PostFormValue("is_admin") == "1"
	var errs []string
	if wasAdmin && !u.IsAdmin {
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
			s.startSession(w, r, u.ID)
		}
	}
	s.redirect(w, r, "/admin/users", "User "+u.Username+" updated.")
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
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
