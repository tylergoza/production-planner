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

// productionByID loads a production for the signed-in user. A locked one
// they aren't a member of is "not found", so its title doesn't leak. Every
// page and form for a production or anything in it goes through here.
func (s *Server) productionByID(w http.ResponseWriter, r *http.Request, id int64) (*store.Production, bool) {
	p, err := s.store.GetProduction(id)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	ok, err := s.store.CanSee(p, currentUser(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if !ok {
		s.notFound(w, r)
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
	list, err := s.store.ListProductions(currentUser(r), true, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	upcoming, err := s.store.UpcomingEvents(currentUser(r), s.today(), 10)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", map[string]any{"Productions": list, "Upcoming": upcoming, "Live": true})
}

// Productions ------------------------------------------------------------

func (s *Server) handleProductions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProductions(currentUser(r), false, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "productions/index", map[string]any{"Title": "Productions", "Productions": list})
}

func (s *Server) productionForm(w http.ResponseWriter, r *http.Request, status int, p store.Production, members []int64, errs []string) {
	title := "New production"
	if p.ID != 0 {
		title = "Edit " + p.Title
	}
	data := map[string]any{"Title": title, "Form": p, "Errors": errs, "Statuses": store.ProductionStatuses}
	// Only admins see who's on it and can change that.
	if currentUser(r).IsAdmin {
		// Current members stay listed even if their access was removed.
		var keep []int64
		if p.ID != 0 {
			var err error
			if keep, err = s.store.Members(p.ID); err != nil {
				s.serverError(w, r, err)
				return
			}
		}
		users, err := s.pickableUsers(r, keep)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		isMember := map[int64]bool{}
		for _, id := range members {
			isMember[id] = true
		}
		data["Users"], data["IsMember"] = users, isMember
	}
	s.render(w, r, status, "productions/form", data)
}

func (s *Server) handleProductionNew(w http.ResponseWriter, r *http.Request) {
	s.productionForm(w, r, http.StatusOK, store.Production{Status: "planning"}, nil, nil)
}

// readProduction reads the form into p and returns who's on its member
// list. Only admins lock productions and pick who's on them; for anyone
// else p keeps the access it had and the list comes back nil.
func readProduction(r *http.Request, p *store.Production) ([]int64, []string) {
	p.Title, p.Kind, p.Status = formStr(r, "title"), formStr(r, "kind"), formStr(r, "status")
	p.Venue, p.Director = formStr(r, "venue"), formStr(r, "director")
	p.Description, p.Notes = formStr(r, "description"), formStr(r, "notes")
	var members []int64
	if currentUser(r).IsAdmin {
		p.Locked = formStr(r, "access") == "locked"
		for _, v := range r.PostForm["member"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				members = append(members, id)
			}
		}
	}
	var errs []string
	if p.Title == "" {
		errs = append(errs, "Give the production a title.")
	}
	if !slices.Contains(store.ProductionStatuses, p.Status) {
		errs = append(errs, "Pick a status.")
	}
	return members, errs
}

func (s *Server) handleProductionCreate(w http.ResponseWriter, r *http.Request) {
	var p store.Production
	members, errs := readProduction(r, &p)
	if errs != nil {
		s.productionForm(w, r, http.StatusUnprocessableEntity, p, members, errs)
		return
	}
	id, err := s.store.CreateProduction(&p)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.Locked && currentUser(r).IsAdmin {
		if err := s.store.SetMembers(id, members); err != nil {
			s.serverError(w, r, err)
			return
		}
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
		"StillNeeded": stillNeeded, "Chart": chart, "Live": true,
	})
}

func (s *Server) handleProductionEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	var members []int64
	if currentUser(r).IsAdmin {
		var err error
		if members, err = s.store.Members(p.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.productionForm(w, r, http.StatusOK, *p, members, nil)
}

func (s *Server) handleProductionUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	members, errs := readProduction(r, p)
	if errs != nil {
		s.productionForm(w, r, http.StatusUnprocessableEntity, *p, members, errs)
		return
	}
	if err := s.store.UpdateProduction(p); err != nil {
		s.serverError(w, r, err)
		return
	}
	// Unlocking keeps the list, for if it's locked again.
	if p.Locked && currentUser(r).IsAdmin {
		if err := s.store.SetMembers(p.ID, members); err != nil {
			s.serverError(w, r, err)
			return
		}
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
	data["Live"] = true
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
