// Package sso signs people in through the church's central User Management
// service (authorization code + PKCE) and reads and changes their access
// to this app there. Every server-to-server call sends the app's client ID
// and secret with HTTP Basic auth.
package sso

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrGrantRevoked is User Management saying the grant is no good any more:
// the person signed out there, or lost access. Sign them out here too.
var ErrGrantRevoked = errors.New("your sign-in has ended; please sign in again")

// ErrUnreachable means User Management couldn't be reached or failed
// (network error or HTTP 5xx). Callers may keep a session going for a
// grace period rather than signing everyone out.
var ErrUnreachable = errors.New("couldn't reach User Management")

// cacheFor is how long the app's user list is reused.
const cacheFor = time.Minute

// APIError is a refusal from User Management, from its JSON error body.
type APIError struct {
	Status      int
	Code        string // e.g. "forbidden", "locked", "last_admin"
	Description string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("User Management said: %s: %s (HTTP %d)", e.Code, e.Description, e.Status)
}

// Message is a short explanation of a refusal, fit to show on a page.
func (e *APIError) Message() string {
	switch e.Code {
	case "invalid_grant":
		if e.Status == http.StatusUnauthorized {
			return "Your sign-in has ended; please sign in again."
		}
		return "That sign-in didn't work; please try again."
	case "forbidden":
		return "Only this app's admins can change people's access."
	case "locked":
		return "A user admin has locked this person's access; change it in User Management."
	case "no_access":
		return "That person doesn't have access to this app."
	case "last_admin":
		return "This app needs at least one admin; make someone else an admin first."
	case "bad_role":
		return "That isn't one of this app's roles."
	}
	if e.Description != "" {
		return e.Description
	}
	return "User Management turned that down."
}

type Client struct {
	PublicURL    string // where browsers go, e.g. "https://accounts.example.org"
	InternalURL  string // server-to-server, e.g. "http://127.0.0.1:8100"; PublicURL if empty
	ClientID     string
	ClientSecret string
	HTTP         *http.Client

	mu      sync.Mutex
	users   *AppUsers
	usersAt time.Time
}

func New(publicURL, internalURL, clientID, clientSecret string) *Client {
	trim := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
	c := &Client{
		PublicURL:    trim(publicURL),
		InternalURL:  trim(internalURL),
		ClientID:     strings.TrimSpace(clientID),
		ClientSecret: strings.TrimSpace(clientSecret),
		HTTP:         &http.Client{Timeout: 8 * time.Second},
	}
	if c.InternalURL == "" {
		c.InternalURL = c.PublicURL
	}
	return c
}

// Enabled reports whether sign-in goes through User Management. When it
// doesn't, the app uses its own local login.
func (c *Client) Enabled() bool { return c != nil && c.PublicURL != "" }

// JSON shapes, as User Management sends them ---------------------------------

type User struct {
	Sub       string `json:"sub"` // keep as sso_subject
	Username  string `json:"username"`
	Name      string `json:"name"` // may be empty
	Email     string `json:"email"`
	Role      string `json:"role"`
	UserAdmin bool   `json:"user_admin"` // show a "Manage in User Management" link
}

type AppUser struct {
	User
	Suspended bool   `json:"suspended"` // an admin turned their access to this app off
	Locked    bool   `json:"locked"`    // a user admin pinned it; show read-only
	Disabled  bool   `json:"disabled"`  // their whole account is turned off
	Active    bool   `json:"active"`    // can sign in to this app now
	UpdatedBy string `json:"updated_by"`
	UpdatedAt string `json:"updated_at"`
}

type AppUsers struct {
	Roles []string  `json:"roles"`
	Users []AppUser `json:"users"`
}

// Sign-in --------------------------------------------------------------------

// Authorization is the start of a sign-in. Keep State and Verifier in a
// short-lived cookie and send the browser to URL.
type Authorization struct {
	URL      string
	State    string
	Verifier string
}

// Authorize makes a fresh state and PKCE verifier and the /authorize URL.
func (c *Client) Authorize(redirectURI string) (*Authorization, error) {
	state, err := randomString()
	if err != nil {
		return nil, err
	}
	verifier, err := randomString()
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {state},
		"code_challenge":        {Challenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return &Authorization{URL: c.PublicURL + "/authorize?" + q.Encode(), State: state, Verifier: verifier}, nil
}

// Challenge is the S256 PKCE challenge for a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Exchange trades the code from the redirect back for a grant and the person.
func (c *Client) Exchange(ctx context.Context, code, redirectURI, verifier string) (grant string, u *User, err error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	var out struct {
		Grant string `json:"grant"`
		User  User   `json:"user"`
	}
	err = c.do(ctx, http.MethodPost, "/token", strings.NewReader(form.Encode()),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, &out)
	if err != nil {
		return "", nil, err
	}
	return out.Grant, &out.User, nil
}

// CheckGrant asks whether a grant is still good and who it's for now.
// It returns ErrGrantRevoked when the service says invalid_grant (or a
// 401/404 with no error code), and an error wrapping ErrUnreachable when
// User Management is down or turns down this app's own credentials
// (invalid_client).
func (c *Client) CheckGrant(ctx context.Context, grant string) (*User, error) {
	var out struct {
		User User `json:"user"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/grant", nil, map[string]string{"X-Grant": grant}, &out)
	var ae *APIError
	if errors.As(err, &ae) {
		switch {
		case ae.Code == "invalid_grant",
			ae.Code == "" && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusNotFound):
			return nil, ErrGrantRevoked
		case ae.Code == "invalid_client":
			// A bad client ID or secret is this app's misconfiguration,
			// not the person's sign-in ending. Treat it like an outage so
			// callers apply their grace period instead of signing
			// everyone out; errors.As still finds the *APIError.
			return nil, fmt.Errorf("%w: %w", ErrUnreachable, ae)
		}
	}
	if err != nil {
		return nil, err
	}
	return &out.User, nil
}

// Logout ends the person's session in User Management, and so in every app.
func (c *Client) Logout(ctx context.Context, grant string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/logout", nil, map[string]string{"X-Grant": grant}, nil)
}

// App access -----------------------------------------------------------------

// AppUsers is everyone with access to this app (suspended and turned off
// included) and the app's roles. Answers are reused for a minute.
func (c *Client) AppUsers(ctx context.Context) (*AppUsers, error) {
	c.mu.Lock()
	hit, at := c.users, c.usersAt
	c.mu.Unlock()
	if hit != nil && time.Since(at) < cacheFor {
		return hit, nil
	}
	var out AppUsers
	if err := c.do(ctx, http.MethodGet, "/api/v1/apps/"+url.PathEscape(c.ClientID)+"/users", nil, nil, &out); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.users, c.usersAt = &out, time.Now()
	c.mu.Unlock()
	return &out, nil
}

// Forget drops the cached user list, e.g. after a change.
func (c *Client) Forget() {
	c.mu.Lock()
	c.users = nil
	c.mu.Unlock()
}

// UpdateAppUser changes a person's role and/or suspension in this app, as
// the admin whose grant is actingGrant. Nil fields are left alone.
// Refusals come back as *APIError; use its Message on the page.
func (c *Client) UpdateAppUser(ctx context.Context, actingGrant, sub string, role *string, suspended *bool) (*AppUser, error) {
	body := map[string]any{}
	if role != nil {
		body["role"] = *role
	}
	if suspended != nil {
		body["suspended"] = *suspended
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var out AppUser
	path := "/api/v1/apps/" + url.PathEscape(c.ClientID) + "/users/" + url.PathEscape(sub)
	err = c.do(ctx, http.MethodPatch, path, bytes.NewReader(b),
		map[string]string{"Content-Type": "application/json", "X-Acting-Grant": actingGrant}, &out)
	c.Forget()
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// do makes one call to User Management. out may be nil.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, headers map[string]string, out any) error {
	if !c.Enabled() {
		return errors.New("User Management isn't set up")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.InternalURL+path, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%w (HTTP %d)", ErrUnreachable, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		json.Unmarshal(raw, &e)
		return &APIError{Status: resp.StatusCode, Code: e.Error, Description: e.Description}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("User Management sent something unexpected (HTTP %d)", resp.StatusCode)
	}
	return nil
}
