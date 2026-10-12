package server

import (
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

func (s *Server) settingsData(data map[string]any) map[string]any {
	data["Title"] = "Settings"
	if _, ok := data["SiteNameValue"]; !ok {
		data["SiteNameValue"] = s.SiteName()
	}
	return data
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "settings", s.settingsData(map[string]any{}))
}

// handleSettingsUpdate saves the site name. Connections have their own
// page; tracker fields posted from an old Settings form are ignored.
func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	name := formStr(r, "site_name")
	if name == "" {
		s.render(w, r, http.StatusUnprocessableEntity, "settings", s.settingsData(map[string]any{
			"Errors": []string{"Site name is required."}, "SiteNameValue": name,
		}))
		return
	}
	if err := s.store.SetSetting("site_name", name); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.reloadSettings()
	if r.PostForm.Has("tracker_url") || r.PostForm.Has("tracker_token") {
		s.redirect(w, r, "/admin/connections#tracker", "Site name saved. The maintenance tracker is set on Connections now.")
		return
	}
	s.redirect(w, r, "/admin/settings", "Settings saved.")
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
		return "", errors.New("should look like https://example.org")
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
