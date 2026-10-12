package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/tylergoza/production-planner/internal/calendar"
	"github.com/tylergoza/production-planner/internal/tracker"
)

// Connections to other apps (see CONNECTIONS_PLAN.md). Each one comes as a
// whole from the environment when any of its variables is set there,
// otherwise from the settings an admin saved, otherwise it's off. Single
// sign-on is still environment-only here (CONNECTIONS_PLAN.md step 3).

type connField struct {
	Label  string
	Env    string // environment variable that overrides it
	Key    string // settings key
	Secret bool   // sealed in settings and never shown
	URL    bool   // an address, checked and tidied on save
	Prefix string // what a valid secret starts with ("mt_")
	Hint   string // placeholder / help for the page
}

type connection struct {
	Name   string // "tracker", "calendar"
	Title  string
	Noun   string // "the calendar", for messages
	Intro  string // what it's for and how to set it up, for the page
	Fields []connField
	// Check asks the other app whether the values work; Address is what to
	// name when it can't be reached. Both take the connection's values.
	Check   func(ctx context.Context, v resolvedConn) error
	Address func(v resolvedConn) string
}

var (
	connTracker = &connection{Name: "tracker", Title: "Maintenance tracker", Noun: "the maintenance tracker",
		Intro: "Pick items, supplies, mics and rooms from the maintenance tracker and see how many there are. In the tracker, an admin opens API, makes a token for \"Production planner\", and pastes it here.",
		Fields: []connField{
			{Label: "Address", Env: "TRACKER_URL", Key: "tracker_url", URL: true, Hint: "https://maintenance.example.org"},
			{Label: "API token", Env: "TRACKER_TOKEN", Key: "tracker_token", Secret: true, Prefix: "mt_", Hint: "mt_…"},
		},
		Check: func(ctx context.Context, v resolvedConn) error {
			return tracker.New(v.Get("tracker_url"), v.Get("tracker_token")).Check(ctx)
		},
		Address: func(v resolvedConn) string { return v.Get("tracker_url") },
	}
	connCalendar = &connection{Name: "calendar", Title: "Calendar", Noun: "the calendar",
		Intro: "Send every rehearsal and performance, with its rooms, to the church calendar, and see room clashes with the other apps. On the calendar, an admin makes a source token for \"productions\" on its API tokens page and pastes it here.",
		Fields: []connField{
			{Label: "Address", Env: "CALENDAR_URL", Key: "calendar_url", URL: true, Hint: "https://calendar.example.org"},
			{Label: "Internal address", Env: "CALENDAR_INTERNAL_URL", Key: "calendar_internal_url", URL: true, Hint: "Optional, e.g. http://127.0.0.1:8120"},
			{Label: "Token", Env: "CALENDAR_TOKEN", Key: "calendar_token", Secret: true, Prefix: "cal_", Hint: "cal_…"},
		},
		Check: func(ctx context.Context, v resolvedConn) error {
			return calendar.New(v.Get("calendar_url"), v.Get("calendar_internal_url"), v.Get("calendar_token")).Check(ctx)
		},
		Address: func(v resolvedConn) string { return v.Get("calendar_url") },
	}
	// connections are the ones on /admin/connections, in order.
	connections = []*connection{connTracker, connCalendar}
)

// ConnectionEnv lists every environment variable a connection reads, for
// main to pass in Config.Env.
func ConnectionEnv() []string {
	var out []string
	for _, c := range connections {
		for _, f := range c.Fields {
			out = append(out, f.Env)
		}
	}
	return out
}

// ConnectionKeys lists the settings a connection keeps, including the
// record of who last changed it, for `connection clear`. nil if there's
// no such connection.
func ConnectionKeys(name string) []string {
	for _, c := range connections {
		if c.Name == name {
			var out []string
			for _, f := range c.Fields {
				out = append(out, f.Key)
			}
			return append(out, "conn_"+name+"_changed_by", "conn_"+name+"_changed_at")
		}
	}
	return nil
}

type connSource string

const (
	sourceOff      connSource = "off"
	sourceEnv      connSource = "env"
	sourceSettings connSource = "settings"
)

// resolvedConn is a connection's values (by settings key) and where they
// came from. Err is set when a saved secret can't be read; that field is
// then blank.
type resolvedConn struct {
	Source connSource
	Values map[string]string
	Err    error
}

func (r resolvedConn) Get(key string) string { return r.Values[key] }

func (s *Server) env(name string) string { return strings.TrimSpace(s.cfg.Env[name]) }

// fromEnv reports whether any of c's variables is set in the environment.
func (s *Server) fromEnv(c *connection) bool {
	for _, f := range c.Fields {
		if s.env(f.Env) != "" {
			return true
		}
	}
	return false
}

// resolve works out c's values: all from the environment if any of them is
// set there (never mixed with settings), else from settings.
func (s *Server) resolve(c *connection) resolvedConn {
	r := resolvedConn{Source: sourceOff, Values: map[string]string{}}
	if s.fromEnv(c) {
		r.Source = sourceEnv
		for _, f := range c.Fields {
			r.Values[f.Key] = s.env(f.Env)
		}
		return r
	}
	return s.saved(c)
}

// saved is c's values from settings, whatever the environment says.
func (s *Server) saved(c *connection) resolvedConn {
	r := resolvedConn{Source: sourceOff, Values: map[string]string{}}
	for _, f := range c.Fields {
		v := s.store.Setting(f.Key, "")
		if f.Secret {
			var err error
			if v, err = s.store.SecretSetting(f.Key); err != nil {
				r.Err = err
				v = ""
			}
		}
		if v != "" || (f.Secret && r.Err != nil) {
			r.Source = sourceSettings
		}
		r.Values[f.Key] = v
	}
	return r
}

// errFromEnv means a connection is set on the server and can't be changed
// in the app.
var errFromEnv = errors.New("this connection is set on the server")

// saveConnection stores c's settings from values (by settings key). A blank
// secret keeps the saved one; keys in remove are cleared. Logs who changed
// which fields, never the values.
func (s *Server) saveConnection(c *connection, values map[string]string, remove map[string]bool, by string) error {
	if s.fromEnv(c) {
		return errFromEnv
	}
	return s.storeConnection(c, values, remove, by)
}

// storeConnection is saveConnection without the environment guard, for
// copying the server's values into settings (Keep in settings).
func (s *Server) storeConnection(c *connection, values map[string]string, remove map[string]bool, by string) error {
	var changed []string
	for _, f := range c.Fields {
		v := strings.TrimSpace(values[f.Key])
		var err error
		switch {
		case remove[f.Key]:
			if s.store.Setting(f.Key, "") == "" {
				continue
			}
			err = s.store.SetSetting(f.Key, "")
		case f.Secret:
			if v == "" {
				continue
			}
			if old, _ := s.store.SecretSetting(f.Key); old == v {
				continue
			}
			err = s.store.SetSecretSetting(f.Key, v)
		default:
			if _, ok := values[f.Key]; !ok || s.store.Setting(f.Key, "") == v {
				continue
			}
			err = s.store.SetSetting(f.Key, v)
		}
		if err != nil {
			return err
		}
		changed = append(changed, f.Key)
	}
	if len(changed) > 0 {
		s.log.Info("connection changed", slog.String("connection", c.Name), slog.String("by", by), slog.Any("fields", changed))
		s.store.SetSetting("conn_"+c.Name+"_changed_by", by)
		s.store.SetSetting("conn_"+c.Name+"_changed_at", time.Now().UTC().Format(time.RFC3339))
	}
	s.reloadSettings()
	return nil
}
