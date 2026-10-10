package sso

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// fake is a stand-in for User Management. It checks Basic auth on every
// call and hands the rest to handle.
func fake(t *testing.T, handle http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "planner" || secret != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid_client","error_description":"bad client"}`)
			return
		}
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	return New("https://accounts.example.org/", srv.URL, "planner", "s3cret"), srv
}

func TestEnabledAndInternalDefault(t *testing.T) {
	if New("", "", "x", "y").Enabled() {
		t.Error("empty PublicURL should not be enabled")
	}
	c := New(" https://a.example.org/ ", "", "x", "y")
	if !c.Enabled() || c.InternalURL != "https://a.example.org" {
		t.Errorf("got enabled=%v internal=%q", c.Enabled(), c.InternalURL)
	}
	var nilc *Client
	if nilc.Enabled() {
		t.Error("nil client should not be enabled")
	}
}

func TestAuthorize(t *testing.T) {
	c := New("https://accounts.example.org", "http://127.0.0.1:8100", "planner", "s3cret")
	a, err := c.Authorize("https://planner.example.org/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://accounts.example.org/authorize" {
		t.Errorf("url = %s", a.URL)
	}
	q := u.Query()
	want := map[string]string{
		"response_type":         "code",
		"client_id":             "planner",
		"redirect_uri":          "https://planner.example.org/sso/callback",
		"state":                 a.State,
		"code_challenge_method": "S256",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if a.State == "" || len(a.Verifier) < 43 {
		t.Errorf("state %q verifier %q", a.State, a.Verifier)
	}
	// RFC 7636 appendix B example.
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("Challenge = %s", got)
	}
	if q.Get("code_challenge") != Challenge(a.Verifier) {
		t.Error("code_challenge doesn't match the verifier")
	}
	b, _ := c.Authorize("x")
	if b.State == a.State || b.Verifier == a.Verifier {
		t.Error("state and verifier should be fresh each time")
	}
}

func TestExchange(t *testing.T) {
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/token" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != "https://p/cb" ||
			r.PostForm.Get("code_verifier") != "ver" {
			t.Errorf("form = %v", r.PostForm)
		}
		if r.PostForm.Get("code") != "good" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"code used or expired"}`)
			return
		}
		io.WriteString(w, `{"grant":"g1","user":{"sub":"7","username":"amy","name":"Amy","email":"a@x","role":"admin","user_admin":true}}`)
	})
	grant, u, err := c.Exchange(context.Background(), "good", "https://p/cb", "ver")
	if err != nil {
		t.Fatal(err)
	}
	if grant != "g1" || u.Sub != "7" || u.Username != "amy" || u.Role != "admin" || !u.UserAdmin {
		t.Errorf("got %q %+v", grant, u)
	}

	_, _, err = c.Exchange(context.Background(), "bad", "https://p/cb", "ver")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Code != "invalid_grant" || ae.Description != "code used or expired" {
		t.Errorf("err = %v", err)
	}
}

func TestBasicAuthSent(t *testing.T) {
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"user":{"sub":"1"}}`)
	})
	if _, err := c.CheckGrant(context.Background(), "g"); err != nil {
		t.Fatal(err)
	}
	c.ClientSecret = "wrong"
	// A bad secret is the app's problem, not a revoked grant: it must not
	// sign everyone out, but the APIError is still there to log.
	_, err := c.CheckGrant(context.Background(), "g")
	var ae *APIError
	if errors.Is(err, ErrGrantRevoked) || !errors.Is(err, ErrUnreachable) ||
		!errors.As(err, &ae) || ae.Code != "invalid_client" || ae.Status != 401 {
		t.Errorf("CheckGrant with bad secret: err = %v", err)
	}
	_, _, err = c.Exchange(context.Background(), "c", "r", "v")
	ae = nil
	if !errors.As(err, &ae) || ae.Code != "invalid_client" {
		t.Errorf("err = %v", err)
	}
}

func TestCheckGrant(t *testing.T) {
	c, srv := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/grant" {
			t.Errorf("path %s", r.URL.Path)
		}
		switch r.Header.Get("X-Grant") {
		case "good":
			io.WriteString(w, `{"user":{"sub":"7","username":"amy","role":"user"}}`)
		case "bare":
			w.WriteHeader(http.StatusUnauthorized)
		case "boom":
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"server_error","error_description":"something went wrong"}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"signed out"}`)
		}
	})
	ctx := context.Background()
	u, err := c.CheckGrant(ctx, "good")
	if err != nil || u.Sub != "7" || u.Role != "user" {
		t.Errorf("good: %+v %v", u, err)
	}
	if _, err := c.CheckGrant(ctx, "gone"); !errors.Is(err, ErrGrantRevoked) {
		t.Errorf("gone: %v", err)
	}
	if _, err := c.CheckGrant(ctx, "bare"); !errors.Is(err, ErrGrantRevoked) {
		t.Errorf("401 without a code: %v", err)
	}
	if _, err := c.CheckGrant(ctx, "boom"); !errors.Is(err, ErrUnreachable) || errors.Is(err, ErrGrantRevoked) {
		t.Errorf("5xx: %v", err)
	}
	srv.Close()
	if _, err := c.CheckGrant(ctx, "good"); !errors.Is(err, ErrUnreachable) || errors.Is(err, ErrGrantRevoked) {
		t.Errorf("down: %v", err)
	}
}

func TestLogout(t *testing.T) {
	var got string
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/logout" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		got = r.Header.Get("X-Grant")
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.Logout(context.Background(), "g9"); err != nil || got != "g9" {
		t.Errorf("err %v grant %q", err, got)
	}
}

func TestAppUsersCacheAndForget(t *testing.T) {
	var calls atomic.Int32
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/apps/planner/users" {
			t.Errorf("path %s", r.URL.Path)
		}
		calls.Add(1)
		io.WriteString(w, `{"roles":["user","admin"],"users":[{"sub":"7","username":"amy","role":"admin","suspended":false,"locked":true,"disabled":false,"active":true,"updated_by":"bob","updated_at":"2026-10-01"}]}`)
	})
	ctx := context.Background()
	au, err := c.AppUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(au.Roles) != 2 || len(au.Users) != 1 || au.Users[0].Sub != "7" || !au.Users[0].Locked || !au.Users[0].Active || au.Users[0].UpdatedBy != "bob" {
		t.Errorf("got %+v", au)
	}
	c.AppUsers(ctx)
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (cached)", calls.Load())
	}
	c.Forget()
	c.AppUsers(ctx)
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 after Forget", calls.Load())
	}
}

func TestUpdateAppUser(t *testing.T) {
	var body map[string]any
	var acting string
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"roles":["user","admin"],"users":[]}`)
			return
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/apps/planner/users/7" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		acting = r.Header.Get("X-Acting-Grant")
		body = nil
		json.NewDecoder(r.Body).Decode(&body)
		if body["role"] == "owner" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, `{"error":"bad_role","error_description":"roles for this app: user, admin"}`)
			return
		}
		if body["role"] == "user" {
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"error":"last_admin","error_description":"last admin"}`)
			return
		}
		io.WriteString(w, `{"sub":"7","role":"admin","suspended":true}`)
	})
	ctx := context.Background()
	yes := true

	u, err := c.UpdateAppUser(ctx, "admin-grant", "7", nil, &yes)
	if err != nil || !u.Suspended || u.Sub != "7" {
		t.Fatalf("%+v %v", u, err)
	}
	if acting != "admin-grant" {
		t.Errorf("X-Acting-Grant = %q", acting)
	}
	if _, ok := body["role"]; ok || body["suspended"] != true || len(body) != 1 {
		t.Errorf("body = %v, want only suspended", body)
	}

	role := "admin"
	c.UpdateAppUser(ctx, "g", "7", &role, nil)
	if _, ok := body["suspended"]; ok || body["role"] != "admin" || len(body) != 1 {
		t.Errorf("body = %v, want only role", body)
	}

	for role, code := range map[string]string{"owner": "bad_role", "user": "last_admin"} {
		_, err = c.UpdateAppUser(ctx, "g", "7", &role, nil)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Code != code {
			t.Errorf("%s: err = %v", role, err)
		} else if ae.Message() == "" || strings.Contains(ae.Message(), "HTTP") {
			t.Errorf("%s: message = %q", role, ae.Message())
		}
	}
}

func TestUpdateForgetsCache(t *testing.T) {
	var gets atomic.Int32
	c, _ := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			io.WriteString(w, `{"roles":["user","admin"],"users":[]}`)
			return
		}
		io.WriteString(w, `{"sub":"7"}`)
	})
	ctx := context.Background()
	c.AppUsers(ctx)
	no := false
	c.UpdateAppUser(ctx, "g", "7", nil, &no)
	c.AppUsers(ctx)
	if gets.Load() != 2 {
		t.Errorf("gets = %d, want 2", gets.Load())
	}
}
