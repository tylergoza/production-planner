package server

import (
	"net/http"
	"slices"
	"strconv"

	"github.com/tylergoza/production-planner/internal/store"
)

// production loads the production named by the {id} in the path, or
// shows "not found".
func (s *Server) production(w http.ResponseWriter, r *http.Request) (*store.Production, bool) {
	return s.productionByID(w, r, pathID(r))
}

func (s *Server) productionByID(w http.ResponseWriter, r *http.Request, id int64) (*store.Production, bool) {
	p, err := s.store.GetProduction(id)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return p, true
}

// prodPage renders one of a production's tabs.
func (s *Server) prodPage(w http.ResponseWriter, r *http.Request, status int, page string, p *store.Production, tab string, data map[string]any) {
	data["P"] = p
	data["Tab"] = tab
	if _, ok := data["Title"]; !ok {
		data["Title"] = p.Title
	}
	s.render(w, r, status, page, data)
}

func prodURL(id int64, rest string) string { return "/productions/" + strconv.FormatInt(id, 10) + rest }

// Dashboard --------------------------------------------------------------

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProductions(true, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	upcoming, err := s.store.UpcomingEvents(s.today(), 10)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", map[string]any{"Productions": list, "Upcoming": upcoming})
}

// Productions ------------------------------------------------------------

func (s *Server) handleProductions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProductions(false, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "productions/index", map[string]any{"Title": "Productions", "Productions": list})
}

func (s *Server) productionForm(w http.ResponseWriter, r *http.Request, status int, p store.Production, errs []string) {
	title := "New production"
	if p.ID != 0 {
		title = "Edit " + p.Title
	}
	s.render(w, r, status, "productions/form", map[string]any{
		"Title": title, "Form": p, "Errors": errs, "Statuses": store.ProductionStatuses,
	})
}

func (s *Server) handleProductionNew(w http.ResponseWriter, r *http.Request) {
	s.productionForm(w, r, http.StatusOK, store.Production{Status: "planning"}, nil)
}

func readProduction(r *http.Request, p *store.Production) []string {
	p.Title, p.Kind, p.Status = formStr(r, "title"), formStr(r, "kind"), formStr(r, "status")
	p.Venue, p.Director = formStr(r, "venue"), formStr(r, "director")
	p.Description, p.Notes = formStr(r, "description"), formStr(r, "notes")
	var errs []string
	if p.Title == "" {
		errs = append(errs, "Give the production a title.")
	}
	if !slices.Contains(store.ProductionStatuses, p.Status) {
		errs = append(errs, "Pick a status.")
	}
	return errs
}

func (s *Server) handleProductionCreate(w http.ResponseWriter, r *http.Request) {
	var p store.Production
	if errs := readProduction(r, &p); errs != nil {
		s.productionForm(w, r, http.StatusUnprocessableEntity, p, errs)
		return
	}
	id, err := s.store.CreateProduction(&p)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(id, "/schedule"), p.Title+" added. Next, add its dates.")
}

func (s *Server) handleProductionShow(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	sum, err := s.store.Summary(p, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	events, err := s.store.ListEvents(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	prep, err := s.store.ListPrep(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	needs, err := s.store.ListNeeds(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	chart, _, err := s.store.ChartStatus(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var upcoming []store.Event
	for _, e := range events {
		if e.Date >= s.today() && len(upcoming) < 5 {
			upcoming = append(upcoming, e)
		}
	}
	var attention []store.PrepItem
	for _, it := range prep {
		if it.Status == "blocked" || (it.Status != "ready" && it.DueOn != "" && it.DueOn < s.today()) {
			attention = append(attention, it)
		}
	}
	var stillNeeded []store.Need
	for _, n := range needs {
		if n.Status == "needed" {
			stillNeeded = append(stillNeeded, n)
		}
	}
	s.prodPage(w, r, http.StatusOK, "productions/show", p, "overview", map[string]any{
		"Sum": sum, "Upcoming": upcoming, "Departments": store.GroupPrep(prep), "Attention": attention,
		"StillNeeded": stillNeeded, "Chart": chart,
	})
}

func (s *Server) handleProductionEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	s.productionForm(w, r, http.StatusOK, *p, nil)
}

func (s *Server) handleProductionUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	if errs := readProduction(r, p); errs != nil {
		s.productionForm(w, r, http.StatusUnprocessableEntity, *p, errs)
		return
	}
	if err := s.store.UpdateProduction(p); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, ""), "Saved.")
}

func (s *Server) handleProductionDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteProduction(p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/productions", p.Title+" deleted.")
}

// Schedule ---------------------------------------------------------------

func (s *Server) scheduleData(p *store.Production, form store.Event, errs []string) (map[string]any, error) {
	events, err := s.store.ListEvents(p.ID)
	if err != nil {
		return nil, err
	}
	if form.Kind == "" {
		form.Kind = "rehearsal"
	}
	return map[string]any{"Events": events, "Form": form, "Errors": errs, "Kinds": store.EventKinds}, nil
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	data, err := s.scheduleData(p, store.Event{}, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, http.StatusOK, "productions/schedule", p, "schedule", data)
}

func readEvent(r *http.Request, e *store.Event) []string {
	e.Kind, e.Date, e.Time = formStr(r, "kind"), formStr(r, "date"), formStr(r, "time")
	e.Label, e.Notes = formStr(r, "label"), formStr(r, "notes")
	var errs []string
	if !slices.Contains(store.EventKinds, e.Kind) {
		errs = append(errs, "Pick what kind of date it is.")
	}
	if !validDate(e.Date) {
		errs = append(errs, "Pick a date.")
	}
	if e.Time != "" && !validTime(e.Time) {
		errs = append(errs, "Times look like 18:30.")
	}
	return errs
}

func (s *Server) handleEventCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	e := store.Event{ProductionID: p.ID}
	if errs := readEvent(r, &e); errs != nil {
		data, err := s.scheduleData(p, e, errs)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.prodPage(w, r, http.StatusUnprocessableEntity, "productions/schedule", p, "schedule", data)
		return
	}
	if _, err := s.store.CreateEvent(&e); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/schedule"), e.Title()+" on "+fmtDate(e.Date)+" added.")
}

func (s *Server) eventAndProduction(w http.ResponseWriter, r *http.Request) (*store.Event, *store.Production, bool) {
	e, err := s.store.GetEvent(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	p, ok := s.productionByID(w, r, e.ProductionID)
	return e, p, ok
}

func (s *Server) handleEventEdit(w http.ResponseWriter, r *http.Request) {
	e, p, ok := s.eventAndProduction(w, r)
	if !ok {
		return
	}
	s.prodPage(w, r, http.StatusOK, "events/form", p, "schedule", map[string]any{"Form": *e, "Kinds": store.EventKinds})
}

func (s *Server) handleEventUpdate(w http.ResponseWriter, r *http.Request) {
	e, p, ok := s.eventAndProduction(w, r)
	if !ok {
		return
	}
	if errs := readEvent(r, e); errs != nil {
		s.prodPage(w, r, http.StatusUnprocessableEntity, "events/form", p, "schedule", map[string]any{"Form": *e, "Kinds": store.EventKinds, "Errors": errs})
		return
	}
	if err := s.store.UpdateEvent(e); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/schedule"), "Saved.")
}

func (s *Server) handleEventDelete(w http.ResponseWriter, r *http.Request) {
	e, p, ok := s.eventAndProduction(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteEvent(e.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/schedule"), "Date removed.")
}
