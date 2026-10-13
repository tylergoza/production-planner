package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tylergoza/production-planner/internal/store"
)

func TestCalendarBookingTentativeStatus(t *testing.T) {
	srv, _ := newConnServer(t, nil)
	p := &store.Production{ID: 3, Title: "Musical", Status: "rehearsing"}
	firm := &store.Event{ID: 1, ProductionID: 3, Kind: "rehearsal", Date: "2026-12-01"}
	pencil := &store.Event{ID: 2, ProductionID: 3, Kind: "rehearsal", Date: "2026-12-02", Tentative: true}
	if b := srv.calendarBooking(firm, p, nil); b.Status != "confirmed" {
		t.Errorf("firm date: %q", b.Status)
	}
	if b := srv.calendarBooking(pencil, p, nil); b.Status != "tentative" {
		t.Errorf("pencilled date: %q", b.Status)
	}
	p.Status = "cancelled"
	if b := srv.calendarBooking(pencil, p, nil); b.Status != "cancelled" {
		t.Errorf("cancelled production with pencilled date: %q", b.Status)
	}
}

func TestConfirmDatesOverHTTP(t *testing.T) {
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", false)
	pat := c.as("pat")
	a, _ := st.CreateProduction(&store.Production{Title: "Musical"})
	b, _ := st.CreateProduction(&store.Production{Title: "Drama"})
	mk := func(pid int64, date string, tent bool) int64 {
		id, err := st.CreateEvent(&store.Event{ProductionID: pid, Kind: "rehearsal", Date: date, Tentative: tent}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	e1, e2, e3, other := mk(a, "2099-01-01", true), mk(a, "2099-01-02", true), mk(a, "2099-01-03", true), mk(b, "2099-01-04", true)

	body := pat.get("/productions/1/schedule", 200)
	if !strings.Contains(body, "Pencilled in") || !strings.Contains(body, "Confirm all pencilled-in dates") {
		t.Fatal("expected badge and confirm-all button")
	}
	pat.post("/productions/1/schedule", "/events/"+itoa(e1)+"/confirm", url.Values{}, 200)
	if e, _ := st.GetEvent(e1); e.Tentative {
		t.Error("date should be confirmed")
	}
	pat.post("/productions/1/schedule", "/productions/1/events/confirm", url.Values{}, 200)
	for _, id := range []int64{e2, e3} {
		if e, _ := st.GetEvent(id); e.Tentative {
			t.Errorf("date %d should be confirmed", id)
		}
	}
	if e, _ := st.GetEvent(other); !e.Tentative {
		t.Error("another production's date must stay pencilled in")
	}
	if body := pat.get("/productions/1/schedule", 200); strings.Contains(body, "Confirm all pencilled-in dates") {
		t.Error("button should be gone")
	}

	// The form's checkbox saves it.
	pat.post("/productions/1/schedule", "/productions/1/events", url.Values{"kind": {"rehearsal"}, "date": {"2099-02-01"}, "tentative": {"1"}}, 200)
	evs, _ := st.ListEvents(a)
	if last := evs[len(evs)-1]; !last.Tentative {
		t.Error("checkbox should save as pencilled in")
	}
}

func TestClashShowsPencilledIn(t *testing.T) {
	cal := newFakeCalendar(t)
	c, st := newTestServer(t)
	st.CreateUser("pat", "", "a long password", false)
	pat := c.as("pat")
	hall := []store.Room{{PlaceID: 41, PlaceName: "Lifecenter"}}
	a, _ := st.CreateProduction(&store.Production{Title: "Musical"})
	st.CreateEvent(&store.Event{ProductionID: a, Kind: "dress", Date: "2099-12-01", Time: "18:00", EndTime: "20:00"}, hall)
	cal.busy = `{"busy": [{"id": 7, "source": "events", "title": "Fall Dinner", "starts_on": "2099-12-01",
		"start_time": "19:30", "end_time": "21:00", "rooms": ["Lifecenter"], "status": "tentative"},
		{"id": 8, "source": "calendar", "starts_on": "2099-12-01", "start_time": "18:30", "end_time": "19:00", "rooms": ["Lifecenter"], "busy": true, "status": "pending"}]}`
	srv, err := New(Config{Env: map[string]string{"CALENDAR_URL": cal.srv.URL, "CALENDAR_TOKEN": "cal_test"}}, st, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	withCal := &client{t: t, base: ts.URL, http: pat.http}
	body := withCal.get("/productions/1/schedule", 200)
	if !strings.Contains(body, "Fall Dinner (7:30–9:00 pm, Event planner, pencilled in)") {
		t.Errorf("expected pencilled in label:\n%s", body)
	}
	if !strings.Contains(body, "pending approval") {
		t.Error("expected pending approval label on the busy block")
	}
}
