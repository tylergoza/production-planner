// Package umtoken checks User Management delegated tokens ("umd_…"), the
// bearer tokens the church MCP server (and later the website) send to an
// app's per-person API, /api/v1/me/…. See APP_CONTRACTS.md in mcp_server.
//
// The check is one server-to-server call:
//
//	GET {um}/api/v1/token
//	Authorization: Basic base64(<this app's client ID>:<secret>)
//	X-Token: umd_…
//
// answered 200 {"user":{sub,username,name,email,role,user_admin},"via":"mcp"}.
//
// A good answer is reused for at most MaxCache (60 s), keyed by a SHA-256
// of the UM address, client ID and token, so the token itself is never kept.
// Refusals and failures are never cached. Concurrent checks of one token
// share a single call.
//
// This package is standard library only and knows nothing about the app,
// so it is copied verbatim into each Go app (only the import path changes).
package umtoken

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrRefused: User Management says the token is no good for this app
// (expired, revoked, another audience, malformed). Answer 401.
var ErrRefused = errors.New("the token was refused")

// ErrUnavailable: User Management couldn't be reached, failed, sent
// something unreadable, or turned down this app's own client credentials
// (a misconfiguration here, not the caller's fault). Answer 503.
var ErrUnavailable = errors.New("couldn't check the token with User Management")

// MaxCache is the longest a good answer is reused.
const MaxCache = 60 * time.Second

// maxEntries bounds the cache; past it, the whole cache is dropped.
const maxEntries = 4096

// Config is the app's User Management connection.
type Config struct {
	URL          string // server-to-server address, e.g. "http://10.0.0.5:8100"
	ClientID     string
	ClientSecret string
}

// User is the person the token acts for. Role is their role in this app.
type User struct {
	Sub       string `json:"sub"`
	Username  string `json:"username"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	UserAdmin bool   `json:"user_admin"`
}

// Answer is User Management's answer about a token. Via is the app the
// token was delegated to (e.g. "mcp").
type Answer struct {
	User User   `json:"user"`
	Via  string `json:"via"`
}

// Verifier checks tokens and caches good answers. The zero value is not
// usable; make one with New.
type Verifier struct {
	HTTP *http.Client
	// Now is the clock; tests may replace it.
	Now func() time.Time

	mu       sync.Mutex
	cache    map[[32]byte]entry
	inFlight map[[32]byte]*call
}

type entry struct {
	ans     Answer
	expires time.Time
}

type call struct {
	done chan struct{}
	ans  *Answer
	err  error
}

func New() *Verifier {
	return &Verifier{
		HTTP:     &http.Client{Timeout: 5 * time.Second},
		Now:      time.Now,
		cache:    map[[32]byte]entry{},
		inFlight: map[[32]byte]*call{},
	}
}

func key(cfg Config, token string) [32]byte {
	return sha256.Sum256([]byte(cfg.URL + "\x00" + cfg.ClientID + "\x00" + token))
}

// Check returns who token acts for, from the cache when a good answer is
// under MaxCache old. Errors wrap ErrRefused or ErrUnavailable.
func (v *Verifier) Check(ctx context.Context, cfg Config, token string) (*Answer, error) {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, ErrRefused
	}
	k := key(cfg, token)
	v.mu.Lock()
	if e, ok := v.cache[k]; ok {
		if v.Now().Before(e.expires) {
			v.mu.Unlock()
			ans := e.ans
			return &ans, nil
		}
		delete(v.cache, k)
	}
	if c, ok := v.inFlight[k]; ok {
		v.mu.Unlock()
		select {
		case <-c.done:
			return c.ans, c.err
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
		}
	}
	c := &call{done: make(chan struct{})}
	v.inFlight[k] = c
	v.mu.Unlock()

	// Not the caller's context: a client hanging up shouldn't become a
	// failure that the other waiters share. The HTTP client's timeout
	// bounds the call.
	c.ans, c.err = v.ask(context.WithoutCancel(ctx), cfg, token)

	v.mu.Lock()
	delete(v.inFlight, k)
	if c.err == nil {
		now := v.Now()
		if len(v.cache) >= maxEntries {
			for kk, e := range v.cache {
				if !now.Before(e.expires) {
					delete(v.cache, kk)
				}
			}
			if len(v.cache) >= maxEntries {
				clear(v.cache)
			}
		}
		v.cache[k] = entry{ans: *c.ans, expires: now.Add(MaxCache)}
	}
	v.mu.Unlock()
	close(c.done)
	return c.ans, c.err
}

// ask makes the call to User Management.
func (v *Verifier) ask(ctx context.Context, cfg Config, token string) (*Answer, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if base == "" || cfg.ClientID == "" {
		return nil, fmt.Errorf("%w: User Management isn't set up", ErrUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/token", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req.SetBasicAuth(cfg.ClientID, cfg.ClientSecret)
	req.Header.Set("X-Token", token)
	req.Header.Set("Accept", "application/json")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode >= 300:
		var e struct {
			Error string `json:"error"`
		}
		json.Unmarshal(raw, &e)
		if e.Error == "invalid_client" {
			// This app's own ID or secret is wrong: not the caller's
			// token, so don't send them off for a new one.
			return nil, fmt.Errorf("%w: User Management turned down this app's client credentials", ErrUnavailable)
		}
		return nil, fmt.Errorf("%w (HTTP %d %s)", ErrRefused, resp.StatusCode, e.Error)
	}
	var ans Answer
	if err := json.Unmarshal(raw, &ans); err != nil || ans.User.Sub == "" {
		return nil, fmt.Errorf("%w: unexpected answer", ErrUnavailable)
	}
	return &ans, nil
}
