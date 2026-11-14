package umtoken

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	srv    *httptest.Server
	hits   atomic.Int32
	mu     sync.Mutex
	status int // 0 = 200 with the user
	delay  time.Duration
}

func newFake(t *testing.T) *fake {
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/token" {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		status, delay := f.status, f.delay
		f.mu.Unlock()
		time.Sleep(delay)
		if id, sec, ok := r.BasicAuth(); !ok || id != "calendar" || sec != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		if r.Header.Get("X-Token") != "umd_good" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_token"}`))
			return
		}
		json.NewEncoder(w).Encode(Answer{User: User{Sub: "7", Username: "mel", Role: "member"}, Via: "mcp"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) cfg() Config {
	return Config{URL: f.srv.URL, ClientID: "calendar", ClientSecret: "s3cret"}
}

func TestCheckAndCache(t *testing.T) {
	f := newFake(t)
	v := New()
	now := time.Now()
	v.Now = func() time.Time { return now }
	ctx := context.Background()

	ans, err := v.Check(ctx, f.cfg(), "umd_good")
	if err != nil || ans.User.Sub != "7" || ans.Via != "mcp" {
		t.Fatalf("Check = %+v, %v", ans, err)
	}
	now = now.Add(59 * time.Second)
	if _, err := v.Check(ctx, f.cfg(), "umd_good"); err != nil || f.hits.Load() != 1 {
		t.Fatalf("within 60 s should be cached: hits %d, err %v", f.hits.Load(), err)
	}
	now = now.Add(2 * time.Second)
	if _, err := v.Check(ctx, f.cfg(), "umd_good"); err != nil || f.hits.Load() != 2 {
		t.Fatalf("after 60 s should ask again: hits %d, err %v", f.hits.Load(), err)
	}

	// Refusals aren't cached.
	f.hits.Store(0)
	for range 2 {
		if _, err := v.Check(ctx, f.cfg(), "umd_bad"); !errors.Is(err, ErrRefused) {
			t.Fatalf("bad token: %v", err)
		}
	}
	if f.hits.Load() != 2 {
		t.Errorf("refusals were cached: hits %d", f.hits.Load())
	}
	// A different client ID doesn't reuse the cached answer.
	other := f.cfg()
	other.ClientSecret = "wrong"
	if _, err := v.Check(ctx, Config{URL: f.srv.URL, ClientID: "events", ClientSecret: "x"}, "umd_good"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("another client should be checked, and invalid_client is unavailable: %v", err)
	}
	if _, err := New().Check(ctx, other, "umd_good"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("bad secret: %v", err)
	}
}

func TestCheckFailures(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	f.status = http.StatusInternalServerError
	if _, err := New().Check(ctx, f.cfg(), "umd_good"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("500: %v", err)
	}
	f.status = http.StatusForbidden
	if _, err := New().Check(ctx, f.cfg(), "umd_good"); !errors.Is(err, ErrRefused) {
		t.Errorf("403: %v", err)
	}
	if _, err := New().Check(ctx, f.cfg(), ""); !errors.Is(err, ErrRefused) {
		t.Errorf("empty: %v", err)
	}
	f.srv.Close()
	if _, err := New().Check(ctx, f.cfg(), "umd_good"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("closed: %v", err)
	}
}

func TestCheckSharesOneCall(t *testing.T) {
	f := newFake(t)
	f.delay = 100 * time.Millisecond
	v := New()
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.Check(context.Background(), f.cfg(), "umd_good"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := f.hits.Load(); n != 1 {
		t.Errorf("a burst made %d calls, want 1", n)
	}
}
