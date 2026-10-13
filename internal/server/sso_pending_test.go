package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/production-planner/internal/secrets"
)

func pendingForm(f *fakeUM, secret string) url.Values {
	return url.Values{"sso_url": {f.srv.URL}, "sso_client_id": {"planner"}, "sso_client_secret": {secret}}
}

// postNoFollow submits a form (CSRF token taken from tokenPage) and stops
// at the first redirect.
func postNoFollow(t *testing.T, c *client, tokenPage, path string, form url.Values) *http.Response {
	t.Helper()
	m := csrfRe.FindStringSubmatch(c.get(tokenPage, 200))
	if m == nil {
		t.Fatalf("no csrf token on %s", tokenPage)
	}
	form.Set("_csrf", m[1])
	resp, err := c.noFollow().PostForm(c.base+path, form)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

// testSignIn presses Test sign-in and, when it heads to User Management,
// goes through it and back to the callback. It returns the page the
// callback sent the admin to.
func testSignIn(t *testing.T, c *client) (leftApp bool, page string) {
	t.Helper()
	resp := postNoFollow(t, c, "/admin/connections", "/admin/connections/sso/signin-test", url.Values{})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("signin-test = %d", resp.StatusCode)
	}
	if strings.HasPrefix(loc, "/") {
		return false, c.get("/admin/connections", 200)
	}
	// User Management's /authorize signs in at once and sends the browser back.
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	um, err := plain.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	um.Body.Close()
	back := um.Header.Get("Location")
	if !strings.HasPrefix(back, c.base+"/auth/callback") {
		t.Fatalf("User Management sent the browser to %q", back)
	}
	resp2 := c.getRaw(c.noFollow(), strings.TrimPrefix(back, c.base))
	if resp2.StatusCode != http.StatusSeeOther {
		return true, "status " + resp2.Status
	}
	return true, c.get("/admin/connections", 200)
}

func isLoginLocal(t *testing.T, c *client) bool {
	t.Helper()
	jar := mustJar()
	anon := &client{t: t, base: c.base, http: &http.Client{Jar: jar}}
	return anon.getRaw(anon.noFollow(), "/login").StatusCode == http.StatusOK
}

func TestSSOPendingSaveDoesNotChangeSignIn(t *testing.T) {
	f := newFakeUM(t)
	srv, st, c := newSSOConnServer(t, nil)
	boss := c.as("boss")

	// Missing secret is refused.
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, ""), 422)
	if _, ok := srv.pendingSSO(); ok {
		t.Fatal("pending saved without a secret")
	}
	body := boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, "s3cret"), 200)
	if !strings.Contains(body, "Pending: not live until a test sign-in works") || !strings.Contains(body, "Test sign-in") || !strings.Contains(body, "Discard") {
		t.Fatalf("page doesn't show the pending state:\n%s", body)
	}
	if strings.Contains(body, "s3cret") {
		t.Fatal("secret shown on the page")
	}
	if srv.SSO().Enabled() || st.Setting("sso_url", "") != "" {
		t.Fatal("saving changed the live setting")
	}
	if !isLoginLocal(t, c) {
		t.Fatal("/login no longer shows the local form")
	}
	if v := st.Setting("sso_pending_client_secret", ""); !secrets.IsSealed(v) {
		t.Fatalf("pending secret not sealed: %q", v)
	}
	if st.Setting("sso_pending_url", "") != f.srv.URL {
		t.Fatal("pending url not stored")
	}
	// A blank secret on a later save keeps the pending one.
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, ""), 200)
	if s, _ := st.SecretSetting("sso_pending_client_secret"); s != "s3cret" {
		t.Fatalf("pending secret lost: %q", s)
	}
	// Bad address is refused.
	boss.post("/admin/connections", "/admin/connections/sso", url.Values{"sso_url": {"ftp://x"}, "sso_client_id": {"planner"}}, 422)
	// The form-action CSP allows User Management on this page only.
	resp := boss.getRaw(boss.http, "/admin/connections")
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' "+f.srv.URL) {
		t.Fatalf("CSP: %s", csp)
	}
	if csp := boss.getRaw(boss.http, "/account").Header.Get("Content-Security-Policy"); strings.Contains(csp, f.srv.URL) {
		t.Fatalf("CSP allows pending origin elsewhere: %s", csp)
	}
}

func TestSSOTestSignInAsNonAdminIsRefused(t *testing.T) {
	f := newFakeUM(t)
	f.set(func(f *fakeUM) { f.user.Role = "editor"; f.user.Username = "boss" })
	srv, st, c := newSSOConnServer(t, nil)
	boss := c.as("boss")
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, "s3cret"), 200)

	left, page := testSignIn(t, boss)
	if !left {
		t.Fatal("test sign-in never went to User Management")
	}
	if !strings.Contains(page, "isn&#39;t an admin of this app in User Management") && !strings.Contains(page, "isn't an admin of this app in User Management") {
		t.Fatalf("no reason shown:\n%s", page)
	}
	if srv.SSO().Enabled() || st.Setting("sso_url", "") != "" {
		t.Fatal("went live for a non-admin")
	}
	if _, ok := srv.pendingSSO(); !ok {
		t.Fatal("pending was dropped")
	}
	boss.get("/admin/connections", 200) // still signed in
	if !isLoginLocal(t, c) {
		t.Fatal("login changed")
	}
}

func TestSSOTestSignInRefusesBadSecretBeforeLeaving(t *testing.T) {
	f := newFakeUM(t)
	srv, _, c := newSSOConnServer(t, nil)
	boss := c.as("boss")
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, "wrong"), 200)
	left, page := testSignIn(t, boss)
	if left {
		t.Fatal("went to User Management with a bad secret")
	}
	if !strings.Contains(page, "refused the client ID or secret") {
		t.Fatalf("no reason shown:\n%s", page)
	}
	if srv.SSO().Enabled() {
		t.Fatal("live changed")
	}
	if _, ok := srv.pendingSSO(); !ok {
		t.Fatal("pending dropped")
	}
}

func TestSSOTestSignInAsAdminMakesItLive(t *testing.T) {
	f := newFakeUM(t)
	f.set(func(f *fakeUM) { f.user.Username = "boss" })
	srv, st, c := newSSOConnServer(t, nil)
	boss := c.as("boss")
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, "s3cret"), 200)

	left, page := testSignIn(t, boss)
	if !left || !strings.Contains(page, "Single sign-on is live") {
		t.Fatalf("not live (left=%v):\n%s", left, page)
	}
	if !srv.SSO().Enabled() || srv.SSOBroken() != "" {
		t.Fatal("SSO not enabled")
	}
	if _, ok := srv.pendingSSO(); ok || st.Setting("sso_pending_client_secret", "") != "" {
		t.Fatal("pending not cleared")
	}
	if s, _ := st.SecretSetting("sso_client_secret"); s != "s3cret" || !secrets.IsSealed(st.Setting("sso_client_secret", "")) {
		t.Fatal("live secret not stored sealed")
	}
	var sub string
	st.DB.QueryRow(`SELECT sso_subject FROM users WHERE username = 'boss'`).Scan(&sub)
	if sub != "42" {
		t.Fatalf("boss not linked to their account: %q", sub)
	}
	boss.get("/admin/connections", 200) // the session stays
	anon := &client{t: t, base: c.base, http: &http.Client{Jar: mustJar()}}
	resp := anon.getRaw(anon.noFollow(), "/login")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), f.srv.URL) {
		t.Fatalf("login = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestSSOTestCallbackNeedsTheSameSession(t *testing.T) {
	f := newFakeUM(t)
	f.set(func(f *fakeUM) { f.user.Username = "boss" })
	srv, _, c := newSSOConnServer(t, nil)
	boss := c.as("boss")
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, "s3cret"), 200)
	resp := postNoFollow(t, boss, "/admin/connections", "/admin/connections/sso/signin-test", url.Values{})
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	um, _ := plain.Get(resp.Header.Get("Location"))
	um.Body.Close()
	back := strings.TrimPrefix(um.Header.Get("Location"), boss.base)
	// Signed out (and in again as someone else) before it came back.
	postNoFollow(t, boss, "/account", "/logout", url.Values{})
	boss.post("/login", "/login", url.Values{"username": {"boss"}, "password": {"a long password"}}, 200)
	if r := boss.getRaw(boss.noFollow(), back); r.StatusCode != http.StatusForbidden {
		t.Fatalf("callback from another session = %d", r.StatusCode)
	}
	if srv.SSO().Enabled() {
		t.Fatal("went live")
	}
}

func TestSSOPendingDiscard(t *testing.T) {
	f := newFakeUM(t)
	srv, _, c := newSSOConnServer(t, nil)
	boss := c.as("boss")
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, "s3cret"), 200)
	boss.post("/admin/connections", "/admin/connections/sso/discard", url.Values{}, 200)
	if _, ok := srv.pendingSSO(); ok {
		t.Fatal("pending kept")
	}
	if srv.store.Setting("sso_pending_client_secret", "") != "" {
		t.Fatal("pending secret kept")
	}
}

func TestSSOTurnOff(t *testing.T) {
	f := newFakeUM(t)
	srv, st, c := newSSOConnServer(t, nil)
	st.SetSetting("sso_url", f.srv.URL)
	st.SetSetting("sso_client_id", "planner")
	st.SetSecretSetting("sso_client_secret", "s3cret")
	srv.reloadSettings()
	if isLoginLocal(t, c) {
		t.Fatal("SSO should be on")
	}
	// The local password login works as break-glass (local-login on).
	st.SetSetting(localLoginSetting, "on")
	boss := c.as("boss")
	st.SetSetting(localLoginSetting, "")
	body := boss.get("/admin/connections", 200)
	if !strings.Contains(body, "reset-password") || !strings.Contains(body, "/admin/connections/sso/off") {
		t.Fatal("turn off isn't offered with its warning")
	}
	boss.post("/admin/connections", "/admin/connections/sso/off", url.Values{}, 200)
	if srv.SSO().Enabled() || st.Setting("sso_url", "") != "" || st.Setting("sso_client_secret", "") != "" {
		t.Fatal("not turned off")
	}
	if !isLoginLocal(t, c) {
		t.Fatal("login isn't local after turning off")
	}
}

func TestSSOFromEnvIsReadOnly(t *testing.T) {
	f := newFakeUM(t)
	srv, st, c := newSSOConnServer(t, ssoConfig(f).Env)
	st.SetSetting(localLoginSetting, "on")
	boss := c.as("boss")
	st.SetSetting(localLoginSetting, "")
	body := boss.get("/admin/connections", 200)
	if strings.Contains(body, "/admin/connections/sso/off") || strings.Contains(body, "signin-test") {
		t.Fatal("env SSO offers changes")
	}
	if !strings.Contains(body, "Keep in settings") {
		t.Fatal("no Keep in settings for env SSO")
	}
	boss.post("/admin/connections", "/admin/connections/sso", pendingForm(f, "x"), 200)
	boss.post("/admin/connections", "/admin/connections/sso/off", url.Values{}, 200)
	if !srv.SSO().Enabled() {
		t.Fatal("env SSO turned off")
	}
	if _, ok := srv.pendingSSO(); ok {
		t.Fatal("pending saved over env")
	}
	boss.post("/admin/connections", "/admin/connections/sso/signin-test", url.Values{}, 200)
	boss.post("/admin/connections", "/admin/connections/sso/keep", url.Values{}, 200)
	if s, _ := st.SecretSetting("sso_client_secret"); s != "s3cret" || st.Setting("sso_url", "") != f.srv.URL {
		t.Fatal("Keep in settings didn't copy the values")
	}
}

func TestSSOPendingRoutesNeedAdmin(t *testing.T) {
	f := newFakeUM(t)
	_, st, c := newSSOConnServer(t, nil)
	hana := c.as("hana")
	for _, p := range []string{"", "/test", "/signin-test", "/discard", "/off"} {
		hana.post("/account", "/admin/connections/sso"+p, pendingForm(f, "s3cret"), 403)
	}
	if st.Setting("sso_pending_url", "") != "" {
		t.Fatal("a helper saved pending values")
	}
}
