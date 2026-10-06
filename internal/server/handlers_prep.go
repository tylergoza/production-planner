package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
	"github.com/tylergoza/production-planner/internal/tracker"
)

// Readiness: what has to be ready, by department -------------------------------

func (s *Server) prepData(p *store.Production, data map[string]any) (map[string]any, error) {
	items, err := s.store.ListPrep(p.ID)
	if err != nil {
		return nil, err
	}
	scenes, err := s.store.ListScenes(p.ID)
	if err != nil {
		return nil, err
	}
	chart, _, err := s.store.ChartStatus(p.ID)
	if err != nil {
		return nil, err
	}
	total := store.Tally{}
	for _, it := range items {
		total.Total++
		if it.Status == "ready" {
			total.Done++
		}
	}
	data["Title"] = "Readiness · " + p.Title
	data["Groups"] = store.GroupPrep(items)
	data["Total"] = total
	data["Scenes"] = scenes
	data["Departments"] = store.Departments
	data["Statuses"] = store.PrepStatuses
	data["Chart"] = chart
	if _, ok := data["Form"]; !ok {
		data["Form"] = store.PrepItem{Department: "props", Status: "todo"}
	}
	return data, nil
}

func (s *Server) renderPrep(w http.ResponseWriter, r *http.Request, status int, p *store.Production, data map[string]any) {
	data, err := s.prepData(p, data)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, status, "prep/index", p, "prep", data)
}

func (s *Server) handlePrep(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	s.renderPrep(w, r, http.StatusOK, p, map[string]any{})
}

func (s *Server) readPrep(r *http.Request, it *store.PrepItem) []string {
	it.Department, it.Name, it.Owner = formStr(r, "department"), formStr(r, "name"), formStr(r, "owner")
	it.DueOn, it.Notes, it.SceneID = formStr(r, "due_on"), formStr(r, "notes"), formInt(r, "scene_id")
	if v := formStr(r, "status"); v != "" {
		it.Status = v
	}
	it.UpdatedBy = currentUser(r).Name()
	var errs []string
	if it.Name == "" {
		errs = append(errs, "Say what has to be ready.")
	}
	if !store.ValidDepartment(it.Department) {
		errs = append(errs, "Pick a department.")
	}
	if !store.ValidPrepStatus(it.Status) {
		errs = append(errs, "Pick a status.")
	}
	if it.DueOn != "" && !validDate(it.DueOn) {
		errs = append(errs, "That due date isn't a date.")
	}
	if it.SceneID != 0 {
		if sc, err := s.store.GetScene(it.SceneID); err != nil || sc.ProductionID != it.ProductionID {
			errs = append(errs, "That scene isn't in this production.")
		}
	}
	return errs
}

func (s *Server) handlePrepCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	it := store.PrepItem{ProductionID: p.ID, Status: "todo"}
	if errs := s.readPrep(r, &it); errs != nil {
		s.renderPrep(w, r, http.StatusUnprocessableEntity, p, map[string]any{"Form": it, "Errors": errs})
		return
	}
	if _, err := s.store.CreatePrep(&it); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/prep#dept-"+it.Department), it.Name+" added to "+store.DepartmentLabel(it.Department)+".")
}

func (s *Server) prepAndProduction(w http.ResponseWriter, r *http.Request) (*store.PrepItem, *store.Production, bool) {
	it, err := s.store.GetPrep(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	p, ok := s.productionByID(w, r, it.ProductionID)
	return it, p, ok
}

func (s *Server) prepForm(w http.ResponseWriter, r *http.Request, status int, p *store.Production, it store.PrepItem, errs []string) {
	scenes, err := s.store.ListScenes(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, status, "prep/form", p, "prep", map[string]any{
		"Form": it, "Errors": errs, "Scenes": scenes, "Departments": store.Departments, "Statuses": store.PrepStatuses,
	})
}

func (s *Server) handlePrepEdit(w http.ResponseWriter, r *http.Request) {
	it, p, ok := s.prepAndProduction(w, r)
	if !ok {
		return
	}
	s.prepForm(w, r, http.StatusOK, p, *it, nil)
}

func (s *Server) handlePrepUpdate(w http.ResponseWriter, r *http.Request) {
	it, p, ok := s.prepAndProduction(w, r)
	if !ok {
		return
	}
	if errs := s.readPrep(r, it); errs != nil {
		s.prepForm(w, r, http.StatusUnprocessableEntity, p, *it, errs)
		return
	}
	if err := s.store.UpdatePrep(it); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/prep#dept-"+it.Department), "Saved.")
}

func (s *Server) handlePrepStatus(w http.ResponseWriter, r *http.Request) {
	it, p, ok := s.prepAndProduction(w, r)
	if !ok {
		return
	}
	status := formStr(r, "status")
	if !store.ValidPrepStatus(status) {
		s.setFlash(w, r, "error", "Pick a status.")
		http.Redirect(w, r, prodURL(p.ID, "/prep"), http.StatusSeeOther)
		return
	}
	if err := s.store.SetPrepStatus(it.ID, status, currentUser(r).Name()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/prep#prep-"+strconv.FormatInt(it.ID, 10)), it.Name+": "+store.PrepStatusLabel(status)+".")
}

func (s *Server) handlePrepDelete(w http.ResponseWriter, r *http.Request) {
	it, p, ok := s.prepAndProduction(w, r)
	if !ok {
		return
	}
	if err := s.store.DeletePrep(it.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/prep#dept-"+it.Department), it.Name+" removed.")
}

// Needs: items and supplies, mostly from the maintenance tracker ---------------

// needRow is a need with what the maintenance tracker says about it.
type needRow struct {
	store.Need
	Found     bool   // the tracker still has it
	Have      int    // how many there are in all (products) or on hand (supplies)
	Unit      string // a supply's unit, e.g. "rolls"
	Stock     string // supplies: "ok", "low" or "out"
	Where     string
	Short     bool // fewer than needed
	Shortfall int
	Link      string
}

func (s *Server) needRows(ctx context.Context, needs []store.Need) ([]needRow, string) {
	t := s.Tracker()
	rows := make([]needRow, len(needs))
	var products map[int64]tracker.Product
	var supplies map[int64]tracker.Supply
	var problem string
	for i, n := range needs {
		row := needRow{Need: n}
		switch {
		case !n.FromTracker():
		case !t.Configured():
			problem = trackerErr(tracker.ErrNotConfigured)
		case n.TrackerKind == "product":
			if products == nil {
				list, err := t.Products(ctx, "")
				if err != nil {
					problem = trackerErr(err)
					break
				}
				products = map[int64]tracker.Product{}
				for _, p := range list {
					products[p.ID] = p
				}
			}
			if p, ok := products[n.TrackerID]; ok {
				row.Found, row.Have = true, p.Total
				row.Where = pluralize(p.PlaceCount, "place")
				row.Link = t.PageURL("products", p.ID)
			}
		case n.TrackerKind == "supply":
			if supplies == nil {
				list, err := t.Supplies(ctx, "")
				if err != nil {
					problem = trackerErr(err)
					break
				}
				supplies = map[int64]tracker.Supply{}
				for _, sp := range list {
					supplies[sp.ID] = sp
				}
			}
			if sp, ok := supplies[n.TrackerID]; ok {
				row.Found, row.Have, row.Unit, row.Stock = true, sp.OnHand, sp.Unit, sp.Stock
				row.Where = sp.Place.Path
				row.Link = t.PageURL("supplies", sp.ID)
			}
		}
		row.Short = row.Found && n.Status == "needed" && row.Have < n.Quantity
		if row.Short {
			row.Shortfall = n.Quantity - row.Have
		}
		rows[i] = row
	}
	return rows, problem
}

func (s *Server) needsData(r *http.Request, p *store.Production, data map[string]any) (map[string]any, error) {
	needs, err := s.store.ListNeeds(p.ID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	rows, problem := s.needRows(ctx, needs)
	tally := store.Tally{Total: len(needs)}
	for _, n := range needs {
		if n.Status != "needed" {
			tally.Done++
		}
	}
	t := s.Tracker()
	data["Title"] = "Needs · " + p.Title
	data["Needs"] = rows
	data["Tally"] = tally
	data["TrackerProblem"] = problem
	data["TrackerOn"] = t.Configured()
	data["Departments"] = store.Departments
	data["Statuses"] = store.NeedStatuses
	if _, ok := data["Form"]; !ok {
		data["Form"] = store.Need{Quantity: 1}
	}
	if q := r.URL.Query().Get("q"); q != "" && t.Configured() {
		data["Q"] = q
		products, err := t.Products(ctx, q)
		if err != nil {
			data["SearchError"] = trackerErr(err)
		}
		supplies, err := t.Supplies(ctx, q)
		if err != nil {
			data["SearchError"] = trackerErr(err)
		}
		data["FoundProducts"] = products
		data["FoundSupplies"] = supplies
	}
	return data, nil
}

func (s *Server) renderNeeds(w http.ResponseWriter, r *http.Request, status int, p *store.Production, data map[string]any) {
	data, err := s.needsData(r, p, data)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, status, "needs/index", p, "needs", data)
}

func (s *Server) handleNeeds(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	s.renderNeeds(w, r, http.StatusOK, p, map[string]any{})
}

func readNeed(r *http.Request, n *store.Need) []string {
	n.Name, n.Department, n.Notes = formStr(r, "name"), formStr(r, "department"), formStr(r, "notes")
	if v := formStr(r, "status"); v != "" {
		n.Status = v
	}
	q, err := strconv.Atoi(formStr(r, "quantity"))
	n.Quantity = q
	n.UpdatedBy = currentUser(r).Name()
	var errs []string
	if n.Name == "" {
		errs = append(errs, "Say what's needed.")
	}
	if err != nil || q < 1 || q > 100000 {
		errs = append(errs, "How many are needed? At least 1.")
	}
	if n.Department != "" && !store.ValidDepartment(n.Department) {
		errs = append(errs, "Pick a department, or none.")
	}
	if !store.ValidNeedStatus(n.Status) {
		errs = append(errs, "Pick a status.")
	}
	return errs
}

func (s *Server) handleNeedCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	n := store.Need{ProductionID: p.ID, Status: "needed"}
	errs := readNeed(r, &n)
	switch kind := formStr(r, "tracker_kind"); kind {
	case "":
	case "product", "supply":
		n.TrackerKind, n.TrackerID = kind, formInt(r, "tracker_id")
		if n.TrackerID < 1 {
			errs = append(errs, "That isn't something in the maintenance tracker.")
		}
	default:
		errs = append(errs, "That isn't something in the maintenance tracker.")
	}
	if errs != nil {
		s.renderNeeds(w, r, http.StatusUnprocessableEntity, p, map[string]any{"Form": n, "Errors": errs})
		return
	}
	if _, err := s.store.CreateNeed(&n); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/needs"), strconv.Itoa(n.Quantity)+" × "+n.Name+" added.")
}

func (s *Server) needAndProduction(w http.ResponseWriter, r *http.Request) (*store.Need, *store.Production, bool) {
	n, err := s.store.GetNeed(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	p, ok := s.productionByID(w, r, n.ProductionID)
	return n, p, ok
}

func (s *Server) needForm(w http.ResponseWriter, r *http.Request, status int, p *store.Production, n store.Need, errs []string) {
	link := ""
	if n.FromTracker() {
		kind := "products"
		if n.TrackerKind == "supply" {
			kind = "supplies"
		}
		link = s.Tracker().PageURL(kind, n.TrackerID)
	}
	s.prodPage(w, r, status, "needs/form", p, "needs", map[string]any{
		"Form": n, "Errors": errs, "Departments": store.Departments, "Statuses": store.NeedStatuses, "TrackerLink": link,
	})
}

func (s *Server) handleNeedEdit(w http.ResponseWriter, r *http.Request) {
	n, p, ok := s.needAndProduction(w, r)
	if !ok {
		return
	}
	s.needForm(w, r, http.StatusOK, p, *n, nil)
}

func (s *Server) handleNeedUpdate(w http.ResponseWriter, r *http.Request) {
	n, p, ok := s.needAndProduction(w, r)
	if !ok {
		return
	}
	if errs := readNeed(r, n); errs != nil {
		s.needForm(w, r, http.StatusUnprocessableEntity, p, *n, errs)
		return
	}
	if err := s.store.UpdateNeed(n); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/needs"), "Saved.")
}

func (s *Server) handleNeedStatus(w http.ResponseWriter, r *http.Request) {
	n, p, ok := s.needAndProduction(w, r)
	if !ok {
		return
	}
	status := formStr(r, "status")
	if !store.ValidNeedStatus(status) {
		s.setFlash(w, r, "error", "Pick a status.")
		http.Redirect(w, r, prodURL(p.ID, "/needs"), http.StatusSeeOther)
		return
	}
	if err := s.store.SetNeedStatus(n.ID, status, currentUser(r).Name()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/needs"), n.Name+": "+store.NeedStatusLabel(status)+".")
}

func (s *Server) handleNeedDelete(w http.ResponseWriter, r *http.Request) {
	n, p, ok := s.needAndProduction(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteNeed(n.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/needs"), n.Name+" removed.")
}
