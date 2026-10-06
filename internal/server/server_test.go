package server

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newTestServer(t *testing.T) (*client, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := New(Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}, st
}

func (c *client) get(path string, wantStatus int) string {
	c.t.Helper()
	resp, err := c.http.Get(c.base + path)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("GET %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

var csrfRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// post submits a form, pulling the CSRF token from a page first.
func (c *client) post(tokenPage, path string, form url.Values, wantStatus int) string {
	c.t.Helper()
	m := csrfRe.FindStringSubmatch(c.get(tokenPage, 200))
	if m == nil {
		c.t.Fatalf("no csrf token on %s", tokenPage)
	}
	form.Set("_csrf", m[1])
	resp, err := c.http.PostForm(c.base+path, form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		c.t.Fatalf("POST %s: status %d, want %d\n%s", path, resp.StatusCode, wantStatus, body)
	}
	return string(body)
}

// fakeTracker answers like the maintenance tracker's API: one product of
// two numbered lapel mics, folding chairs, and a supply of batteries.
func fakeTracker(t *testing.T) *httptest.Server {
	t.Helper()
	routes := map[string]string{
		"/api/v1/products?q=mic":    `{"products":[{"id":5,"name":"Lapel mics","category":"Audio","total":2,"item_count":1,"place_count":1}]}`,
		"/api/v1/products?q=chairs": `{"products":[{"id":9,"name":"Folding chairs","category":"Furniture","counted":true,"total":100,"item_count":2,"place_count":2}]}`,
		"/api/v1/products": `{"products":[{"id":5,"name":"Lapel mics","total":2,"item_count":1,"place_count":1},
			{"id":9,"name":"Folding chairs","counted":true,"total":100,"item_count":2,"place_count":2}]}`,
		"/api/v1/products/5":        `{"id":5,"name":"Lapel mics","total":2,"items":[{"id":7,"product_id":5,"name":"Lapel mics","quantity":2,"place":{"id":3,"path":"Sanctuary › Sound booth"}}]}`,
		"/api/v1/items/7":           `{"id":7,"product_id":5,"name":"Lapel mics","quantity":2,"place":{"id":3,"path":"Sanctuary › Sound booth"},"units":[{"number":1,"id":"L1"},{"number":2,"id":"L2"}]}`,
		"/api/v1/supplies?q=chairs": `{"supplies":[]}`,
		"/api/v1/supplies":          `{"supplies":[{"id":4,"name":"AA batteries","unit":"batteries","place":{"id":3,"path":"Sanctuary › Sound booth"},"on_hand":6,"total":6,"reorder_at":12,"stock":"low","requested":null}]}`,
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer mt_test" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"bad token"}`)
			return
		}
		body, ok := routes[r.URL.RequestURI()]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"not found"}`)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestEndToEnd(t *testing.T) {
	c, st := newTestServer(t)
	tracker := fakeTracker(t)

	// No users yet: everything leads to setup.
	if body := c.get("/", 200); !strings.Contains(body, "Create the first administrator") {
		t.Fatal("expected setup page")
	}
	c.post("/setup", "/setup", url.Values{
		"site_name": {"Grace Church Productions"}, "username": {"director"}, "display_name": {"Pat"},
		"password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)

	// Connect the tracker; saving checks it answers.
	if body := c.post("/admin/settings", "/admin/settings", url.Values{
		"site_name": {"Grace Church Productions"}, "tracker_url": {tracker.URL + "/"}, "tracker_token": {"mt_test"},
	}, 200); !strings.Contains(body, "Connected to the maintenance tracker") {
		t.Fatal("expected the tracker check to pass")
	}
	if body := c.post("/admin/settings", "/admin/settings", url.Values{"site_name": {"X"}, "tracker_token": {"nope"}}, 422); !strings.Contains(body, "start with mt_") {
		t.Error("expected token format error")
	}

	// A production, with dates, cast and scenes.
	c.post("/productions/new", "/productions", url.Values{"title": {"Christmas Musical"}, "status": {"planning"}}, 200)
	if body := c.post("/productions/new", "/productions", url.Values{"title": {""}, "status": {"planning"}}, 422); !strings.Contains(body, "Give the production a title") {
		t.Error("expected title error")
	}
	c.post("/productions/1/schedule", "/productions/1/events", url.Values{"kind": {"performance"}, "date": {"2099-12-20"}, "time": {"18:00"}}, 200)
	c.post("/productions/1/schedule", "/productions/1/events", url.Values{"kind": {"rehearsal"}, "date": {"2000-01-01"}}, 200)
	if body := c.post("/productions/1/schedule", "/productions/1/events", url.Values{"kind": {"rehearsal"}, "date": {"soon"}}, 422); !strings.Contains(body, "Pick a date") {
		t.Error("expected date error")
	}
	c.post("/productions/1/cast", "/productions/1/cast/paste", url.Values{"list": {"Mary - Ava\nJoseph\tEli\n\nNarrator"}}, 200)
	c.post("/productions/1/cast", "/productions/1/scenes/paste", url.Values{"list": {"1 - Field\n2 - Inn\n3 - Stable"}}, 200)
	cast, _ := st.ListCast(1)
	scenes, _ := st.ListScenes(1)
	if len(cast) != 3 || cast[1].Person != "Eli" || cast[2].Character != "Narrator" || len(scenes) != 3 || scenes[2].Title != "Stable" {
		t.Fatalf("pasted lists: %+v %+v", cast, scenes)
	}
	c.post("/productions/1/cast", "/scenes/3/move", url.Values{"dir": {"up"}, "next": {"/productions/1/cast"}}, 200)
	if scenes, _ = st.ListScenes(1); scenes[1].Title != "Stable" {
		t.Error("scene should have moved up")
	}
	c.post("/productions/1/cast", "/scenes/3/move", url.Values{"dir": {"down"}, "next": {"/productions/1/cast"}}, 200)
	scenes, _ = st.ListScenes(1)

	// Mics from the tracker, named by their sticker IDs, and one by hand.
	if body := c.get("/productions/1/mics/setup", 200); !strings.Contains(body, "Lapel mics") {
		t.Fatal("mic setup should search the tracker for mics")
	}
	c.post("/productions/1/mics/setup", "/productions/1/mics/import", url.Values{"product_id": {"5"}}, 200)
	c.post("/productions/1/mics/setup", "/productions/1/mics/import", url.Values{"product_id": {"5"}}, 200) // no duplicates
	c.post("/productions/1/mics/setup", "/productions/1/mics", url.Values{"name": {"Handheld A"}, "kind": {"handheld"}, "channel": {"9"}}, 200)
	mics, _ := st.ListMics(1)
	if len(mics) != 3 || mics[0].Name != "Lapel mics L1" || mics[0].TrackerUnit != "L1" || mics[0].Kind != "lav" || mics[2].Label() != "9 · Handheld A" {
		t.Fatalf("mics: %+v", mics)
	}

	// The chart: Mary keeps L1; L2 goes from Joseph to the Narrator.
	cell := func(m store.Mic, sc store.Scene) string { return "c_" + itoa(m.ID) + "_" + itoa(sc.ID) }
	form := url.Values{}
	for _, sc := range scenes {
		form.Set(cell(mics[0], sc), itoa(cast[0].ID))
	}
	form.Set(cell(mics[1], scenes[0]), itoa(cast[1].ID))
	form.Set(cell(mics[1], scenes[1]), itoa(cast[1].ID))
	form.Set(cell(mics[1], scenes[2]), itoa(cast[2].ID))
	c.post("/productions/1/mics/edit", "/productions/1/mics/chart", form, 200)
	body := c.get("/productions/1/mics", 200)
	if !strings.Contains(body, "Going into 3") || !strings.Contains(body, "from Joseph (Eli)") || !strings.Contains(body, "Draft") {
		t.Fatalf("chart should list the swap into scene 3 and be a draft:\n%s", body)
	}
	// A cast member who isn't in this production is refused.
	c.post("/productions/new", "/productions", url.Values{"title": {"Other"}, "status": {"planning"}}, 200)
	other, _ := st.CreateCastMember(&store.CastMember{ProductionID: 2, Person: "Stranger"})
	if body := c.post("/productions/1/mics/edit", "/productions/1/mics/chart", url.Values{cell(mics[0], scenes[0]): {itoa(other)}}, 200); !strings.Contains(body, "changed the cast") {
		t.Error("expected a refusal for a cast member from another production")
	}

	// Finalize, change, and see the change flagged.
	c.post("/productions/1/mics", "/productions/1/mics/finalize", url.Values{"note": {"After rehearsal"}}, 200)
	if body := c.get("/productions/1/mics", 200); !strings.Contains(body, "Final v1") {
		t.Fatal("expected Final v1")
	}
	if body := c.post("/productions/1/mics", "/productions/1/mics/finalize", url.Values{}, 200); !strings.Contains(body, "Nothing has changed") {
		t.Error("finalizing an unchanged chart should say so")
	}
	form.Set(cell(mics[2], scenes[2]), itoa(cast[1].ID))
	c.post("/productions/1/mics/edit", "/productions/1/mics/chart", form, 200)
	if body := c.get("/productions/1/mics", 200); !strings.Contains(body, "Changed since v1") || !strings.Contains(body, "1 cell changed") {
		t.Fatal("expected the change since v1 to be flagged")
	}
	c.post("/productions/1/mics", "/productions/1/mics/finalize", url.Values{}, 200)
	if body := c.get("/productions/1/mics/v/2", 200); !strings.Contains(body, "changed from version 1 (1 cell)") {
		t.Error("version 2 should show its change from version 1")
	}

	// Readiness.
	c.post("/productions/1/prep", "/productions/1/prep", url.Values{"department": {"props"}, "name": {"Manger"}, "scene_id": {itoa(scenes[2].ID)}}, 200)
	c.post("/productions/1/prep", "/productions/1/prep", url.Values{"department": {"costumes"}, "name": {"Robes"}, "due_on": {"2000-01-02"}}, 200)
	c.post("/productions/1/prep", "/prep/1/status", url.Values{"status": {"ready"}}, 200)
	if body := c.post("/productions/1/prep", "/productions/1/prep", url.Values{"department": {"bogus"}, "name": {"X"}}, 422); !strings.Contains(body, "Pick a department") {
		t.Error("expected department error")
	}
	if body := c.get("/productions/1", 200); !strings.Contains(body, "Robes") || !strings.Contains(body, "was due") {
		t.Error("overview should flag overdue readiness items")
	}

	// Needs: search the tracker and add from it, plus something to buy.
	if body := c.get("/productions/1/needs?q=chairs", 200); !strings.Contains(body, "Folding chairs") {
		t.Fatal("expected tracker search results")
	}
	c.post("/productions/1/needs", "/productions/1/needs", url.Values{"tracker_kind": {"product"}, "tracker_id": {"9"}, "name": {"Folding chairs"}, "quantity": {"150"}}, 200)
	c.post("/productions/1/needs", "/productions/1/needs", url.Values{"tracker_kind": {"supply"}, "tracker_id": {"4"}, "name": {"AA batteries"}, "quantity": {"4"}, "department": {"mics"}}, 200)
	c.post("/productions/1/needs", "/productions/1/needs", url.Values{"name": {"Hay bales"}, "quantity": {"3"}}, 200)
	body = c.get("/productions/1/needs", 200)
	if !strings.Contains(body, "Short by 50") || !strings.Contains(body, "6 batteries on hand") || !strings.Contains(body, "buy or borrow") {
		t.Fatalf("needs should show tracker availability:\n%s", body)
	}
	c.post("/productions/1/needs", "/needs/3/status", url.Values{"status": {"gathered"}}, 200)
	if n, _ := st.GetNeed(3); n.Status != "gathered" || n.UpdatedBy != "Pat" {
		t.Errorf("need status: %+v", n)
	}

	// Every signed-in page renders.
	for _, p := range []string{
		"/", "/productions", "/productions/new", "/productions/1", "/productions/1/edit", "/productions/1/schedule",
		"/events/1/edit", "/productions/1/cast", "/cast/1/edit", "/scenes/1/edit",
		"/productions/1/mics", "/productions/1/mics/edit", "/productions/1/mics/setup", "/productions/1/mics/setup?q=",
		"/productions/1/mics/v/1", "/mics/1/edit", "/productions/1/prep", "/prep/1/edit",
		"/productions/1/needs", "/needs/1/edit", "/productions/2", "/productions/2/mics", "/productions/2/needs", "/productions/2/prep",
		"/account", "/admin/users", "/admin/users/new", "/admin/users/1/edit", "/admin/settings", "/offline",
	} {
		c.get(p, 200)
	}
	c.get("/productions/99", 404)
	c.get("/productions/1/mics/v/9", 404)

	// Deleting a production takes everything in it.
	c.post("/productions/1/edit", "/productions/1/delete", url.Values{}, 200)
	if n, _ := st.ListNeeds(1); len(n) != 0 {
		t.Error("needs should go with the production")
	}
}

func TestTrackerDown(t *testing.T) {
	c, st := newTestServer(t)
	c.post("/setup", "/setup", url.Values{
		"site_name": {"P"}, "username": {"director"}, "password": {"a long password"}, "password_confirm": {"a long password"},
	}, 200)
	if body := c.post("/admin/settings", "/admin/settings", url.Values{
		"site_name": {"P"}, "tracker_url": {"http://127.0.0.1:1"}, "tracker_token": {"mt_x"},
	}, 200); !strings.Contains(body, "didn&#39;t answer") {
		t.Fatal("expected a warning that the tracker didn't answer")
	}
	c.post("/productions/new", "/productions", url.Values{"title": {"Show"}, "status": {"planning"}}, 200)
	st.CreateNeed(&store.Need{ProductionID: 1, Name: "Chairs", Quantity: 5, TrackerKind: "product", TrackerID: 9})
	// Pages still work, saying the tracker couldn't be checked.
	if body := c.get("/productions/1/needs", 200); !strings.Contains(body, "Couldn't check the maintenance tracker") {
		t.Error("needs page should say the tracker couldn't be reached")
	}
	c.get("/productions/1/mics/setup", 200)
}

func TestSignInRequired(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("someone", "", "a long password", true)
	for _, p := range []string{"/", "/productions", "/productions/1/mics"} {
		if body := c.get(p, 200); !strings.Contains(body, "Sign in to plan") {
			t.Errorf("%s should ask to sign in", p)
		}
	}
}

func TestLiveEvents(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("director", "", "a long password", true)

	// The stream is for signed-in people only.
	if body := c.get("/live", 200); !strings.Contains(body, "Sign in to plan") {
		t.Fatal("/live should ask to sign in")
	}
	c.post("/login", "/login", url.Values{"username": {"director"}, "password": {"a long password"}}, 200)

	resp, err := c.http.Get(c.base + "/live")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data:") {
				lines <- sc.Text()
			}
		}
	}()
	next := func(wait time.Duration) string {
		select {
		case l := <-lines:
			return l
		case <-time.After(wait):
			return ""
		}
	}
	if l := next(2 * time.Second); l != "data: ping" {
		t.Fatalf("first message %q, want a ping", l)
	}

	// Signing in changes nothing anyone else sees.
	c.post("/login", "/login", url.Values{"username": {"director"}, "password": {"a long password"}}, 200)
	if l := next(200 * time.Millisecond); l != "" {
		t.Fatalf("signing in sent %q", l)
	}
	// A failed save doesn't either.
	c.post("/productions/new", "/productions", url.Values{"title": {""}, "status": {"planning"}}, 422)
	if l := next(200 * time.Millisecond); l != "" {
		t.Fatalf("a rejected save sent %q", l)
	}
	c.post("/productions/new", "/productions", url.Values{"title": {"Easter Play"}, "status": {"planning"}}, 200)
	if l := next(2 * time.Second); l != "data: change" {
		t.Fatalf("after a save got %q, want a change", l)
	}

	for _, p := range []string{"/", "/productions/1", "/productions/1/schedule", "/productions/1/mics", "/productions/1/prep", "/productions/1/needs"} {
		if !strings.Contains(c.get(p, 200), `data-controller="live"`) {
			t.Errorf("%s should update live", p)
		}
	}
	for _, p := range []string{"/productions", "/productions/1/cast", "/productions/1/edit"} {
		if strings.Contains(c.get(p, 200), `data-controller="live"`) {
			t.Errorf("%s shouldn't update live", p)
		}
	}
	// A form sent back with errors would lose what was typed on a refresh.
	if body := c.post("/productions/1/prep", "/productions/1/prep", url.Values{"name": {""}}, 422); strings.Contains(body, `data-controller="live"`) {
		t.Error("a form with errors shouldn't update live")
	}
	c.get("/static/js/controllers/live_controller.js", 200)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// as signs in as someone else in a browser of their own.
func (c *client) as(username string) *client {
	jar, _ := cookiejar.New(nil)
	other := &client{t: c.t, base: c.base, http: &http.Client{Jar: jar}}
	other.post("/login", "/login", url.Values{"username": {username}, "password": {"a long password"}}, 200)
	return other
}

func TestLockedProductions(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("boss", "", "a long password", true)
	annID, _ := st.CreateUser("ann", "", "a long password", false)
	bobID, _ := st.CreateUser("bob", "", "a long password", false)
	ann, bob, boss := c.as("ann"), c.as("bob"), c.as("boss")

	// Only admins lock: Ann asking for it gets an open production.
	ann.post("/productions/new", "/productions", url.Values{"title": {"Open Concert"}, "status": {"planning"}, "access": {"locked"}}, 200)
	if p, _ := st.GetProduction(1); p.Locked {
		t.Fatal("a non-admin shouldn't be able to lock a production")
	}
	if strings.Contains(ann.get("/productions/1/edit", 200), "Who can see it") {
		t.Error("non-admins shouldn't get the access controls")
	}

	boss.post("/productions/new", "/productions", url.Values{"title": {"Youth Play"}, "status": {"planning"}, "access": {"locked"}, "member": {itoa(annID), "999"}}, 200)
	ann.post("/productions/2/schedule", "/productions/2/events", url.Values{"kind": {"rehearsal"}, "date": {"2099-01-01"}}, 200)
	ann.post("/productions/2/cast", "/productions/2/cast", url.Values{"character": {"Mary"}}, 200)
	if m, _ := st.Members(2); len(m) != 1 || m[0] != annID {
		t.Fatalf("members %v, want just ann", m)
	}

	// Bob doesn't see it anywhere, and can't reach it or anything in it.
	for _, p := range []string{"/", "/productions"} {
		body := bob.get(p, 200)
		if strings.Contains(body, "Youth Play") || !strings.Contains(body, "Open Concert") {
			t.Errorf("%s: bob should see only the open production", p)
		}
	}
	for _, p := range []string{"/productions/2", "/productions/2/edit", "/productions/2/mics", "/events/1/edit", "/cast/1/edit"} {
		bob.get(p, 404)
	}
	bob.post("/productions", "/productions/2/events", url.Values{"kind": {"rehearsal"}, "date": {"2099-01-02"}}, 404)
	bob.post("/productions", "/cast/1/move", url.Values{"dir": {"up"}}, 404)
	bob.post("/productions", "/productions/2/delete", url.Values{}, 404)

	// A member saving the details can't open it up or change who's on it.
	ann.post("/productions/2/edit", "/productions/2", url.Values{"title": {"Youth Play"}, "status": {"planning"}, "access": {"open"}, "member": {itoa(bobID)}}, 200)
	if p, _ := st.GetProduction(2); !p.Locked {
		t.Fatal("a non-admin shouldn't be able to unlock a production")
	}
	if m, _ := st.Members(2); len(m) != 1 || m[0] != annID {
		t.Fatalf("members %v, want just ann", m)
	}

	// Admins see everything, add Bob, then open it up, which keeps the list.
	if body := boss.get("/productions/2", 200); !strings.Contains(body, "Locked") {
		t.Error("admin should see the locked production, marked locked")
	}
	boss.post("/productions/2/edit", "/productions/2", url.Values{"title": {"Youth Play"}, "status": {"planning"}, "access": {"locked"}, "member": {itoa(annID), itoa(bobID)}}, 200)
	bob.get("/productions/2", 200)
	boss.post("/productions/2/edit", "/productions/2", url.Values{"title": {"Youth Play"}, "status": {"planning"}, "access": {"open"}}, 200)
	if m, _ := st.Members(2); len(m) != 2 {
		t.Errorf("members %v, want ann and bob kept", m)
	}
	if body := bob.get("/productions", 200); !strings.Contains(body, "Youth Play") {
		t.Error("an open production is for everyone")
	}
}
