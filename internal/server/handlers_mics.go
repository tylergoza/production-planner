package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
)

// The mic chart --------------------------------------------------------------

func (s *Server) handleMicChart(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	status, cur, err := s.store.ChartStatus(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var base *store.ChartSnapshot
	if status.Last != nil {
		base = &status.Last.Snapshot
	}
	versions, err := s.store.ListChartVersions(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cast, err := s.store.ListCast(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, http.StatusOK, "mics/chart", p, "mics", map[string]any{
		"Title": "Mic chart · " + p.Title, "Grid": store.BuildGrid(cur, base), "Chart": status, "Versions": versions,
		"CastCount": len(cast), "Live": true,
	})
}

func (s *Server) handleMicChartEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	cur, err := s.store.CurrentChart(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cast, err := s.store.ListCast(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(cur.Mics) == 0 || len(cur.Scenes) == 0 || len(cast) == 0 {
		s.setFlash(w, r, "error", "Add scenes, cast and mics first; the chart is who has each mic in each scene.")
		http.Redirect(w, r, prodURL(p.ID, "/mics"), http.StatusSeeOther)
		return
	}
	s.prodPage(w, r, http.StatusOK, "mics/edit", p, "mics", map[string]any{
		"Title": "Edit mic chart · " + p.Title, "Grid": store.BuildGrid(cur, nil), "Cast": cast,
	})
}

func (s *Server) handleMicChartSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	cur, err := s.store.CurrentChart(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cells := map[store.Cell]int64{}
	for _, m := range cur.Mics {
		for _, sc := range cur.Scenes {
			v := r.PostFormValue(fmt.Sprintf("c_%d_%d", m.ID, sc.ID))
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
				cells[store.Cell{MicID: m.ID, SceneID: sc.ID}] = id
			}
		}
	}
	if err := s.store.SaveAssignments(p.ID, cells); errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Someone changed the cast, scenes or mics while you were editing. Check the chart and try again.")
		http.Redirect(w, r, prodURL(p.ID, "/mics/edit"), http.StatusSeeOther)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/mics"), "Mic chart saved.")
}

func (s *Server) handleMicChartFinalize(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	v, err := s.store.FinalizeChart(p.ID, formStr(r, "note"), currentUser(r).Name())
	if errors.Is(err, store.ErrUnchanged) {
		s.setFlash(w, r, "info", "Nothing has changed since the last final version.")
		http.Redirect(w, r, prodURL(p.ID, "/mics"), http.StatusSeeOther)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/mics"), fmt.Sprintf("Mic chart marked final as version %d.", v))
}

func (s *Server) handleMicChartVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("version"))
	if err != nil || n < 1 {
		s.notFound(w, r)
		return
	}
	v, err := s.store.GetChartVersion(p.ID, n)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Show what changed from the version before it.
	var base *store.ChartSnapshot
	if n > 1 {
		if prev, err := s.store.GetChartVersion(p.ID, n-1); err == nil {
			base = &prev.Snapshot
		}
	}
	latest, err := s.store.LatestChartVersion(p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, http.StatusOK, "mics/version", p, "mics", map[string]any{
		"Title": fmt.Sprintf("Mic chart v%d · %s", v.Version, p.Title), "V": v, "Grid": store.BuildGrid(&v.Snapshot, base),
		"IsLatest": latest != nil && latest.Version == v.Version,
	})
}

// Mics -------------------------------------------------------------------------

func (s *Server) micSetupData(r *http.Request, p *store.Production, data map[string]any) (map[string]any, error) {
	mics, err := s.store.ListMics(p.ID)
	if err != nil {
		return nil, err
	}
	data["Title"] = "Mics · " + p.Title
	data["Mics"] = mics
	data["Kinds"] = store.MicKinds
	if _, ok := data["Form"]; !ok {
		data["Form"] = store.Mic{Kind: "lav"}
	}
	t := s.Tracker()
	data["TrackerOn"] = t.Configured()
	if t.Configured() {
		q := r.URL.Query().Get("q")
		if _, searched := r.URL.Query()["q"]; !searched {
			q = "mic"
		}
		data["Q"] = q
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		products, err := t.Products(ctx, q)
		if err != nil {
			data["TrackerError"] = trackerErr(err)
		}
		data["TrackerProducts"] = products
	}
	return data, nil
}

func (s *Server) renderMicSetup(w http.ResponseWriter, r *http.Request, status int, p *store.Production, data map[string]any) {
	data, err := s.micSetupData(r, p, data)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, status, "mics/setup", p, "mics", data)
}

func (s *Server) handleMicSetup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	s.renderMicSetup(w, r, http.StatusOK, p, map[string]any{})
}

func readMic(r *http.Request, m *store.Mic) []string {
	m.Channel, m.Name, m.Kind, m.Notes = formStr(r, "channel"), formStr(r, "name"), formStr(r, "kind"), formStr(r, "notes")
	var errs []string
	if m.Name == "" {
		errs = append(errs, "Give the mic a name, like Lav 3 or Handheld A.")
	}
	if !slices.Contains(store.MicKinds, m.Kind) {
		errs = append(errs, "Pick what kind of mic it is.")
	}
	return errs
}

func (s *Server) handleMicCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	m := store.Mic{ProductionID: p.ID}
	if errs := readMic(r, &m); errs != nil {
		s.renderMicSetup(w, r, http.StatusUnprocessableEntity, p, map[string]any{"Form": m, "Errors": errs})
		return
	}
	if _, err := s.store.CreateMic(&m); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/mics/setup"), m.Label()+" added.")
}

// guessMicKind works out lav, headset or handheld from a product's name.
func guessMicKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "headset") || strings.Contains(n, "head-worn") || strings.Contains(n, "earset"):
		return "headset"
	case strings.Contains(n, "handheld") || strings.Contains(n, "hand-held") || strings.Contains(n, "hand held"):
		return "handheld"
	case strings.Contains(n, "lav") || strings.Contains(n, "lapel"):
		return "lav"
	default:
		return "other"
	}
}

// handleMicImport adds every unit of a tracker product (e.g. "Lapel mics")
// as a mic, named by the ID on its sticker. Units already added are skipped.
func (s *Server) handleMicImport(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	t := s.Tracker()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	fail := func(err error) {
		s.setFlash(w, r, "error", trackerErr(err))
		http.Redirect(w, r, prodURL(p.ID, "/mics/setup"), http.StatusSeeOther)
	}
	product, err := t.Product(ctx, formInt(r, "product_id"))
	if err != nil {
		fail(err)
		return
	}
	kind := guessMicKind(product.Name)
	added := 0
	add := func(itemID int64, unit string) error {
		if s.store.HasTrackerMic(p.ID, itemID, unit) {
			return nil
		}
		m := store.Mic{ProductionID: p.ID, Name: product.Name + " " + unit, Kind: kind, TrackerItemID: itemID, TrackerUnit: unit}
		_, err := s.store.CreateMic(&m)
		if err == nil {
			added++
		}
		return err
	}
	for _, it := range product.Items {
		if it.Counted {
			for i := 1; i <= it.Quantity; i++ {
				if err := add(it.ID, strconv.Itoa(i)); err != nil {
					s.serverError(w, r, err)
					return
				}
			}
			continue
		}
		item, err := t.Item(ctx, it.ID)
		if err != nil {
			fail(err)
			return
		}
		for _, u := range item.Units {
			if err := add(item.ID, u.ID); err != nil {
				s.serverError(w, r, err)
				return
			}
		}
	}
	msg := pluralize(added, "mic") + " added from " + product.Name + "."
	if added == 0 {
		msg = "Every " + product.Name + " unit is already on the list."
	}
	s.redirect(w, r, prodURL(p.ID, "/mics/setup"), msg+" Set their channels by editing them.")
}

func (s *Server) micAndProduction(w http.ResponseWriter, r *http.Request) (*store.Mic, *store.Production, bool) {
	m, err := s.store.GetMic(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	p, ok := s.productionByID(w, r, m.ProductionID)
	return m, p, ok
}

func (s *Server) handleMicEdit(w http.ResponseWriter, r *http.Request) {
	m, p, ok := s.micAndProduction(w, r)
	if !ok {
		return
	}
	s.prodPage(w, r, http.StatusOK, "mics/form", p, "mics", map[string]any{"Form": *m, "Kinds": store.MicKinds,
		"TrackerLink": s.Tracker().PageURL("items", m.TrackerItemID)})
}

func (s *Server) handleMicUpdate(w http.ResponseWriter, r *http.Request) {
	m, p, ok := s.micAndProduction(w, r)
	if !ok {
		return
	}
	if errs := readMic(r, m); errs != nil {
		s.prodPage(w, r, http.StatusUnprocessableEntity, "mics/form", p, "mics", map[string]any{"Form": *m, "Kinds": store.MicKinds, "Errors": errs})
		return
	}
	if err := s.store.UpdateMic(m); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/mics/setup"), "Saved.")
}

func (s *Server) handleMicDelete(w http.ResponseWriter, r *http.Request) {
	m, p, ok := s.micAndProduction(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMic(m.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/mics/setup"), m.Label()+" removed.")
}
