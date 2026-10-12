package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tylergoza/production-planner/internal/calendar"
	"github.com/tylergoza/production-planner/internal/store"
)

// Sending ---------------------------------------------------------------------

func TestCalendarBooking(t *testing.T) {
	srv, _ := newConnServer(t, nil)
	srv.cfg.BaseURL = "https://plan.example.org"
	rooms := []store.Room{{PlaceID: 41, PlaceName: "Lifecenter"}, {PlaceName: "lifecenter"}, {PlaceName: "Kitchen"}}
	p := &store.Production{ID: 3, Title: "Christmas Musical", Description: "Songs and the story", Public: true, Status: "rehearsing"}

	dress := &store.Event{ID: 9, ProductionID: 3, Kind: "dress", Date: "2026-12-12", Time: "17:00", EndTime: "19:30"}
	b := srv.calendarBooking(dress, p, rooms)
	if b.Title != "Christmas Musical: Dress rehearsal" || b.Kind != "Dress rehearsal" || b.Visibility != "private" || b.Description != "" {
		t.Errorf("dress rehearsal of a public production: %+v", b)
	}
	if b.StartTime != "17:00" || b.EndTime != "19:30" || b.EndsOn != "" || b.Status != "confirmed" {
		t.Errorf("times: %+v", b)
	}
	if len(b.Rooms) != 2 || b.Rooms[0].PlaceID != 41 || b.URL != "https://plan.example.org/productions/3/schedule" {
		t.Errorf("rooms and url: %+v", b)
	}

	show := &store.Event{ID: 10, ProductionID: 3, Kind: "performance", Date: "2026-12-14", Time: "22:30", Label: "Late show"}
	b = srv.calendarBooking(show, p, nil)
	if b.Title != "Christmas Musical: Late show" || b.Visibility != "public" || b.Description != "Songs and the story" {
		t.Errorf("performance of a public production: %+v", b)
	}
	// No end time: three hours, past midnight here.
	if b.EndTime != "01:30" || b.EndsOn != "2026-12-15" {
		t.Errorf("until done: %+v", b)
	}
	allDay := &store.Event{ID: 11, ProductionID: 3, Kind: "other", Date: "2026-12-01"}
	if b = srv.calendarBooking(allDay, p, nil); b.StartTime != "" || b.EndTime != "" || b.EndsOn != "" {
		t.Errorf("all day: %+v", b)
	}

	p.Public = false
	if b = srv.calendarBooking(show, p, nil); b.Visibility != "private" || b.Description != "" {
		t.Errorf("performance of a private production: %+v", b)
	}

	p.Locked, p.Public = true, true
	b = srv.calendarBooking(show, p, rooms)
	if b.Title != "" || b.Kind != "" || b.Description != "" || b.Visibility != "locked" || len(b.Rooms) != 2 {
		t.Errorf("locked production: %+v", b)
	}
	p.Locked, p.Status = false, "cancelled"
	if b = srv.calendarBooking(show, p, nil); b.Status != "cancelled" {
		t.Errorf("cancelled production: %+v", b)
	}
}

// fakeCalendar records what it's sent and answers every PUT with conflicts.
type fakeCalendar struct {
	mu   sync.Mutex
	sent []string // "PUT /api/v1/bookings/productions/1 <title>"
	srv  *httptest.Server
	busy string // JSON for /availability
}

func newFakeCalendar(t *testing.T) *fakeCalendar {
	f := &fakeCalendar{busy: `{"busy": []}`}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer cal_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/availability":
			io.WriteString(w, f.busy)
		case r.Method == http.MethodPut:
			var b calendar.Booking
			json.NewDecoder(r.Body).Decode(&b)
			f.sent = append(f.sent, r.Method+" "+r.URL.Path+" "+b.Title)
			io.WriteString(w, `{"id": 1, "conflicts": [{"id": 5, "source": "events", "title": "Fall Dinner",
				"starts_on": "2099-12-01", "start_time": "18:00", "end_time": "21:00", "rooms": ["Lifecenter"]}]}`)
		case r.Method == http.MethodDelete:
			f.sent = append(f.sent, r.Method+" "+r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			io.WriteString(w, `{"rooms": []}`)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCalendar) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

func TestCalendarWorker(t *testing.T) {
	cal := newFakeCalendar(t)
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": cal.srv.URL, "CALENDAR_TOKEN": "cal_test"})
	ctx := context.Background()
	pid, _ := st.CreateProduction(&store.Production{Title: "Musical", Status: "rehearsing"})
	a := store.Event{ProductionID: pid, Kind: "rehearsal", Date: "2099-12-01", Time: "18:00"}
	aID, _ := st.CreateEvent(&a, []store.Room{{PlaceName: "Lifecenter"}})
	b := store.Event{ProductionID: pid, Kind: "performance", Date: "2099-12-05"}
	bID, _ := st.CreateEvent(&b, nil)

	srv.sendCalendarJobs(ctx)
	if got := cal.take(); len(got) != 2 || got[0] != "PUT /api/v1/bookings/productions/"+itoa(aID)+" Musical: Rehearsal" {
		t.Fatalf("first send: %v", got)
	}
	if status, _ := st.CalendarOutbox(); status.Waiting != 0 || status.LastSent == "" {
		t.Fatalf("outbox after sending: %+v", status)
	}
	// The calendar's clashes are kept for the date's page.
	if c := srv.storedClashes(aID); len(c) != 1 || c[0].Title != "Fall Dinner" || c[0].Source != "Event planner" {
		t.Fatalf("kept clashes: %+v", c)
	}

	// A new title goes out for every date.
	p, _ := st.GetProduction(pid)
	p.Title = "Christmas Musical"
	st.UpdateProduction(p)
	srv.sendCalendarJobs(ctx)
	got := cal.take()
	if len(got) != 2 || !strings.HasSuffix(got[0], "Christmas Musical: Rehearsal") || !strings.HasSuffix(got[1], "Christmas Musical: Performance") {
		t.Fatalf("after title change: %v", got)
	}

	// Locking it sends no titles at all.
	p.Locked = true
	st.UpdateProduction(p)
	srv.sendCalendarJobs(ctx)
	for _, s := range cal.take() {
		if strings.Contains(s, "Musical") {
			t.Fatalf("locked production sent a title: %s", s)
		}
	}

	st.DeleteEvent(bID)
	srv.sendCalendarJobs(ctx)
	if got := cal.take(); len(got) != 1 || got[0] != "DELETE /api/v1/bookings/productions/"+itoa(bID) {
		t.Fatalf("delete: %v", got)
	}
}

func TestCalendarWorkerBacksOff(t *testing.T) {
	srv, st := newConnServer(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_test"})
	pid, _ := st.CreateProduction(&store.Production{Title: "Musical"})
	e := store.Event{ProductionID: pid, Kind: "rehearsal", Date: "2099-12-01"}
	st.CreateEvent(&e, nil)
	srv.sendCalendarJobs(context.Background())
	status, _ := st.CalendarOutbox()
	if status.Waiting != 1 || !strings.Contains(status.LastError, "couldn't reach the calendar") {
		t.Fatalf("status %+v", status)
	}
	if due, _ := st.DueCalendarJobs(10); len(due) != 0 {
		t.Fatal("a failed date should wait before the next try")
	}
}

func TestCalendarUnsetChangesNothing(t *testing.T) {
	srv, st := newConnServer(t, nil)
	pid, _ := st.CreateProduction(&store.Production{Title: "Musical"})
	e := store.Event{ProductionID: pid, Kind: "rehearsal", Date: "2099-12-01"}
	st.CreateEvent(&e, nil)
	if srv.Calendar().Configured() {
		t.Fatal("calendar configured with nothing set")
	}
	srv.sendCalendarJobs(context.Background()) // must not panic or clear the row
	if status, _ := st.CalendarOutbox(); status.Waiting != 1 {
		t.Fatalf("status %+v", status)
	}
}

// Backfill ---------------------------------------------------------------------

func backfillServer(t *testing.T, env map[string]string) (*client, *store.Store) {
	t.Helper()
	c, st := newTestServerWith(t, Config{Env: env})
	st.CreateUser("boss", "", "a long password", true)
	st.CreateUser("ann", "", "a long password", false)
	return c.as("boss"), st
}

func seedBackfillDates(t *testing.T, st *store.Store) {
	t.Helper()
	now := time.Now()
	pid, _ := st.CreateProduction(&store.Production{Title: "Musical"})
	for _, d := range []time.Time{now.AddDate(-2, 0, 0), now.AddDate(0, -3, 0), now.AddDate(0, 1, 0)} {
		e := store.Event{ProductionID: pid, Kind: "rehearsal", Date: d.Format(store.DateLayout)}
		if _, err := st.CreateEvent(&e, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Saving queues rows by itself; start from an empty outbox.
	if _, err := st.DB.Exec(`DELETE FROM calendar_outbox`); err != nil {
		t.Fatal(err)
	}
}

func outboxCount(t *testing.T, st *store.Store) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCalendarBackfillButton(t *testing.T) {
	admin, st := backfillServer(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_test"})
	seedBackfillDates(t, st)

	page := admin.get("/admin/connections", 200)
	if !strings.Contains(page, "Send 2 dates from") || !strings.Contains(page, "Waiting to send") {
		t.Fatalf("expected the button and status on the page:\n%s", page)
	}
	admin.post("/admin/connections", "/admin/connections/calendar/backfill", url.Values{}, 200)
	if got := outboxCount(t, st); got != 2 {
		t.Fatalf("outbox has %d rows, want 2", got)
	}
	var old int
	st.DB.QueryRow(`SELECT COUNT(*) FROM calendar_outbox WHERE event_id = 1`).Scan(&old)
	if old != 0 {
		t.Error("a date from two years ago was queued")
	}
	// Again: still one row per date.
	if body := admin.post("/admin/connections", "/admin/connections/calendar/backfill", url.Values{}, 200); !strings.Contains(body, "Queued 2 dates") {
		t.Errorf("expected the flash, got:\n%s", body)
	}
	if got := outboxCount(t, st); got != 2 {
		t.Fatalf("after pressing twice: %d rows, want 2", got)
	}
	var status struct {
		Waiting  int    `json:"waiting"`
		LastSent string `json:"last_sent"`
	}
	if err := json.Unmarshal([]byte(admin.get("/admin/connections/calendar/status", 200)), &status); err != nil {
		t.Fatal(err)
	}
	if status.Waiting != 2 || status.LastSent != "never" {
		t.Errorf("status = %+v", status)
	}
}

func TestCalendarBackfillAdminsOnly(t *testing.T) {
	admin, st := backfillServer(t, map[string]string{"CALENDAR_URL": "http://127.0.0.1:1", "CALENDAR_TOKEN": "cal_test"})
	seedBackfillDates(t, st)
	ann := admin.as("ann")
	ann.get("/admin/connections/calendar/status", 403)
	ann.post("/account", "/admin/connections/calendar/backfill", url.Values{}, 403)
	if outboxCount(t, st) != 0 {
		t.Error("a non-admin queued dates")
	}
}

func TestCalendarBackfillNotConnected(t *testing.T) {
	admin, st := backfillServer(t, nil)
	seedBackfillDates(t, st)
	if page := admin.get("/admin/connections", 200); strings.Contains(page, "Send all dates") {
		t.Error("button shown without a calendar")
	}
	if body := admin.post("/admin/connections", "/admin/connections/calendar/backfill", url.Values{}, 200); !strings.Contains(body, "connected, so there") {
		t.Errorf("expected an error flash:\n%s", body)
	}
	if outboxCount(t, st) != 0 {
		t.Error("dates queued with no calendar")
	}
}

// Rooms and the schedule ----------------------------------------------------

func TestScheduleRoomsAndEndTime(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", false)
	pat := c.as("pat")
	pat.post("/productions/new", "/productions", url.Values{"title": {"Musical"}, "status": {"planning"}, "public": {"1"}}, 200)
	if p, _ := st.GetProduction(1); !p.Public {
		t.Fatal("public not saved")
	}
	if body := pat.get("/productions/1/schedule", 200); !strings.Contains(body, "Pick its rooms") {
		t.Error("schedule should point to picking the production's rooms")
	}
	pat.post("/productions/1/edit", "/productions/1/rooms", url.Values{"place_name": {"Sanctuary"}}, 200)
	pat.post("/productions/1/edit", "/productions/1/rooms", url.Values{"place_name": {"Choir room"}}, 200)
	if body := pat.post("/productions/1/edit", "/productions/1/rooms", url.Values{"place_name": {"sanctuary"}}, 200); !strings.Contains(body, "already one of this production") {
		t.Error("same room twice should be refused")
	}

	// A new date starts with the production's rooms ticked.
	body := pat.get("/productions/1/schedule", 200)
	if !strings.Contains(body, `value="0|Sanctuary" checked`) || !strings.Contains(body, `value="0|Choir room" checked`) {
		t.Fatalf("rooms not ticked by default:\n%s", body)
	}
	// End time is optional...
	pat.post("/productions/1/schedule", "/productions/1/events", url.Values{
		"kind": {"rehearsal"}, "date": {"2099-12-01"}, "time": {"18:00"}, "room": {"0|Sanctuary", "0|Choir room"},
	}, 200)
	rooms, _ := st.EventRooms(1)
	if len(rooms) != 2 || rooms[0].PlaceName != "Sanctuary" {
		t.Fatalf("date rooms %+v", rooms)
	}
	// ...but has to come after the start.
	if body := pat.post("/productions/1/schedule", "/productions/1/events", url.Values{
		"kind": {"rehearsal"}, "date": {"2099-12-02"}, "time": {"18:00"}, "end_time": {"17:00"}, "room": {"0|Sanctuary"},
	}, 422); !strings.Contains(body, "end time has to be after") || !strings.Contains(body, `value="0|Sanctuary" checked`) || strings.Contains(body, `value="0|Choir room" checked`) {
		t.Error("expected the end time error with the ticked rooms kept")
	}
	// Rooms not offered are ignored; one typed in is kept.
	pat.post("/events/1/edit", "/events/1", url.Values{
		"kind": {"rehearsal"}, "date": {"2099-12-01"}, "time": {"18:00"}, "end_time": {"20:00"},
		"room": {"0|Sanctuary", "99|Somewhere"}, "room_other": {"Fellowship hall"},
	}, 200)
	if rooms, _ = st.EventRooms(1); len(rooms) != 2 || rooms[1].PlaceName != "Fellowship hall" {
		t.Fatalf("date rooms after edit %+v", rooms)
	}
	e, _ := st.GetEvent(1)
	if e.EndTime != "20:00" {
		t.Errorf("end time %q", e.EndTime)
	}
	// The edit page offers the typed room again, ticked.
	if body := pat.get("/events/1/edit", 200); !strings.Contains(body, `value="0|Fellowship hall" checked`) || strings.Contains(body, `value="0|Choir room" checked`) {
		t.Error("edit page should tick exactly the date's rooms")
	}
}

func TestScheduleClashes(t *testing.T) {
	cal := newFakeCalendar(t)
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", false)
	pat := c.as("pat")
	hall := []store.Room{{PlaceID: 41, PlaceName: "Lifecenter"}}
	a, _ := st.CreateProduction(&store.Production{Title: "Musical"})
	b, _ := st.CreateProduction(&store.Production{Title: "Drama"})
	mine := store.Event{ProductionID: a, Kind: "dress", Date: "2099-12-01", Time: "18:00", EndTime: "20:00"}
	st.CreateEvent(&mine, hall)
	other := store.Event{ProductionID: b, Kind: "rehearsal", Date: "2099-12-01", Time: "19:00"}
	st.CreateEvent(&other, hall)

	// Without the calendar: this app's own check.
	if body := pat.get("/productions/1/schedule", 200); !strings.Contains(body, "Double-booked") || !strings.Contains(body, "Drama: Rehearsal") {
		t.Fatal("expected the same-app clash")
	}

	// With it: the calendar's answer, from every app.
	cal.busy = `{"busy": [{"id": 7, "source": "events", "title": "Fall Dinner", "starts_on": "2099-12-01",
		"start_time": "19:30", "end_time": "21:00", "rooms": ["Lifecenter"]},
		{"id": 8, "source": "calendar", "starts_on": "2099-12-01", "start_time": "06:00", "end_time": "07:00", "rooms": ["Lifecenter"], "busy": true},
		{"id": 9, "source": "productions", "source_id": "1", "title": "Musical: Dress rehearsal", "starts_on": "2099-12-01",
		"start_time": "18:00", "end_time": "20:00", "rooms": ["Lifecenter"]}]}`
	srv, err := New(Config{Env: map[string]string{"CALENDAR_URL": cal.srv.URL, "CALENDAR_TOKEN": "cal_test"}}, st, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	withCal := &client{t: t, base: ts.URL, http: pat.http}
	body := withCal.get("/productions/1/schedule", 200)
	if !strings.Contains(body, "Also booked on the calendar") || !strings.Contains(body, "Fall Dinner (7:30–9:00 pm, Event planner)") {
		t.Fatalf("expected the calendar's clash:\n%s", body)
	}
	if strings.Contains(body, "Booked ·") || strings.Contains(body, "Musical: Dress rehearsal (") || strings.Contains(body, "Double-booked") {
		t.Error("its own booking, other times and the same-app check should be left out")
	}

	// Calendar down: back to this app's check, and it says so.
	cal.srv.Close()
	body = withCal.get("/productions/1/schedule", 200)
	if !strings.Contains(body, "Couldn't reach the calendar") || !strings.Contains(body, "Double-booked") {
		t.Error("expected the fallback")
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
