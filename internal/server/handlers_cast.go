package server

import (
	"net/http"

	"github.com/tylergoza/production-planner/internal/store"
)

// Cast & scenes share a page: who's in it, and the running order.

func (s *Server) castData(p *store.Production, data map[string]any) (map[string]any, error) {
	cast, err := s.store.ListCast(p.ID)
	if err != nil {
		return nil, err
	}
	scenes, err := s.store.ListScenes(p.ID)
	if err != nil {
		return nil, err
	}
	data["Cast"] = cast
	data["Scenes"] = scenes
	if _, ok := data["CastForm"]; !ok {
		data["CastForm"] = store.CastMember{}
	}
	if _, ok := data["SceneForm"]; !ok {
		data["SceneForm"] = store.Scene{}
	}
	data["Title"] = "Cast & scenes · " + p.Title
	return data, nil
}

func (s *Server) renderCast(w http.ResponseWriter, r *http.Request, status int, p *store.Production, data map[string]any) {
	data, err := s.castData(p, data)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.prodPage(w, r, status, "productions/cast", p, "cast", data)
}

func (s *Server) handleCast(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	s.renderCast(w, r, http.StatusOK, p, map[string]any{})
}

func readCast(r *http.Request, c *store.CastMember) []string {
	c.Character, c.Person, c.Notes = formStr(r, "character"), formStr(r, "person"), formStr(r, "notes")
	if c.Character == "" && c.Person == "" {
		return []string{"Fill in the character, the person, or both."}
	}
	return nil
}

func (s *Server) handleCastCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	c := store.CastMember{ProductionID: p.ID}
	if errs := readCast(r, &c); errs != nil {
		s.renderCast(w, r, http.StatusUnprocessableEntity, p, map[string]any{"CastForm": c, "CastErrors": errs})
		return
	}
	if _, err := s.store.CreateCastMember(&c); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#cast"), c.Label()+" added.")
}

func (s *Server) handleCastPaste(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	n, err := s.store.AddCastList(p.ID, r.PostFormValue("list"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#cast"), pluralize(n, "cast member")+" added.")
}

func (s *Server) castAndProduction(w http.ResponseWriter, r *http.Request) (*store.CastMember, *store.Production, bool) {
	c, err := s.store.GetCastMember(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	p, ok := s.productionByID(w, r, c.ProductionID)
	return c, p, ok
}

func (s *Server) handleCastEdit(w http.ResponseWriter, r *http.Request) {
	c, p, ok := s.castAndProduction(w, r)
	if !ok {
		return
	}
	s.prodPage(w, r, http.StatusOK, "cast/form", p, "cast", map[string]any{"Form": *c})
}

func (s *Server) handleCastUpdate(w http.ResponseWriter, r *http.Request) {
	c, p, ok := s.castAndProduction(w, r)
	if !ok {
		return
	}
	if errs := readCast(r, c); errs != nil {
		s.prodPage(w, r, http.StatusUnprocessableEntity, "cast/form", p, "cast", map[string]any{"Form": *c, "Errors": errs})
		return
	}
	if err := s.store.UpdateCastMember(c); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#cast"), "Saved.")
}

func (s *Server) handleCastDelete(w http.ResponseWriter, r *http.Request) {
	c, p, ok := s.castAndProduction(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteCastMember(c.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#cast"), c.Label()+" removed, along with their mics in the chart.")
}

// Scenes -----------------------------------------------------------------

func readScene(r *http.Request, sc *store.Scene) []string {
	sc.Number, sc.Title, sc.Notes = formStr(r, "number"), formStr(r, "title"), formStr(r, "notes")
	if sc.Number == "" && sc.Title == "" {
		return []string{"Give the scene a number, a title, or both."}
	}
	return nil
}

func (s *Server) handleSceneCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	sc := store.Scene{ProductionID: p.ID}
	if errs := readScene(r, &sc); errs != nil {
		s.renderCast(w, r, http.StatusUnprocessableEntity, p, map[string]any{"SceneForm": sc, "SceneErrors": errs})
		return
	}
	if _, err := s.store.CreateScene(&sc); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#scenes"), "Scene "+sc.FullLabel()+" added.")
}

func (s *Server) handleScenePaste(w http.ResponseWriter, r *http.Request) {
	p, ok := s.production(w, r)
	if !ok {
		return
	}
	n, err := s.store.AddSceneList(p.ID, r.PostFormValue("list"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#scenes"), pluralize(n, "scene")+" added.")
}

func (s *Server) sceneAndProduction(w http.ResponseWriter, r *http.Request) (*store.Scene, *store.Production, bool) {
	sc, err := s.store.GetScene(pathID(r))
	if err != nil {
		s.serverError(w, r, err)
		return nil, nil, false
	}
	p, ok := s.productionByID(w, r, sc.ProductionID)
	return sc, p, ok
}

func (s *Server) handleSceneEdit(w http.ResponseWriter, r *http.Request) {
	sc, p, ok := s.sceneAndProduction(w, r)
	if !ok {
		return
	}
	s.prodPage(w, r, http.StatusOK, "scenes/form", p, "cast", map[string]any{"Form": *sc})
}

func (s *Server) handleSceneUpdate(w http.ResponseWriter, r *http.Request) {
	sc, p, ok := s.sceneAndProduction(w, r)
	if !ok {
		return
	}
	if errs := readScene(r, sc); errs != nil {
		s.prodPage(w, r, http.StatusUnprocessableEntity, "scenes/form", p, "cast", map[string]any{"Form": *sc, "Errors": errs})
		return
	}
	if err := s.store.UpdateScene(sc); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#scenes"), "Saved.")
}

func (s *Server) handleSceneDelete(w http.ResponseWriter, r *http.Request) {
	sc, p, ok := s.sceneAndProduction(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteScene(sc.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, prodURL(p.ID, "/cast#scenes"), "Scene "+sc.FullLabel()+" removed.")
}

// handleMove moves a scene, cast member or mic up or down its list, then
// goes back to the page it was moved from.
func (s *Server) handleMove(table string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Move(table, pathID(r), r.PostFormValue("dir") == "up"); err != nil {
			s.serverError(w, r, err)
			return
		}
		http.Redirect(w, r, safeRedirect(r.PostFormValue("next")), http.StatusSeeOther)
	}
}
