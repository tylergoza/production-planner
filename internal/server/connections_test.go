package server

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylergoza/production-planner/internal/store"
)

func newConnServer(t *testing.T, env map[string]string) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(Config{Env: env}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return srv, st
}

func TestConnectionWholeFromEnv(t *testing.T) {
	// One variable set on the server: all of the connection comes from
	// there, so the saved token isn't mixed in.
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": "https://cal.example.org"})
	st.SetSetting("calendar_url", "https://saved.example.org")
	st.SetSecretSetting("calendar_token", "cal_saved")
	r := srv.resolve(connCalendar)
	if r.Source != sourceEnv || r.Get("calendar_url") != "https://cal.example.org" || r.Get("calendar_token") != "" {
		t.Fatalf("resolved %+v", r)
	}
	if err := srv.saveConnection(connCalendar, map[string]string{"calendar_url": "x"}, nil, "admin"); !errors.Is(err, errFromEnv) {
		t.Fatalf("save over env: %v", err)
	}
}

func TestConnectionSettings(t *testing.T) {
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": ""})
	if r := srv.resolve(connCalendar); r.Source != sourceOff {
		t.Fatalf("source = %s, want off", r.Source)
	}
	save := func(v map[string]string, remove map[string]bool) {
		t.Helper()
		if err := srv.saveConnection(connCalendar, v, remove, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	save(map[string]string{"calendar_url": "https://cal.example.org", "calendar_token": "cal_one"}, nil)
	r := srv.resolve(connCalendar)
	if r.Source != sourceSettings || r.Get("calendar_token") != "cal_one" {
		t.Fatalf("resolved %+v", r)
	}
	if raw := st.Setting("calendar_token", ""); raw == "cal_one" {
		t.Fatal("token saved in plain text")
	}
	// A blank secret keeps the saved one.
	save(map[string]string{"calendar_url": "https://cal2.example.org", "calendar_token": ""}, nil)
	if r := srv.resolve(connCalendar); r.Get("calendar_token") != "cal_one" || r.Get("calendar_url") != "https://cal2.example.org" {
		t.Fatalf("after blank: %+v", r)
	}
	// Remove clears it.
	save(map[string]string{"calendar_url": "https://cal2.example.org"}, map[string]bool{"calendar_token": true})
	if r := srv.resolve(connCalendar); r.Get("calendar_token") != "" {
		t.Fatalf("after remove: %+v", r)
	}
}

func TestTrackerConnectionFromEnv(t *testing.T) {
	srv, st := newConnServer(t, map[string]string{"TRACKER_URL": "https://mt.example.org", "TRACKER_TOKEN": "mt_env"})
	st.SetSetting("tracker_url", "https://saved.example.org")
	srv.reloadSettings()
	if tr := srv.Tracker(); tr.BaseURL != "https://mt.example.org" || tr.Token != "mt_env" {
		t.Fatalf("tracker = %s %s", tr.BaseURL, tr.Token)
	}
}

// connAdmin is a test server with one admin, signed in.
func connAdmin(t *testing.T, env map[string]string) (*client, *store.Store) {
	t.Helper()
	c, st := newTestServerWith(t, Config{Env: env})
	st.CreateUser("boss", "", "a long password", true)
	return c.as("boss"), st
}

func TestConnectionsPageNeedsAdmin(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", false)
	pat := c.as("pat")
	pat.get("/admin/connections", 403)
	pat.post("/account", "/admin/connections/calendar", url.Values{"calendar_url": {"https://x.example.org"}}, 403)
	pat.post("/account", "/admin/connections/calendar/test", url.Values{}, 403)
	pat.post("/account", "/admin/connections/calendar/keep", url.Values{}, 403)
	if st.Setting("calendar_url", "") != "" {
		t.Fatal("a planner saved a connection")
	}
}

func TestConnectionsSaveCalendar(t *testing.T) {
	boss, st := connAdmin(t, nil)
	if body := boss.get("/admin/connections", 200); !strings.Contains(body, "Not connected") {
		t.Error("expected Not connected")
	}
	body := boss.post("/admin/connections", "/admin/connections/calendar", url.Values{
		"calendar_url": {"http://127.0.0.1:1/"}, "calendar_token": {"cal_abcd1234"},
	}, 200)
	// Saved even though nothing answers there.
	if !strings.Contains(body, "Saved.") || !strings.Contains(body, "Can&#39;t reach http://127.0.0.1:1") {
		t.Errorf("expected saved + can't reach flash")
	}
	if st.Setting("calendar_url", "") != "http://127.0.0.1:1" || st.Setting("conn_calendar_changed_by", "") != "boss" {
		t.Error("calendar not saved with who changed it")
	}
	if !strings.Contains(body, "Set here") || !strings.Contains(body, "ends …1234") || strings.Contains(body, "cal_abcd1234") {
		t.Error("page should say Set here and show only the secret's tail")
	}
	// Blank secret keeps the saved one.
	boss.post("/admin/connections", "/admin/connections/calendar", url.Values{"calendar_url": {"http://127.0.0.1:2"}}, 200)
	if tok, _ := st.SecretSetting("calendar_token"); tok != "cal_abcd1234" {
		t.Errorf("token = %q", tok)
	}
	// Checks.
	if body := boss.post("/admin/connections", "/admin/connections/calendar", url.Values{"calendar_token": {"mt_wrong"}, "calendar_url": {"ftp://x"}}, 422); !strings.Contains(body, "should start with cal_") || !strings.Contains(body, "should look like https://") {
		t.Error("expected validation errors")
	}
	// Remove.
	boss.post("/admin/connections", "/admin/connections/calendar", url.Values{"remove_calendar_token": {"1"}}, 200)
	if tok, _ := st.SecretSetting("calendar_token"); tok != "" {
		t.Error("token not removed")
	}
	boss.post("/admin/connections", "/admin/connections/nope", url.Values{}, 404)
}

func TestConnectionsFromEnvKeep(t *testing.T) {
	boss, st := connAdmin(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_envtoken9"})
	body := boss.get("/admin/connections", 200)
	if !strings.Contains(body, "Set on the server") || !strings.Contains(body, "Keep in settings") || strings.Contains(body, "cal_envtoken9") {
		t.Error("env connection page is wrong")
	}
	boss.post("/admin/connections", "/admin/connections/calendar", url.Values{"calendar_url": {"https://other.example.org"}}, 200)
	if st.Setting("calendar_url", "") != "" {
		t.Fatal("saved over the server's connection")
	}
	st.SetSetting("calendar_url", "https://old.example.org")
	if body := boss.get("/admin/connections", 200); !strings.Contains(body, "Settings differ from the server") {
		t.Error("expected the differ warning")
	}
	boss.post("/admin/connections", "/admin/connections/calendar/keep", url.Values{}, 200)
	if tok, _ := st.SecretSetting("calendar_token"); tok != "cal_envtoken9" || st.Setting("calendar_url", "") != "http://127.0.0.1:1" {
		t.Fatalf("keep didn't copy: %q %q", tok, st.Setting("calendar_url", ""))
	}
	if body := boss.get("/admin/connections", 200); !strings.Contains(body, "settings match, so it's safe to remove") {
		t.Error("expected the settings-match line")
	}
}

func TestConnectionTestRefusedToken(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mt_good" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"bad token"}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"not found"}`)
	}))
	defer fake.Close()
	boss, _ := connAdmin(t, nil)
	if body := boss.post("/admin/connections", "/admin/connections/tracker", url.Values{"tracker_url": {fake.URL}, "tracker_token": {"mt_bad"}}, 200); !strings.Contains(body, "The token was refused") {
		t.Error("save should report the refused token")
	}
	if body := boss.post("/admin/connections", "/admin/connections/tracker/test", url.Values{}, 200); !strings.Contains(body, "The token was refused") {
		t.Error("test should report the refused token")
	}
	boss.post("/admin/connections", "/admin/connections/tracker", url.Values{"tracker_url": {fake.URL}, "tracker_token": {"mt_good"}}, 200)
	if body := boss.post("/admin/connections", "/admin/connections/tracker/test", url.Values{}, 200); !strings.Contains(body, "Connected to the maintenance tracker") {
		t.Error("test should connect")
	}
}
