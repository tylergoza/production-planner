package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
	"github.com/tylergoza/production-planner/internal/umtoken"
)

// fakeTokenUM answers User Management's GET /api/v1/token for the tokens
// in its map; anything else (another audience, expired) is 401.
type fakeTokenUM struct {
	srv  *httptest.Server
	hits atomic.Int32
	mu   sync.Mutex
	ok   map[string]umtoken.Answer
	down bool
}

func newFakeTokenUM(t *testing.T) *fakeTokenUM {
	f := &fakeTokenUM{ok: map[string]umtoken.Answer{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/token" {
			http.NotFound(w, r)
			return
		}
		f.hits.Add(1)
		f.mu.Lock()
		ans, ok := f.ok[r.Header.Get("X-Token")]
		down := f.down
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// The token's audience is the planner's UM client ID, "planner".
		if id, sec, good := r.BasicAuth(); !good || id != "planner" || sec != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_client"}`))
			return
		}
		switch {
		case down:
			w.WriteHeader(http.StatusBadGateway)
		case !ok:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"invalid_token"}`))
		default:
			json.NewEncoder(w).Encode(ans)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTokenUM) give(token, sub, username, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok[token] = umtoken.Answer{User: umtoken.User{Sub: sub, Username: username, Name: strings.ToUpper(username[:1]) + username[1:], Role: role}, Via: "mcp"}
}

// serverOn runs the planner on st with cfg. The *Server is returned so
// tests can move the token cache's clock.
func serverOn(t *testing.T, st *store.Store, cfg Config) (*Server, *client) {
	t.Helper()
	srv, err := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, &client{t: t, base: ts.URL, http: &http.Client{Jar: mustJar()}}
}

// meServer runs the planner with single sign-on pointing at f (nil for
// none).
func meServer(t *testing.T, f *fakeTokenUM) (*Server, *store.Store, *client) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := Config{}
	if f != nil {
		cfg.Env = map[string]string{"SSO_URL": f.srv.URL, "SSO_CLIENT_ID": "planner", "SSO_CLIENT_SECRET": "s3cret"}
	}
	srv, c := serverOn(t, st, cfg)
	return srv, st, c
}

// meGet fetches a /me path with a bearer token and checks the headers
// every /me answer must have.
func (c *client) meGet(path, token string, want int) (string, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, c.base+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, want, body)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		c.t.Errorf("GET %s set a cookie", path)
	}
	if resp.Header.Get("Cache-Control") != "private, no-store" {
		c.t.Errorf("GET %s: Cache-Control %q", path, resp.Header.Get("Cache-Control"))
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		c.t.Errorf("GET %s: CORS header on /me", path)
	}
	return string(body), resp.Header
}

func isJSONError(body string) bool {
	var e struct{ Error string }
	return json.Unmarshal([]byte(body), &e) == nil && e.Error != ""
}

func TestMeOffWithoutUserManagement(t *testing.T) {
	_, _, c := meServer(t, nil)
	for _, p := range []string{"/api/v1/me/productions", "/api/v1/me/productions/1", "/api/v1/me/nope"} {
		if body, _ := c.meGet(p, "umd_whatever", 404); isJSONError(body) {
			t.Errorf("%s: off should be a plain 404, got %s", p, body)
		}
	}
}

func TestMeTokenChecks(t *testing.T) {
	f := newFakeTokenUM(t)
	srv, st, c := meServer(t, f)
	f.give("umd_mel", "s-mel", "mel", "editor")

	// No token, not a umd_ token, another audience's, an expired one: all
	// 401 invalid_token, and nobody is added.
	for _, tok := range []string{"", "pp_session_value", "umd_for_events", "umd_expired"} {
		body, h := c.meGet("/api/v1/me/productions", tok, 401)
		if !strings.Contains(body, "invalid_token") || h.Get("WWW-Authenticate") == "" {
			t.Errorf("token %q: %s", tok, body)
		}
	}
	if n, _ := st.CountUsers(); n != 0 {
		t.Errorf("refused tokens made %d users", n)
	}

	// A good token: the user is made like an SSO sign-in, with no session.
	f.hits.Store(0)
	now := time.Now()
	srv.meTokens.Now = func() time.Time { return now }
	c.meGet("/api/v1/me/productions", "umd_mel", 200)
	c.meGet("/api/v1/me/productions?status=done", "umd_mel", 200)
	if n := f.hits.Load(); n != 1 {
		t.Errorf("second call within 60 s asked User Management again: %d calls", n)
	}
	users, _ := st.ListUsers()
	if len(users) != 1 || users[0].SSOSubject != "s-mel" || users[0].IsAdmin || users[0].DisplayName != "Mel" {
		t.Fatalf("users = %+v", users)
	}
	var sessions int
	if err := st.DB.QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("a /me call made %d sessions (%v)", sessions, err)
	}

	// After 60 s it's checked again, and a token gone bad is refused.
	now = now.Add(61 * time.Second)
	f.mu.Lock()
	delete(f.ok, "umd_mel")
	f.mu.Unlock()
	c.meGet("/api/v1/me/productions", "umd_mel", 401)
	if n := f.hits.Load(); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}

	// The role follows the token, as at sign-in.
	f.give("umd_mel2", "s-mel", "mel", "admin")
	c.meGet("/api/v1/me/productions", "umd_mel2", 200)
	if u, _ := st.GetUser(users[0].ID); !u.IsAdmin {
		t.Error("an admin token should make the user an admin, as sign-in does")
	}

	// Another delegate is refused; User Management down is 503, not 401.
	f.mu.Lock()
	f.ok["umd_other"] = umtoken.Answer{User: umtoken.User{Sub: "s-x", Username: "x"}, Via: "someapp"}
	f.mu.Unlock()
	c.meGet("/api/v1/me/productions", "umd_other", 403)
	f.mu.Lock()
	f.down = true
	f.mu.Unlock()
	if _, h := c.meGet("/api/v1/me/productions", "umd_new", 503); h.Get("Retry-After") == "" {
		t.Error("503 without Retry-After")
	}
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()

	// Unknown /me paths are a plain 404 ("not built"); a bad status is a
	// JSON 400.
	for _, p := range []string{"/api/v1/me/nope", "/api/v1/me/productions/1/cast"} {
		if body, _ := c.meGet(p, "umd_mel2", 404); isJSONError(body) {
			t.Errorf("unknown /me path %s: %s", p, body)
		}
	}
	if body, _ := c.meGet("/api/v1/me/productions?status=Bogus", "umd_mel2", 400); !isJSONError(body) {
		t.Errorf("bad status: %s", body)
	}
}

// TestMeVisibility: each person gets through the API exactly the
// productions the web pages show them, and no more.
func TestMeVisibility(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f)

	// Local users with passwords for the web side; the tokens link to
	// them by username, as single sign-on does.
	annID, _ := st.CreateUser("ann", "", "a long password", false)
	st.CreateUser("bob", "", "a long password", false)
	st.CreateUser("boss", "", "a long password", true)
	f.give("umd_ann", "s-ann", "ann", "editor")
	f.give("umd_bob", "s-bob", "bob", "editor")
	f.give("umd_boss", "s-boss", "boss", "admin")

	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format(store.DateLayout) }
	open := &store.Production{Title: "Christmas Pageant", Kind: "play", Status: "rehearsing", Venue: "Sanctuary",
		Director: "Jo Lee", Notes: "Costumes by Dec 1."}
	openID, _ := st.CreateProduction(open)
	st.CreateEvent(&store.Event{ProductionID: openID, Kind: "rehearsal", Date: day(-3), Time: "18:30", EndTime: "20:00"}, nil)
	st.CreateEvent(&store.Event{ProductionID: openID, Kind: "performance", Date: day(30), Time: "17:00", EndTime: "18:00",
		Tentative: true, Notes: "Pencilled in"}, []store.Room{{PlaceName: "Sanctuary"}})
	st.CreateEvent(&store.Event{ProductionID: openID, Kind: "rehearsal", Date: day(4), Time: "18:30", EndTime: "20:00", Label: "Act 1"},
		[]store.Room{{PlaceName: "Sanctuary"}, {PlaceName: "Fellowship Hall"}})
	st.CreateCastMember(&store.CastMember{ProductionID: openID, Character: "Narrator", Person: "Pat Smith"})

	lockedID, _ := st.CreateProduction(&store.Production{Title: "Youth Play", Status: "planning", Locked: true, Notes: "Secret note"})
	st.CreateCastMember(&store.CastMember{ProductionID: lockedID, Character: "Mary", Person: "Hidden Person"})
	if err := st.SetMembers(lockedID, []int64{annID}); err != nil {
		t.Fatal(err)
	}
	st.CreateProduction(&store.Production{Title: "Old Show", Status: "done"})

	// The same database served with the local login, for the web pages.
	_, web := serverOn(t, st, Config{})
	titles := []string{"Christmas Pageant", "Youth Play", "Old Show"}

	for _, who := range []string{"ann", "bob", "boss"} {
		page := web.as(who).get("/productions", 200)
		body, _ := c.meGet("/api/v1/me/productions", "umd_"+who, 200)
		var out struct {
			Productions []meProductionJSON `json:"productions"`
		}
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, p := range out.Productions {
			got = append(got, p.Title)
		}
		for _, title := range titles {
			if strings.Contains(page, title) != slices.Contains(got, title) {
				t.Errorf("%s: %q on web %v, in API %v", who, title, strings.Contains(page, title), got)
			}
		}
		// The detail follows the production page.
		webStatus := 200
		if who == "bob" {
			webStatus = 404
		}
		web.as(who).get("/productions/"+itoa(lockedID), webStatus)
		body, _ = c.meGet("/api/v1/me/productions/"+itoa(lockedID), "umd_"+who, webStatus)
		if webStatus == 404 {
			if !isJSONError(body) || strings.Contains(body, "Youth") || strings.Contains(body, "Secret") || strings.Contains(body, "Hidden") {
				t.Errorf("bob's locked detail: %s", body)
			}
		} else if !strings.Contains(body, "Hidden Person") {
			t.Errorf("%s should see the locked production's cast: %s", who, body)
		}
		if strings.Contains(body, "Secret") != (webStatus == 200) {
			t.Errorf("%s: notes shown = %v", who, strings.Contains(body, "Secret"))
		}
	}
	if body, _ := c.meGet("/api/v1/me/productions", "umd_bob", 200); strings.Contains(body, "Youth Play") {
		t.Error("bob's list names the locked production")
	}
	// A missing production answers exactly like a hidden one.
	if body, _ := c.meGet("/api/v1/me/productions/9999", "umd_bob", 404); !isJSONError(body) {
		t.Errorf("missing: %s", body)
	}
	hidden, _ := c.meGet("/api/v1/me/productions/"+itoa(lockedID), "umd_bob", 404)
	missing, _ := c.meGet("/api/v1/me/productions/9999", "umd_bob", 404)
	if hidden != missing {
		t.Errorf("hidden %q and missing %q differ", hidden, missing)
	}

	// The status filter.
	body, _ := c.meGet("/api/v1/me/productions?status=done", "umd_ann", 200)
	if !strings.Contains(body, "Old Show") || strings.Contains(body, "Pageant") {
		t.Errorf("status=done: %s", body)
	}
	if body, _ := c.meGet("/api/v1/me/productions?status=cancelled", "umd_ann", 200); strings.TrimSpace(body) != `{"productions":[]}` {
		t.Errorf("empty list should be []: %s", body)
	}
}

// TestMeShapes: the JSON field names and values are the contract's
// (APP_CONTRACTS.md, Production planner).
func TestMeShapes(t *testing.T) {
	f := newFakeTokenUM(t)
	_, st, c := meServer(t, f)
	f.give("umd_ann", "s-ann", "ann", "editor")
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format(store.DateLayout) }
	id, _ := st.CreateProduction(&store.Production{Title: "Christmas Pageant", Kind: "play", Status: "rehearsing",
		Venue: "Sanctuary", Director: "Jo Lee", Notes: "Costumes by Dec 1."})
	st.CreateEvent(&store.Event{ProductionID: id, Kind: "performance", Date: day(30), Time: "17:00", EndTime: "18:00",
		Tentative: true, Notes: "Pencilled in"}, []store.Room{{PlaceName: "Sanctuary"}})
	st.CreateEvent(&store.Event{ProductionID: id, Kind: "rehearsal", Date: day(4), Time: "18:30", EndTime: "20:00"},
		[]store.Room{{PlaceName: "Sanctuary"}})
	st.CreateEvent(&store.Event{ProductionID: id, Kind: "tech", Date: day(-2)}, nil)
	st.CreateCastMember(&store.CastMember{ProductionID: id, Character: "Narrator", Person: "Pat Smith"})
	emptyID, _ := st.CreateProduction(&store.Production{Title: "Empty", Status: "planning"})

	var list struct {
		Productions []map[string]json.RawMessage `json:"productions"`
	}
	body, _ := c.meGet("/api/v1/me/productions?status=rehearsing", "umd_ann", 200)
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Productions) != 1 {
		t.Fatalf("list: %s", body)
	}
	for _, k := range []string{"id", "title", "kind", "status", "next_date"} {
		if _, ok := list.Productions[0][k]; !ok {
			t.Errorf("list item has no %q: %s", k, body)
		}
	}
	wantNext := `{"kind":"rehearsal","starts_on":"` + day(4) + `","start_time":"18:30","end_time":"20:00","tentative":false,"rooms":["Sanctuary"],"notes":""}`
	if got := string(list.Productions[0]["next_date"]); got != wantNext {
		t.Errorf("next_date = %s\nwant %s", got, wantNext)
	}
	if got := string(list.Productions[0]["status"]); got != `"Rehearsing"` {
		t.Errorf("status = %s", got)
	}

	body, _ = c.meGet("/api/v1/me/productions/"+itoa(id), "umd_ann", 200)
	var d struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		Where    string `json:"where"`
		Director string `json:"director"`
		Notes    string `json:"notes"`
		NextDate *struct {
			StartsOn string `json:"starts_on"`
		} `json:"next_date"`
		Schedule []struct {
			Kind      string   `json:"kind"`
			StartsOn  string   `json:"starts_on"`
			StartTime string   `json:"start_time"`
			EndTime   string   `json:"end_time"`
			Tentative bool     `json:"tentative"`
			Rooms     []string `json:"rooms"`
			Notes     string   `json:"notes"`
		} `json:"schedule"`
		Cast []struct {
			Name string `json:"name"`
			Part string `json:"part"`
		} `json:"cast"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != id || d.Title != "Christmas Pageant" || d.Kind != "play" || d.Status != "Rehearsing" || d.Where != "Sanctuary" ||
		d.Director != "Jo Lee" || d.Notes != "Costumes by Dec 1." || d.NextDate == nil || d.NextDate.StartsOn != day(4) {
		t.Errorf("detail = %+v", d)
	}
	if len(d.Schedule) != 3 || d.Schedule[0].Kind != "tech" || d.Schedule[1].Kind != "rehearsal" || d.Schedule[2].Kind != "performance" {
		t.Fatalf("schedule not in date order: %+v", d.Schedule)
	}
	if s := d.Schedule[0]; s.StartTime != "" || s.EndTime != "" || s.Rooms == nil || len(s.Rooms) != 0 {
		t.Errorf("all-day date with no rooms: %+v", s)
	}
	if s := d.Schedule[2]; s.StartsOn != day(30) || s.StartTime != "17:00" || s.EndTime != "18:00" || !s.Tentative || s.Notes != "Pencilled in" {
		t.Errorf("performance: %+v", s)
	}
	if len(d.Cast) != 1 || d.Cast[0].Name != "Pat Smith" || d.Cast[0].Part != "Narrator" {
		t.Errorf("cast = %+v", d.Cast)
	}

	// No dates, no cast: [] (never null), next_date null.
	body, _ = c.meGet("/api/v1/me/productions/"+itoa(emptyID), "umd_ann", 200)
	for _, want := range []string{`"schedule":[]`, `"cast":[]`, `"next_date":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty production lacks %s: %s", want, body)
		}
	}
}

// TestMeScheduleWindow: a long schedule is cut to meMaxItems, oldest past
// dates first, so no upcoming date is lost.
func TestMeScheduleWindow(t *testing.T) {
	var ev []store.Event
	for i := range 150 {
		ev = append(ev, store.Event{ID: int64(i), Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(store.DateLayout)})
	}
	today := ev[120].Date
	w := meWindow(ev, today)
	if len(w) != meMaxItems || w[len(w)-1].ID != 149 || w[0].ID != 50 {
		t.Errorf("window %d..%d (%d)", w[0].ID, w[len(w)-1].ID, len(w))
	}
	w = meWindow(ev, "2025-01-01")
	if len(w) != meMaxItems || w[0].ID != 0 {
		t.Errorf("all upcoming: starts at %d", w[0].ID)
	}
	if len(meWindow(ev[:10], today)) != 10 {
		t.Error("short schedules are whole")
	}
}
