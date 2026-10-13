package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seen struct {
	method, path, query, auth string
	body                      map[string]any
}

func fakeCalendar(t *testing.T, got *[]seen) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := seen{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &s.body)
		}
		*got = append(*got, s)
		w.Header().Set("Content-Type", "application/json")
		if s.auth != "Bearer cal_good" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"bad token"}`))
			return
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/bookings/productions/12":
			w.Write([]byte(`{"id": 40, "conflicts": [{"id": 7, "source": "events", "starts_on": "2026-11-07",
				"start_time": "18:00", "end_time": "21:00", "rooms": ["Lifecenter"], "busy": true}]}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"starts_on: not a date"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/bookings/productions/12":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`))
		case r.URL.Path == "/api/v1/availability":
			w.Write([]byte(`{"busy": [{"id": 3, "source": "calendar", "title": "Choir", "starts_on": "2026-11-07",
				"start_time": "14:00", "end_time": "16:00", "rooms": ["Kitchen"]}]}`))
		case r.URL.Path == "/api/v1/rooms":
			w.Write([]byte(`{"rooms": []}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPutBooking(t *testing.T) {
	var got []seen
	srv := fakeCalendar(t, &got)
	c := New("https://calendar.example.org/", srv.URL, "cal_good")
	res, err := c.PutBooking(context.Background(), 12, Booking{
		Title: "Fall Dinner", StartsOn: "2026-11-07", StartTime: "17:30",
		Rooms: []Room{{PlaceID: 41, Name: "Lifecenter"}, {Name: "Pavilion"}}, Visibility: "public", Status: "confirmed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != 40 || len(res.Conflicts) != 1 || !res.Conflicts[0].Busy || res.Conflicts[0].Ours() {
		t.Fatalf("result %+v", res)
	}
	s := got[0]
	if s.auth != "Bearer cal_good" || s.body["title"] != "Fall Dinner" {
		t.Fatalf("sent %+v", s)
	}
	rooms := s.body["rooms"].([]any)
	if len(rooms) != 2 || rooms[0].(map[string]any)["place_id"] != 41.0 {
		t.Fatalf("rooms %v", rooms)
	}
	if _, ok := rooms[1].(map[string]any)["place_id"]; ok {
		t.Fatal("place_id sent for a room typed in by hand")
	}
	if _, ok := s.body["replaces"]; ok {
		t.Fatal("replaces sent when 0")
	}
	if c.BookingURL(12) != "https://calendar.example.org/calendar/by-source/productions/12" {
		t.Fatalf("booking url %s", c.BookingURL(12))
	}
}

func TestPutBookingErrors(t *testing.T) {
	var got []seen
	srv := fakeCalendar(t, &got)
	_, err := New(srv.URL, "", "cal_good").PutBooking(context.Background(), 13, Booking{})
	var e *Error
	if !errors.As(err, &e) || e.Status != 400 || !strings.Contains(err.Error(), "starts_on") {
		t.Fatalf("err = %v", err)
	}
	if err := New(srv.URL, "", "cal_bad").Check(context.Background()); err == nil || !strings.Contains(err.Error(), "turned down the token") {
		t.Fatalf("bad token err = %v", err)
	}
	if err := New("http://127.0.0.1:1", "", "cal_good").Check(context.Background()); err == nil || !strings.Contains(err.Error(), "couldn't reach") {
		t.Fatalf("unreachable err = %v", err)
	}
	if _, err := New("", "", "").PutBooking(context.Background(), 1, Booking{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured err = %v", err)
	}
}

func TestDeleteBooking(t *testing.T) {
	var got []seen
	srv := fakeCalendar(t, &got)
	c := New(srv.URL, "", "cal_good")
	if err := c.DeleteBooking(context.Background(), 12); err != nil {
		t.Fatal(err)
	}
	// Already gone is fine.
	if err := c.DeleteBooking(context.Background(), 99); err != nil {
		t.Fatal(err)
	}
	if got[0].method != http.MethodDelete || got[0].path != "/api/v1/bookings/productions/12" {
		t.Fatalf("sent %+v", got[0])
	}
}

func TestAvailabilityAndCheck(t *testing.T) {
	var got []seen
	srv := fakeCalendar(t, &got)
	c := New(srv.URL, "", "cal_good")
	busy, err := c.Availability(context.Background(), []string{"Lifecenter", "Kitchen"}, "2026-11-07", "2026-11-07")
	if err != nil || len(busy) != 1 || busy[0].Title != "Choir" {
		t.Fatalf("busy %+v, %v", busy, err)
	}
	if got[0].query != "from=2026-11-07&rooms=Lifecenter%2CKitchen&to=2026-11-07" {
		t.Fatalf("query %s", got[0].query)
	}
	if err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}
