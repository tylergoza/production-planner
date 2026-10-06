package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/tracker"
)

// Settings & backup ------------------------------------------------------

func (s *Server) settingsData(r *http.Request, data map[string]any) map[string]any {
	url, token := s.trackerSettings()
	data["Title"] = "Settings"
	data["TrackerURLFromEnv"] = s.cfg.TrackerURL != ""
	data["TrackerTokenFromEnv"] = s.cfg.TrackerToken != ""
	data["TrackerTokenSet"] = token != ""
	if _, ok := data["SiteNameValue"]; !ok {
		data["SiteNameValue"] = s.SiteName()
	}
	if _, ok := data["TrackerURLValue"]; !ok {
		data["TrackerURLValue"] = url
	}
	return data
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "settings", s.settingsData(r, map[string]any{}))
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "site_name")
	trackerURL, urlErr := normalizeURL(formStr(r, "tracker_url"))
	token := formStr(r, "tracker_token")
	var errs []string
	if name == "" {
		errs = append(errs, "Site name is required.")
	}
	if urlErr != nil {
		errs = append(errs, urlErr.Error())
	}
	if token != "" && !strings.HasPrefix(token, "mt_") {
		errs = append(errs, "API tokens from the maintenance tracker start with mt_. Make one there under API.")
	}
	if errs != nil {
		s.render(w, r, http.StatusUnprocessableEntity, "settings", s.settingsData(r, map[string]any{
			"Errors": errs, "SiteNameValue": name, "TrackerURLValue": formStr(r, "tracker_url"),
		}))
		return
	}
	set := map[string]string{"site_name": name}
	if s.cfg.TrackerURL == "" {
		set["tracker_url"] = trackerURL
	}
	if s.cfg.TrackerToken == "" {
		if token != "" {
			set["tracker_token"] = token
		}
		if r.PostFormValue("clear_token") == "1" {
			set["tracker_token"] = ""
		}
	}
	for k, v := range set {
		if err := s.store.SetSetting(k, v); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.reloadSettings()

	msg := "Settings saved."
	if t := s.Tracker(); t.Configured() {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := t.Check(ctx); err != nil {
			s.setFlash(w, r, "error", "Settings saved, but the maintenance tracker didn't answer: "+err.Error())
			http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
			return
		}
		msg = "Settings saved. Connected to the maintenance tracker."
	}
	s.redirect(w, r, "/admin/settings", msg)
}

// normalizeURL checks an address and trims it to "scheme://host[:port][/path]"
// with no trailing slash. Blank is allowed.
func normalizeURL(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("The maintenance tracker's address should look like https://maintenance.example.org")
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/"), nil
}

// handleBackup streams a consistent snapshot of the database. Restoring is
// just replacing the .db file on the target host while the app is stopped.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	dir, err := os.MkdirTemp("", "pp-backup-")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer os.RemoveAll(dir)
	dest := filepath.Join(dir, "backup.db")
	if err := s.store.Backup(r.Context(), dest); err != nil {
		s.serverError(w, r, err)
		return
	}
	f, err := os.Open(dest)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	defer f.Close()
	name := "productions-" + time.Now().Format("2006-01-02-1504") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	io.Copy(w, f)
}

// trackerErr turns a tracker failure into something to show on a page.
func trackerErr(err error) string {
	if errors.Is(err, tracker.ErrNotConfigured) {
		return err.Error() + "."
	}
	return err.Error()
}
