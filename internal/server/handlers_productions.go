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
	// Active productions with no dates get their own group.
	var scheduled, notScheduled []store.ProductionSummary
	for _, p := range list {
		if p.NotScheduled() {
			notScheduled = append(notScheduled, p)
		} else {
			scheduled = append(scheduled, p)
		}
	}
	s.render(w, r, http.StatusOK, "dashboard", map[string]any{"Productions": scheduled, "NotScheduled": notScheduled, "Upcoming": upcoming, "Live": true})
}

// Productions ------------------------------------------------------------

func (s *Server) handleProductions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProductions(currentUser(r), false, s.today())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Active productions with no dates are listed in their own section.
	var rest, notScheduled []store.ProductionSummary
	for _, p := range list {
		if p.Active() && p.NotScheduled() {
			notScheduled = append(notScheduled, p)
		} else {
			rest = append(rest, p)
		}
	}
	s.render(w, r, http.StatusOK, "productions/index", map[string]any{"Title": "Productions", "Productions": rest, "NotScheduled": notScheduled})
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
	if p.ID != 0 {
		if err := s.roomsData(r, &p, data); err != nil {
			s.serverError(w, r, err)
			return
		}
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
	p.Public = formStr(r, "public") == "1"
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
		"StillNeeded": stillNeeded, "Chart": chart, "Live": true, "DatesWarning": sum.NeedsDatesWarning(),
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
	msg := "Saved."
	if sum, err := s.store.Summary(p, s.today()); err == nil {
		if w := sum.NeedsDatesWarning(); w != "" {
			msg += " " + w
		}
	}
	s.redirect(w, r, prodURL(p.ID, ""), msg)
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

// scheduleRow is a date with its rooms and who else wants them.
type scheduleRow struct {
	store.Event
	Rooms     []store.Room
	Conflicts []store.DateRef // this app's other dates in the same rooms
	Clashes   []clash         // from the calendar, when it's set up
}

func (s *Server) scheduleData(r *http.Request, p *store.Production, form store.Event, opts []roomOption, errs []string) (map[string]any, error) {
	events, err := s.store.ListEvents(p.ID)
	if err != nil {
		return nil, err
	}
	rooms, err := s.store.ScheduleRooms(p.ID)
	if err != nil {
		return nil, err
	}
	conflicts, err := s.store.RoomConflicts(p.ID, currentUser(r))
	if err != nil {
		return nil, err
	}
	prodRooms, err := s.store.ProductionRooms(p.ID)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"Form": form, "Errors": errs, "Kinds": store.EventKinds, "ProdRooms": prodRooms}
	rows := make([]scheduleRow, len(events))
	tentative := 0
	for i, e := range events {
		if e.Tentative {
			tentative++
		}
		rows[i] = scheduleRow{Event: e, Rooms: rooms[e.ID], Conflicts: conflicts[e.ID]}
	}
	// With the calendar set, it knows about every app's bookings, so its
	// answer replaces this app's own check. If it can't be reached, fall
	// back to that, plus what it said the last time each date was sent.
	if s.Calendar().Configured() {
		if clashes, err := s.scheduleClashes(r.Context(), events, rooms); err != nil {
			data["CalendarDown"] = true
			for i := range rows {
				if rows[i].Date >= s.today() {
					rows[i].Clashes = s.storedClashes(rows[i].ID)
				}
			}
		} else {
			for i := range rows {
				rows[i].Conflicts = nil
				rows[i].Clashes = clashes[rows[i].ID]
			}
		}
	}
	data["Events"] = rows
	data["TentativeCount"] = tentative
	if form.Kind == "" {
		form.Kind = "rehearsal"
		data["Form"] = form
	}
	if opts == nil {
		opts = roomOptions(prodRooms, nil, true)
	}
	data["RoomOptions"] = opts
	return data, nil
}

func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	data, err := s.scheduleData(r, p, store.Event{}, nil, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data["Live"] = true
	s.prodPage(w, r, http.StatusOK, "productions/schedule", p, "schedule", data)
}

func readEvent(r *http.Request, e *store.Event) []string {
	e.Kind, e.Date, e.Time = formStr(r, "kind"), formStr(r, "date"), formStr(r, "time")
	e.EndTime = formStr(r, "end_time")
	e.Label, e.Notes = formStr(r, "label"), formStr(r, "notes")
	e.Tentative = formStr(r, "tentative") == "1"
	var errs []string
	if !slices.Contains(store.EventKinds, e.Kind) {
		errs = append(errs, "Pick what kind of date it is.")
	}
	if !validDate(e.Date) {
		errs = append(errs, "Pick a date.")
	}
	if (e.Time != "" && !validTime(e.Time)) || (e.EndTime != "" && !validTime(e.EndTime)) {
		errs = append(errs, "Times look like 18:30.")
	} else if e.EndTime != "" && e.Time == "" {
		errs = append(errs, "Give it a start time too, or leave the end time blank.")
	} else if e.EndTime != "" && e.EndTime <= e.Time {
		errs = append(errs, "The end time has to be after the start time.")
	}
	return errs
}

func (s *Server) handleEventCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	prodRooms, err := s.store.ProductionRooms(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	e := store.Event{ProductionID: p.ID}
	errs := readEvent(r, &e)
	opts := roomOptions(prodRooms, nil, true)
	rooms := readEventRooms(r, opts)
	if errs != nil {
		data, err := s.scheduleData(r, p, e, opts, errs)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.prodPage(w, r, http.StatusUnprocessableEntity, "productions/schedule", p, "schedule", data)
		return
	}
	if _, err := s.store.CreateEvent(&e, rooms); err != nil {
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

// eventRoomOptions is the room checkboxes for editing a date.
func (s *Server) eventRoomOptions(e *store.Event) ([]roomOption, error) {
	prodRooms, err := s.store.ProductionRooms(e.ProductionID)
	if err != nil {
		return nil, err
	}
	current, err := s.store.EventRooms(e.ID)
	if err != nil {
		return nil, err
	}
	return roomOptions(prodRooms, current, false), nil
}

func (s *Server) eventForm(w http.ResponseWriter, r *http.Request, status int, e *store.Event, p *store.Production, opts []roomOption, errs []string) {
	s.prodPage(w, r, status, "events/form", p, "schedule", map[string]any{
		"Form": *e, "Kinds": store.EventKinds, "RoomOptions": opts, "Errors": errs,
		"Clashes": s.storedClashes(e.ID),
	})
}

func (s *Server) handleEventEdit(w http.ResponseWriter, r *http.Request) {
	e, p, ok := s.eventAndProduction(w, r)
	if !ok {
		return
	}
	opts, err := s.eventRoomOptions(e)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.eventForm(w, r, http.StatusOK, e, p, opts, nil)
}

func (s *Server) handleEventUpdate(w http.ResponseWriter, r *http.Request) {
	e, p, ok := s.eventAndProduction(w, r)
	if !ok {
		return
	}
	opts, err := s.eventRoomOptions(e)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	errs := readEvent(r, e)
	rooms := readEventRooms(r, opts)
	if errs != nil {
		s.eventForm(w, r, http.StatusUnprocessableEntity, e, p, opts, errs)
		return
	}
	if err := s.store.UpdateEvent(e, rooms); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/schedule"), "Saved.")
}

func (s *Server) handleEventConfirm(w http.ResponseWriter, r *http.Request) {
	e, p, ok := s.eventAndProduction(w, r)
	if !ok {
		return
	}
	if err := s.store.ConfirmEvent(e.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/schedule"), "Confirmed.")
}

func (s *Server) handleEventsConfirmAll(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	n, err := s.store.ConfirmAllEvents(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := "No pencilled-in dates to confirm."
	if n == 1 {
		msg = "1 date confirmed."
	} else if n > 1 {
		msg = strconv.FormatInt(n, 10) + " dates confirmed."
	}
	s.redirect(w, r, prodURL(p.ID, "/schedule"), msg)
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
