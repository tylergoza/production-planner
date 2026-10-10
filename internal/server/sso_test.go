package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tylergoza/production-planner/internal/sso"
	"github.com/tylergoza/production-planner/internal/store"
)

// fakeUM answers like User Management: /authorize signs in straight away
// as user, /token swaps the code (checking secret, redirect URI and PKCE),
// and the grant and logout APIs do what mode says.
type fakeUM struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	user      sso.User
	codes     map[string]codeInfo // code → what it was issued for
	mode      string              // grant check: "ok", "revoked" or "down"
	delay     time.Duration       // grant check delay
	tokens    int                 // /token calls
	logouts   []string            // grants signed out
	grantHits atomic.Int32

	appUsers sso.AppUsers // GET /api/v1/apps/planner/users
	patches  []patchCall  // PATCH /api/v1/apps/planner/users/{sub}
	patchErr string       // refuse PATCHes with this error code
}

type patchCall struct {
	sub, actingGrant string
	body             map[string]any
}

type codeInfo struct{ redirectURI, challenge string }

const testGrant = "grant-abc"

func newFakeUM(t *testing.T) *fakeUM {
	f := &fakeUM{t: t, codes: map[string]codeInfo{}, mode: "ok",
		user: sso.User{Sub: "42", Username: "alice", Name: "Alice Admin", Role: "admin"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "planner" || q.Get("response_type") != "code" ||
			q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
			http.Error(w, "bad authorize request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.codes["code-1"] = codeInfo{q.Get("redirect_uri"), q.Get("code_challenge")}
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"code": {"code-1"}, "state": {q.Get("state")}}.Encode(), http.StatusFound)
	})
	apiErr := func(w http.ResponseWriter, status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": code})
	}
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if id, secret, ok := r.BasicAuth(); !ok || id != "planner" || secret != "s3cret" {
				apiErr(w, http.StatusUnauthorized, "invalid_client")
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /token", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tokens++
		info, ok := f.codes[r.PostFormValue("code")]
		delete(f.codes, r.PostFormValue("code"))
		if !ok || info.redirectURI != r.PostFormValue("redirect_uri") || sso.Challenge(r.PostFormValue("code_verifier")) != info.challenge {
			apiErr(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"grant": testGrant, "user": f.user})
	}))
	mux.HandleFunc("GET /api/v1/grant", authed(func(w http.ResponseWriter, r *http.Request) {
		f.grantHits.Add(1)
		f.mu.Lock()
		mode, delay, user := f.mode, f.delay, f.user
		f.mu.Unlock()
		time.Sleep(delay)
		switch {
		case mode == "down":
			http.Error(w, "down", http.StatusBadGateway)
		case mode == "revoked" || r.Header.Get("X-Grant") != testGrant:
			apiErr(w, http.StatusUnauthorized, "invalid_grant")
		default:
			json.NewEncoder(w).Encode(map[string]any{"user": user})
		}
	}))
	mux.HandleFunc("POST /api/v1/logout", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.logouts = append(f.logouts, r.Header.Get("X-Grant"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /api/v1/apps/planner/users", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		json.NewEncoder(w).Encode(f.appUsers)
	}))
	mux.HandleFunc("PATCH /api/v1/apps/planner/users/{sub}", authed(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.patches = append(f.patches, patchCall{r.PathValue("sub"), r.Header.Get("X-Acting-Grant"), body})
		if f.patchErr != "" {
			apiErr(w, http.StatusConflict, f.patchErr)
			return
		}
		for i := range f.appUsers.Users {
			u := &f.appUsers.Users[i]
			if u.Sub != r.PathValue("sub") {
				continue
			}
			if role, ok := body["role"].(string); ok {
				u.Role = role
			}
			if sus, ok := body["suspended"].(bool); ok {
				u.Suspended, u.Active = sus, !sus
			}
			u.UpdatedBy = f.user.Name
			json.NewEncoder(w).Encode(u)
			return
		}
		apiErr(w, http.StatusNotFound, "no_access")
	}))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeUM) set(fn func(f *fakeUM)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func ssoConfig(f *fakeUM) Config {
	return Config{SSOURL: f.srv.URL, SSOClientID: "planner", SSOClientSecret: "s3cret"}
}

// noFollow is a client sharing c's cookies that stops at redirects.
func (c *client) noFollow() *http.Client {
	return &http.Client{Jar: c.http.Jar, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func (c *client) getRaw(hc *http.Client, path string) *http.Response {
	c.t.Helper()
	resp, err := hc.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// sessionToken is the browser's pp_session cookie.
func (c *client) sessionToken() string {
	u, _ := url.Parse(c.base)
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == sessionCookie {
			return ck.Value
		}
	}
	return ""
}

// ageGrantCheck pretends the session's grant was last checked ago.
func ageGrantCheck(t *testing.T, st *store.Store, token string, ago time.Duration) {
	t.Helper()
	if _, err := st.DB.Exec(`UPDATE sessions SET grant_checked_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-ago).Format(time.RFC3339), token); err != nil {
		t.Fatal(err)
	}
}

func sessionExists(t *testing.T, st *store.Store, token string) bool {
	t.Helper()
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE token = ?`, token).Scan(&n)
	return n == 1
}

func TestSSOOffKeepsLocalLogin(t *testing.T) {
	c, st := newTestServer(t)
	if _, err := st.CreateUser("alice", "", "a long password", true); err != nil {
		t.Fatal(err)
	}
	body := c.get("/login", 200)
	if !strings.Contains(body, `name="password"`) || strings.Contains(body, "/auth/start") {
		t.Fatal("expected the plain password form")
	}
	c.get("/auth/start", 404)
	c.get("/auth/callback?code=x&state=y", 404)
	c.post("/login", "/login", url.Values{"username": {"alice"}, "password": {"a long password"}}, 200)
	if !strings.Contains(c.get("/account", 200), "Change password") {
		t.Fatal("account page should have the password form")
	}
	// Signing out goes back to the start, not the SSO signed-out page.
	if body := c.post("/", "/logout", url.Values{}, 200); !strings.Contains(body, "You have been signed out.") {
		t.Fatal("expected the old sign-out flash")
	}
}

func TestSSOConfigValidated(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(Config{SSOURL: "https://accounts.example.org", SSOClientID: "planner"}, st, logger); err == nil {
		t.Fatal("SSO_URL without a client secret should be refused")
	}
	if _, err := New(Config{SSOURL: "accounts.example.org", SSOClientID: "a", SSOClientSecret: "b"}, st, logger); err == nil {
		t.Fatal("SSO_URL without a scheme should be refused")
	}
}

func TestSSOLoginRedirect(t *testing.T) {
	f := newFakeUM(t)
	c, _ := newTestServerWith(t, ssoConfig(f))

	resp := c.getRaw(c.noFollow(), "/login?next=/productions")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if !strings.HasPrefix(loc.String(), f.srv.URL+"/authorize?") {
		t.Fatalf("redirected to %s", loc)
	}
	q := loc.Query()
	if q.Get("client_id") != "planner" || q.Get("redirect_uri") != c.base+"/auth/callback" ||
		q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		t.Fatalf("bad authorize params: %v", q)
	}
	var cookie *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == ssoCookie {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/auth/" || cookie.MaxAge <= 0 {
		t.Fatalf("bad pp_sso cookie: %+v", cookie)
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 || parts[0] != q.Get("state") || sso.Challenge(parts[1]) != q.Get("code_challenge") {
		t.Fatalf("cookie doesn't hold the state and verifier: %q", cookie.Value)
	}
	// An off-site next is dropped.
	resp = c.getRaw(c.noFollow(), "/login?next=//evil.example")
	for _, ck := range resp.Cookies() {
		if ck.Name == ssoCookie && !strings.HasSuffix(ck.Value, ".Lw") { // base64url("/")
			t.Fatalf("unsafe next kept: %q", ck.Value)
		}
	}
	// The CSP lets a form post end up at User Management (via /login).
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' "+f.srv.URL) {
		t.Fatalf("CSP form-action doesn't include User Management: %s", csp)
	}
}

func TestSSOCallbackRejectsBadState(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))

	// No sign-in started at all.
	if body := c.get("/auth/callback?code=code-1&state=abc", 400); !strings.Contains(body, "Sign-in expired") {
		t.Fatal("expected the expired page")
	}
	// Started, but the state doesn't match.
	c.getRaw(c.noFollow(), "/login")
	c.get("/auth/callback?code=code-1&state=wrong", 400)
	if f.tokens != 0 {
		t.Fatal("code was exchanged despite a bad state")
	}
	if n, _ := st.CountUsers(); n != 0 {
		t.Fatal("user created despite a bad state")
	}

	// User Management sending an error back gets a friendly page.
	resp := c.getRaw(c.noFollow(), "/login")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	body := c.get("/auth/callback?error=access_denied&state="+url.QueryEscape(loc.Query().Get("state")), 400)
	if !strings.Contains(body, "doesn&#39;t have access to this app") {
		t.Fatalf("expected the no-access message:\n%s", body)
	}
}

func TestSSOCallbackSignsInAndLinksLocalUser(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))
	// The existing local user, from before SSO.
	aliceID, err := st.CreateUser("alice", "", "a long password", false)
	if err != nil {
		t.Fatal(err)
	}

	// The whole round trip: /login → User Management → /auth/callback → next.
	body := c.get("/login?next=/productions", 200)
	if !strings.Contains(body, "Welcome back, Alice Admin.") {
		t.Fatalf("expected to land signed in:\n%s", body)
	}
	u, err := st.GetUserBySubject("42")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != aliceID || !u.IsAdmin || u.DisplayName != "Alice Admin" {
		t.Fatalf("local user not linked/updated: %+v", u)
	}
	sess, err := st.GetSession(c.sessionToken())
	if err != nil || sess.Grant != testGrant || sess.User.ID != aliceID {
		t.Fatalf("bad session: %+v %v", sess, err)
	}
	// The sign-in cookie is gone, and the code can't be replayed.
	u2, _ := url.Parse(c.base + "/auth/")
	for _, ck := range c.http.Jar.Cookies(u2) {
		if ck.Name == ssoCookie {
			t.Fatal("pp_sso cookie left behind")
		}
	}

	// The account page points at User Management, with no password form.
	body = c.get("/account", 200)
	if !strings.Contains(body, f.srv.URL+"/account") || strings.Contains(body, "Change password") {
		t.Fatal("account page should link to User Management")
	}
	// alice still has her old password, but local password changes are off
	// while SSO signs people in; a password-less user is refused outright.
	f.set(func(f *fakeUM) { f.user = sso.User{Sub: "43", Username: "bob", Role: "editor"} })
	bob := &client{t: t, base: c.base, http: &http.Client{Jar: mustJar()}}
	bob.get("/login", 200)
	b, err := st.GetUserBySubject("43")
	if err != nil || b.IsAdmin || st.HasPassword(b.ID) {
		t.Fatalf("new SSO user wrong: %+v %v", b, err)
	}
	body = bob.post("/account", "/account/password", url.Values{
		"current_password": {"x"}, "password": {"another long one"}, "password_confirm": {"another long one"},
	}, 422)
	if !strings.Contains(body, "no password in this app") {
		t.Fatal("expected the no-password refusal")
	}
}

func mustJar() http.CookieJar {
	j, _ := cookiejar.New(nil)
	return j
}

func TestSSOGrantCheck(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))
	c.get("/login", 200)
	token := c.sessionToken()
	nf := c.noFollow()

	// Checked recently: no call.
	c.get("/", 200)
	if n := f.grantHits.Load(); n != 0 {
		t.Fatalf("grant checked %d times within 5 minutes", n)
	}

	// Due, and fine: the user is refreshed from User Management.
	f.set(func(f *fakeUM) { f.user.Name = "Alice Renamed"; f.user.Role = "editor" })
	ageGrantCheck(t, st, token, 6*time.Minute)
	if body := c.get("/", 200); !strings.Contains(body, "Alice Renamed") {
		t.Fatal("user not refreshed")
	}
	u, _ := st.GetUserBySubject("42")
	if u.DisplayName != "Alice Renamed" || u.IsAdmin {
		t.Fatalf("user not updated: %+v", u)
	}
	sess, _ := st.GetSession(token)
	if time.Since(sess.GrantCheckedAt) > time.Minute {
		t.Fatal("grant check time not updated")
	}

	// Down, within the grace period: carry on.
	f.set(func(f *fakeUM) { f.mode = "down" })
	ageGrantCheck(t, st, token, 30*time.Minute)
	if resp := c.getRaw(nf, "/"); resp.StatusCode != 200 {
		t.Fatalf("status %d during grace period, want 200", resp.StatusCode)
	}
	if !sessionExists(t, st, token) {
		t.Fatal("session dropped during grace period")
	}

	// Down past the grace period: signed out.
	ageGrantCheck(t, st, token, 2*time.Hour)
	if resp := c.getRaw(nf, "/"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("past grace: status %d to %q, want redirect to /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	if sessionExists(t, st, token) {
		t.Fatal("session kept past the grace period")
	}

	// Revoked: signed out.
	f.set(func(f *fakeUM) { f.mode = "ok" })
	c.get("/login", 200)
	token = c.sessionToken()
	f.set(func(f *fakeUM) { f.mode = "revoked" })
	ageGrantCheck(t, st, token, 6*time.Minute)
	if resp := c.getRaw(nf, "/productions"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?next=%2Fproductions" {
		t.Fatalf("revoked: status %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if sessionExists(t, st, token) {
		t.Fatal("session kept after revocation")
	}
}

func TestSSOGrantCheckSharedBetweenConcurrentRequests(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))
	c.get("/login", 200)
	f.set(func(f *fakeUM) { f.delay = 200 * time.Millisecond })
	ageGrantCheck(t, st, c.sessionToken(), 6*time.Minute)
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.http.Get(c.base + "/")
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if n := f.grantHits.Load(); n != 1 {
		t.Fatalf("%d grant checks for 5 concurrent requests, want 1", n)
	}
}

// The live update stream never waits on User Management, and once a
// sign-in has ended the page's background refresh stops quietly instead of
// starting a new sign-in.
func TestSSOLiveEvents(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))
	c.get("/login", 200)
	token := c.sessionToken()
	f.set(func(f *fakeUM) { f.mode = "revoked"; f.delay = 2 * time.Second })
	ageGrantCheck(t, st, token, 6*time.Minute)

	start := time.Now()
	resp, err := c.http.Get(c.base + "/live")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("live: status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("opening the stream took %s", d)
	}
	resp.Body.Close()
	if n := f.grantHits.Load(); n != 0 {
		t.Fatalf("opening the stream checked the grant %d times", n)
	}
	if !sessionExists(t, st, token) {
		t.Fatal("opening the stream touched the session")
	}

	// The refresh it sets off finds the sign-in has ended: 401, no new
	// sign-in started (no pp_sso cookie), session gone.
	f.set(func(f *fakeUM) { f.delay = 0 })
	req, _ := http.NewRequest("GET", c.base+"/productions", nil)
	req.Header.Set("X-Live-Refresh", "1")
	resp, err = c.noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("live refresh after revocation: status %d to %q, want 401", resp.StatusCode, resp.Header.Get("Location"))
	}
	u, _ := url.Parse(c.base + "/auth/")
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == ssoCookie {
			t.Fatal("a background refresh started a sign-in")
		}
	}
	if sessionExists(t, st, token) {
		t.Fatal("session kept after revocation")
	}

	// Signed out now, the stream says 401 rather than redirecting.
	if resp := c.getRaw(c.noFollow(), "/live"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("live when signed out: status %d, want 401", resp.StatusCode)
	}
	// And a stray live refresh reaching /auth/start starts no sign-in.
	req, _ = http.NewRequest("GET", c.base+"/auth/start", nil)
	req.Header.Set("X-Live-Refresh", "1")
	resp, err = c.noFollow().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("live refresh at /auth/start: status %d, want 401", resp.StatusCode)
	}
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == ssoCookie {
			t.Fatal("/auth/start started a sign-in for a live refresh")
		}
	}
}

func TestSSOLogout(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))
	c.get("/login", 200)
	token := c.sessionToken()
	body := c.post("/", "/logout", url.Values{}, 200)
	if !strings.Contains(body, "Sign in again") {
		t.Fatalf("expected the signed-out page:\n%s", body)
	}
	if len(f.logouts) != 1 || f.logouts[0] != testGrant {
		t.Fatalf("User Management logout calls: %v", f.logouts)
	}
	if sessionExists(t, st, token) {
		t.Fatal("local session not deleted")
	}

	// Still signs out here when User Management is unreachable.
	c.get("/login", 200)
	token = c.sessionToken()
	f.srv.Close()
	c.post("/", "/logout", url.Values{}, 200)
	if sessionExists(t, st, token) {
		t.Fatal("local session not deleted with User Management down")
	}
}

func TestSSOSetupGone(t *testing.T) {
	f := newFakeUM(t)
	c, _ := newTestServerWith(t, ssoConfig(f))
	c.get("/setup", 404)
	// No users yet, but /login goes to User Management, not /setup.
	resp := c.getRaw(c.noFollow(), "/login")
	if !strings.HasPrefix(resp.Header.Get("Location"), f.srv.URL+"/authorize") {
		t.Fatalf("/login went to %q", resp.Header.Get("Location"))
	}
	// The password form can't be posted to either.
	u, _ := url.Parse(c.base)
	var csrf string
	for _, ck := range c.http.Jar.Cookies(u) {
		if ck.Name == csrfCookie {
			csrf = ck.Value
		}
	}
	resp, err := c.noFollow().PostForm(c.base+"/login", url.Values{"username": {"x"}, "password": {"y"}, "_csrf": {csrf}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("POST /login: status %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSSOBreakGlassLocalLogin(t *testing.T) {
	f := newFakeUM(t)
	c, st := newTestServerWith(t, ssoConfig(f))
	// alice signed in through SSO once, so she has no password here.
	c.get("/login", 200)
	u, _ := st.GetUserBySubject("42")

	// User Management is broken; over SSH: local-login on, reset-password.
	f.srv.Close()
	if err := st.SetSetting(localLoginSetting, "on"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(u.ID, "a long password"); err != nil {
		t.Fatal(err)
	}

	other := &client{t: t, base: c.base, http: &http.Client{Jar: mustJar()}}
	body := other.get("/login", 200)
	if !strings.Contains(body, `name="password"`) || !strings.Contains(body, "/auth/start") {
		t.Fatal("expected the password form, with a link to SSO sign-in")
	}
	body = other.post("/login", "/login", url.Values{"username": {"alice"}, "password": {"a long password"}}, 200)
	if !strings.Contains(body, "Welcome back") {
		t.Fatal("break-glass sign-in failed")
	}
	if !strings.Contains(other.get("/account", 200), "Change password") {
		t.Fatal("local password change should be available with local login on")
	}
	other.get("/setup", 404)

	// Turned off again: back to User Management.
	st.SetSetting(localLoginSetting, "off")
	anon := &client{t: t, base: c.base, http: &http.Client{Jar: mustJar()}}
	resp := anon.getRaw(anon.noFollow(), "/login")
	if !strings.HasPrefix(resp.Header.Get("Location"), f.srv.URL+"/authorize") {
		t.Fatalf("/login went to %q with local login off", resp.Header.Get("Location"))
	}
}
