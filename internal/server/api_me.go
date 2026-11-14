package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/tylergoza/production-planner/internal/store"
	"github.com/tylergoza/production-planner/internal/umtoken"
)

// The per-person read API, /api/v1/me/…, for the church MCP server. The
// gate (token check, local user) is me_auth.go; the shapes are
// APP_CONTRACTS.md in ../mcp_server (Production planner).
//
// Everything here is what the person sees on the web pages: the
// productions they can open (admins all of them, everyone else the
// unlocked ones and the locked ones they're a member of), with the
// overview's details, the schedule and the cast. Nothing about prep,
// needs or mics, and a production they can't see is a JSON 404, the
// same answer as one that doesn't exist.

// meMaxItems bounds every list (the contract's cap).
const meMaxItems = 100

func (s *Server) meRoutes(mux *http.ServeMux) {
	s.meRoute(mux, "GET /api/v1/me/productions", s.meProductions)
	s.meRoute(mux, "GET /api/v1/me/productions/{id}", s.meProduction)
	s.meCatchAll(mux)
}

// meUMConfig is the live single sign-on connection; the /me API is off
// without it.
func (s *Server) meUMConfig() (umtoken.Config, bool) {
	c := s.SSO()
	if !c.Enabled() || c.ClientID == "" {
		return umtoken.Config{}, false
	}
	return umtoken.Config{URL: c.InternalURL, ClientID: c.ClientID, ClientSecret: c.ClientSecret}, true
}

// meLocalUser is what the SSO callback and grant checks do (sso.go).
func (s *Server) meLocalUser(u umtoken.User) (*store.User, error) {
	return s.store.UpsertSSOUser(u.Sub, u.Username, u.Name, u.Role == "admin")
}

// apiError answers {"error":msg} as JSON.
func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) meServerError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("me api", "path", r.URL.Path, "err", err)
	apiError(w, http.StatusInternalServerError, "something went wrong")
}

// meDateJSON is one date on a production's schedule. A date is a single
// day: start_time "" is all day, end_time "" (with a start) is "until
// done", as on the schedule page.
type meDateJSON struct {
	Kind      string   `json:"kind"`
	Label     string   `json:"label,omitempty"`
	StartsOn  string   `json:"starts_on"`
	StartTime string   `json:"start_time"`
	EndTime   string   `json:"end_time"`
	Tentative bool     `json:"tentative"`
	Rooms     []string `json:"rooms"`
	Notes     string   `json:"notes"`
}

type meCastJSON struct {
	Name string `json:"name"`
	Part string `json:"part"`
}

// meProductionJSON is a production on the list; status is the label the
// pages show ("Rehearsing").
type meProductionJSON struct {
	ID       int64       `json:"id"`
	Title    string      `json:"title"`
	Kind     string      `json:"kind"`
	Status   string      `json:"status"`
	NextDate *meDateJSON `json:"next_date"`
}

// meProductionDetailJSON is one production: the overview's details (the
// description is the overview's "About"), the schedule and the cast.
type meProductionDetailJSON struct {
	meProductionJSON
	Where       string       `json:"where"`
	Director    string       `json:"director"`
	Description string       `json:"description,omitempty"`
	Notes       string       `json:"notes"`
	Schedule    []meDateJSON `json:"schedule"`
	Cast        []meCastJSON `json:"cast"`
}

func meDate(e store.Event, rooms []store.Room) meDateJSON {
	d := meDateJSON{Kind: e.Kind, Label: e.Label, StartsOn: e.Date, StartTime: e.Time, EndTime: e.EndTime,
		Tentative: e.Tentative, Notes: e.Notes, Rooms: make([]string, 0, len(rooms))}
	for _, r := range rooms {
		d.Rooms = append(d.Rooms, r.PlaceName)
	}
	return d
}

func (s *Server) meNextDate(sum *store.ProductionSummary) (*meDateJSON, error) {
	if sum.NextEvent == nil {
		return nil, nil
	}
	rooms, err := s.store.EventRooms(sum.NextEvent.ID)
	if err != nil {
		return nil, err
	}
	d := meDate(*sum.NextEvent, rooms)
	return &d, nil
}

func meBase(p store.Production) meProductionJSON {
	return meProductionJSON{ID: p.ID, Title: p.Title, Kind: p.Kind, Status: store.ProductionStatusLabel(p.Status)}
}

// meProductions is the Productions page: every production the person can
// see, in the same order, optionally of one status.
func (s *Server) meProductions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains(store.ProductionStatuses, status) {
		apiError(w, http.StatusBadRequest, "status must be planning, rehearsing, ready, done or cancelled")
		return
	}
	list, err := s.store.ListProductions(meUser(r), false, s.today())
	if err != nil {
		s.meServerError(w, r, err)
		return
	}
	out := make([]meProductionJSON, 0, min(len(list), meMaxItems))
	for i := range list {
		if len(out) == meMaxItems {
			break
		}
		if status != "" && list[i].Status != status {
			continue
		}
		j := meBase(list[i].Production)
		if j.NextDate, err = s.meNextDate(&list[i]); err != nil {
			s.meServerError(w, r, err)
			return
		}
		out = append(out, j)
	}
	writeJSON(w, http.StatusOK, map[string]any{"productions": out})
}

// meProduction is the production's overview, schedule and cast pages.
func (s *Server) meProduction(w http.ResponseWriter, r *http.Request) {
	notFound := func() { apiError(w, http.StatusNotFound, "no production with that id that you can see") }
	p, err := s.store.GetProduction(pathID(r))
	if errors.Is(err, store.ErrNotFound) {
		notFound()
		return
	} else if err != nil {
		s.meServerError(w, r, err)
		return
	}
	if ok, err := s.store.CanSee(p, meUser(r)); err != nil {
		s.meServerError(w, r, err)
		return
	} else if !ok {
		notFound()
		return
	}
	sum, err := s.store.Summary(p, s.today())
	if err != nil {
		s.meServerError(w, r, err)
		return
	}
	events, err := s.store.ListEvents(p.ID)
	if err != nil {
		s.meServerError(w, r, err)
		return
	}
	rooms, err := s.store.ScheduleRooms(p.ID)
	if err != nil {
		s.meServerError(w, r, err)
		return
	}
	cast, err := s.store.ListCast(p.ID)
	if err != nil {
		s.meServerError(w, r, err)
		return
	}

	j := meProductionDetailJSON{meProductionJSON: meBase(*p),
		Where: p.Venue, Director: p.Director, Description: p.Description, Notes: p.Notes}
	if j.NextDate, err = s.meNextDate(sum); err != nil {
		s.meServerError(w, r, err)
		return
	}
	j.Schedule = make([]meDateJSON, 0, min(len(events), meMaxItems))
	for _, e := range meWindow(events, s.today()) {
		j.Schedule = append(j.Schedule, meDate(e, rooms[e.ID]))
	}
	j.Cast = make([]meCastJSON, 0, min(len(cast), meMaxItems))
	for _, c := range cast {
		if len(j.Cast) == meMaxItems {
			break
		}
		j.Cast = append(j.Cast, meCastJSON{Name: c.Person, Part: c.Character})
	}
	writeJSON(w, http.StatusOK, j)
}

// meWindow is at most meMaxItems of a production's dates (in date order),
// dropping the oldest past ones first so upcoming dates are never cut.
func meWindow(events []store.Event, today string) []store.Event {
	if len(events) <= meMaxItems {
		return events
	}
	next := len(events)
	for i, e := range events {
		if e.Date >= today {
			next = i
			break
		}
	}
	start := min(next, len(events)-meMaxItems)
	return events[start : start+meMaxItems]
}
