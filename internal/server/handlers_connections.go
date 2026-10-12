package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/calendar"
)

// Connections page -------------------------------------------------------
//
// One section per entry in connections. To add a connection, give it an
// entry in the list and a Check; the template is generic.

type connFieldView struct {
	Label, Key, ID, Hint string
	Value                string // shown for plain fields; never for secrets
	Secret, URL, IsSet   bool
	Tail                 string // "…a1b2" for a secret that's set
}

type connView struct {
	Name, Title, Intro string
	FromEnv            bool
	SourceLabel        string
	Fields             []connFieldView
	ChangedBy          string
	ChangedAt          string
	Unreadable         bool     // the saved secret can't be decrypted
	Match, Differ      bool     // env only: settings equal / differ from the server's values
	CanKeep            bool     // env only: settings can be filled from the server's values
	Errors             []string // from the last save
}

func secretTail(v string) string {
	if len(v) < 8 {
		return ""
	}
	return "…" + v[len(v)-4:]
}

// connViews builds the sections. posted holds what was typed into the
// section named postedName when a save came back with errors.
func (s *Server) connViews(postedName string, posted url.Values, errs []string) []connView {
	var out []connView
	for _, c := range connections {
		cur := s.resolve(c)
		v := connView{Name: c.Name, Title: c.Title, Intro: c.Intro, FromEnv: cur.Source == sourceEnv}
		switch cur.Source {
		case sourceEnv:
			v.SourceLabel = "Set on the server"
		case sourceSettings:
			v.SourceLabel = "Set here"
		default:
			v.SourceLabel = "Not connected"
		}
		v.Unreadable = cur.Err != nil
		if by := s.store.Setting("conn_"+c.Name+"_changed_by", ""); by != "" {
			v.ChangedBy = by
			if t, err := time.Parse(time.RFC3339, s.store.Setting("conn_"+c.Name+"_changed_at", "")); err == nil {
				v.ChangedAt = t.In(time.Local).Format("Jan 2, 2006 at 3:04 pm")
			}
		}
		for _, f := range c.Fields {
			fv := connFieldView{Label: f.Label, Key: f.Key, ID: c.Name + "_" + f.Key, Hint: f.Hint, Secret: f.Secret, URL: f.URL}
			val := cur.Get(f.Key)
			if f.Secret {
				fv.IsSet = val != ""
				if fv.IsSet {
					fv.Tail = secretTail(val)
				}
			} else {
				fv.Value = val
				if c.Name == postedName && posted != nil {
					fv.Value = posted.Get(f.Key)
				}
			}
			v.Fields = append(v.Fields, fv)
		}
		if v.FromEnv {
			sv := s.saved(c)
			v.CanKeep = true
			if sv.Source != sourceOff {
				v.Match = true
				for _, f := range c.Fields {
					if sv.Get(f.Key) != cur.Get(f.Key) {
						v.Match = false
					}
				}
				v.Differ = !v.Match
			}
		}
		if c.Name == postedName {
			v.Errors = errs
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, status int, postedName string, posted url.Values, errs []string) {
	cal, err := s.calendarBackfillView()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, status, "connections", map[string]any{
		"Title": "Connections", "Conns": s.connViews(postedName, posted, errs), "Cal": cal,
	})
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	s.renderConnections(w, r, http.StatusOK, "", nil, nil)
}

// connFromPath finds the connection a URL names, or answers 404.
func (s *Server) connFromPath(w http.ResponseWriter, r *http.Request) *connection {
	for _, c := range connections {
		if c.Name == r.PathValue("name") {
			return c
		}
	}
	s.notFound(w, r)
	return nil
}

func connBack(c *connection) string { return "/admin/connections#" + c.Name }

// testConnection asks the other app and answers in plain words. ok is
// false when something is wrong; a connection that isn't filled in yet is
// reported but isn't a failure.
func (s *Server) testConnection(ctx context.Context, c *connection) (ok bool, msg string) {
	v := s.resolve(c)
	if v.Err != nil {
		return false, "Can't read the saved secret: enter it again."
	}
	// Complete means the address (first field) and every secret are filled in.
	complete := v.Get(c.Fields[0].Key) != ""
	for _, f := range c.Fields {
		if f.Secret && v.Get(f.Key) == "" {
			complete = false
		}
	}
	if !complete {
		return true, "Not connected yet: it needs an address and a token."
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	err := c.Check(ctx, v)
	if err == nil {
		return true, "Connected to " + c.Noun + "."
	}
	var ce *calendar.Error
	switch {
	case errors.As(err, &ce) && (ce.Status == http.StatusUnauthorized || ce.Status == http.StatusForbidden),
		strings.Contains(err.Error(), "turned down"):
		return false, "The token was refused: " + err.Error()
	case strings.Contains(err.Error(), "couldn't reach"):
		return false, fmt.Sprintf("Can't reach %s: %s", c.Address(v), err.Error())
	}
	return false, "Couldn't check " + c.Noun + ": " + err.Error()
}

func (s *Server) flashResult(w http.ResponseWriter, r *http.Request, prefix string, ok bool, msg string) {
	kind := "success"
	if !ok {
		kind = "error"
	}
	s.setFlash(w, r, kind, prefix+msg)
}

func (s *Server) handleConnectionSave(w http.ResponseWriter, r *http.Request) {
	c := s.connFromPath(w, r)
	if c == nil {
		return
	}
	if s.fromEnv(c) {
		s.setFlash(w, r, "error", c.Title+" is set on the server, so it can't be changed here.")
		http.Redirect(w, r, connBack(c), http.StatusSeeOther)
		return
	}
	values := map[string]string{}
	remove := map[string]bool{}
	var errs []string
	for _, f := range c.Fields {
		v := formStr(r, f.Key)
		switch {
		case f.URL:
			n, err := normalizeURL(v)
			if err != nil {
				errs = append(errs, f.Label+" "+err.Error())
			}
			v = n
		case f.Secret:
			remove[f.Key] = r.PostFormValue("remove_"+f.Key) == "1"
			if v != "" && f.Prefix != "" && !strings.HasPrefix(v, f.Prefix) {
				errs = append(errs, fmt.Sprintf("%s should start with %s. Make a new one in %s.", f.Label, f.Prefix, c.Noun))
			}
		}
		values[f.Key] = v
	}
	if errs != nil {
		s.renderConnections(w, r, http.StatusUnprocessableEntity, c.Name, r.PostForm, errs)
		return
	}
	if err := s.saveConnection(c, values, remove, currentUser(r).Username); err != nil {
		s.serverError(w, r, err)
		return
	}
	// Saved even if the test fails, so an app that isn't running yet can be set up.
	ok, msg := s.testConnection(r.Context(), c)
	s.flashResult(w, r, "Saved. ", ok, msg)
	http.Redirect(w, r, connBack(c), http.StatusSeeOther)
}

func (s *Server) handleConnectionTest(w http.ResponseWriter, r *http.Request) {
	c := s.connFromPath(w, r)
	if c == nil {
		return
	}
	ok, msg := s.testConnection(r.Context(), c)
	s.flashResult(w, r, "", ok, msg)
	http.Redirect(w, r, connBack(c), http.StatusSeeOther)
}

// handleConnectionKeep copies the server's values into settings, so the
// server's variables can be blanked without losing the connection.
func (s *Server) handleConnectionKeep(w http.ResponseWriter, r *http.Request) {
	c := s.connFromPath(w, r)
	if c == nil {
		return
	}
	if !s.fromEnv(c) {
		s.setFlash(w, r, "error", c.Title+" isn't set on the server; there's nothing to keep.")
		http.Redirect(w, r, connBack(c), http.StatusSeeOther)
		return
	}
	cur := s.resolve(c)
	values := map[string]string{}
	remove := map[string]bool{}
	for _, f := range c.Fields {
		values[f.Key] = cur.Get(f.Key)
		remove[f.Key] = cur.Get(f.Key) == "" && f.Secret
	}
	if err := s.storeConnection(c, values, remove, currentUser(r).Username); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.setFlash(w, r, "success", "Kept in settings. They match the server's values, so it's safe to remove them from the server.")
	http.Redirect(w, r, connBack(c), http.StatusSeeOther)
}
