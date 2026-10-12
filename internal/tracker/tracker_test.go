package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const fakePlaces = `{"places": [
  {"id": 1, "parent_id": null, "kind": "site", "number": "", "name": "Main Campus", "path": "Main Campus",
   "address": "", "notes": "", "tags": [], "also_in": []},
  {"id": 2, "parent_id": 1, "kind": "building", "number": "", "name": "Fellowship Hall", "path": "Main Campus › Fellowship Hall",
   "address": "", "notes": "", "tags": ["event"], "also_in": []},
  {"id": 3, "parent_id": 2, "kind": "floor", "number": "", "name": "Ground floor", "path": "Main Campus › Fellowship Hall › Ground floor",
   "address": "", "notes": "", "tags": [], "also_in": []},
  {"id": 4, "parent_id": 3, "kind": "room", "number": "104", "name": "Nursery", "path": "Main Campus › Fellowship Hall › Ground floor › 104 – Nursery",
   "address": "", "notes": "", "tags": [], "also_in": []},
  {"id": 5, "parent_id": 3, "kind": "area", "number": "", "name": "Kitchen Pass-through", "path": "Main Campus › Fellowship Hall › Ground floor › Kitchen Pass-through",
   "address": "", "notes": "", "tags": [], "also_in": [4]},
  {"id": 6, "parent_id": 1, "kind": "room", "number": "201", "name": "Sanctuary", "path": "Main Campus › 201 – Sanctuary",
   "address": "", "notes": "", "tags": ["event"], "also_in": []}
]}`

const fakePlace = `{"id": 4, "parent_id": 3, "kind": "room", "number": "104", "name": "Nursery",
 "path": "Main Campus › Fellowship Hall › Ground floor › 104 – Nursery",
 "address": "", "notes": "", "tags": [], "also_in": [],
 "children": [], "items": [{"id": 9, "name": "Crib"}], "supplies": []}`

// fakeTracker serves the places endpoints and records the Authorization
// header it was sent with.
func fakeTracker(t *testing.T, token string, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/places":
			if r.URL.RawQuery != "" {
				t.Errorf("Places sent unexpected query %q", r.URL.RawQuery)
			}
			w.Write([]byte(fakePlaces))
		case "/api/v1/places/4":
			w.Write([]byte(fakePlace))
		case "/api/v1/places/99":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no such route"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPlaces(t *testing.T) {
	var auth string
	srv := fakeTracker(t, "secret", &auth)
	c := New(srv.URL, "secret")

	places, err := c.Places(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(places) != 6 {
		t.Fatalf("got %d places, want 6", len(places))
	}
	if places[0].ParentID != nil {
		t.Errorf("site ParentID = %v, want nil", *places[0].ParentID)
	}
	room := places[3]
	if room.ID != 4 || room.Kind != "room" || room.Number != "104" || room.Name != "Nursery" {
		t.Errorf("room decoded wrong: %+v", room)
	}
	if room.ParentID == nil || *room.ParentID != 3 {
		t.Errorf("room ParentID wrong: %v", room.ParentID)
	}
	if len(places[4].AlsoIn) != 1 || places[4].AlsoIn[0] != 4 {
		t.Errorf("area AlsoIn = %v, want [4]", places[4].AlsoIn)
	}
	if auth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer secret")
	}
}

func TestPlace(t *testing.T) {
	var auth string
	srv := fakeTracker(t, "secret", &auth)
	c := New(srv.URL, "secret")

	p, err := c.Place(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 4 || p.Name != "Nursery" || p.Kind != "room" || p.Number != "104" {
		t.Errorf("Place decoded wrong: %+v", p)
	}
	if p.ParentID == nil || *p.ParentID != 3 {
		t.Errorf("ParentID wrong: %v", p.ParentID)
	}
	if auth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer secret")
	}

	_, err = c.Place(context.Background(), 99)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Place(99) error = %v, want ErrNotFound", err)
	}
}

func TestRooms(t *testing.T) {
	var auth string
	srv := fakeTracker(t, "secret", &auth)
	c := New(srv.URL, "secret")
	ctx := context.Background()

	names := func(ps []Place) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	tests := []struct {
		query string
		want  []string
	}{
		// Empty query: buildings, rooms and areas only, not the site or floor.
		{"", []string{"Fellowship Hall", "Nursery", "Kitchen Pass-through", "Sanctuary"}},
		// Matches name, case-insensitively.
		{"NURSERY", []string{"Nursery"}},
		// Matches number.
		{"201", []string{"Sanctuary"}},
		// Matches path, but not floors or sites even when the path matches.
		{"fellowship", []string{"Fellowship Hall", "Nursery", "Kitchen Pass-through"}},
		// Whitespace-only query is treated as empty.
		{"   ", []string{"Fellowship Hall", "Nursery", "Kitchen Pass-through", "Sanctuary"}},
		{"ground floor", []string{"Nursery", "Kitchen Pass-through"}},
		{"nothing like this", nil},
	}
	for _, tt := range tests {
		got, err := c.Rooms(ctx, tt.query)
		if err != nil {
			t.Fatalf("Rooms(%q): %v", tt.query, err)
		}
		if !equal(names(got), tt.want) {
			t.Errorf("Rooms(%q) = %v, want %v", tt.query, names(got), tt.want)
		}
	}
}

func TestPlacesJSONRoundTrip(t *testing.T) {
	// Guard that the struct tags match the wire names.
	var p Place
	if err := json.Unmarshal([]byte(`{"id":7,"parent_id":2,"kind":"area","number":"","name":"Stage","path":"x","also_in":[3]}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ID != 7 || p.ParentID == nil || *p.ParentID != 2 || p.Kind != "area" || len(p.AlsoIn) != 1 {
		t.Errorf("unexpected decode: %+v", p)
	}
}
