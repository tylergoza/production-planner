// Package server implements the HTTP interface: routing, sessions, HTML
// rendering and request handlers.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tylergoza/production-planner/internal/store"
	"github.com/tylergoza/production-planner/internal/tracker"
	"github.com/tylergoza/production-planner/web"
)

type Config struct {
	// Dev serves templates and static files from ./web on disk and
	// re-parses templates on every request, so edits show up on refresh.
	Dev bool
	// TrustProxy uses X-Forwarded-For / X-Forwarded-Proto from a reverse
	// proxy (Caddy, nginx, a DO load balancer) for client IP and HTTPS.
	TrustProxy bool
	SessionTTL time.Duration
	// TrackerURL and TrackerToken, when set, override the maintenance
	// tracker connection in Settings (e.g. from the environment).
	TrackerURL   string
	TrackerToken string
}

type Server struct {
	cfg    Config
	store  *store.Store
	log    *slog.Logger
	webFS  fs.FS
	static fs.FS

	// Hash of all static assets, used for cache busting and the service
	// worker cache name.
	assetVersion string
	importMap    template.HTML
	csp          string

	tmplMu sync.Mutex
	tmpls  map[string]*template.Template

	siteName atomic.Value
	tracker  atomic.Pointer[tracker.Client]

	limiter *loginLimiter
	handler http.Handler
}

func New(cfg Config, st *store.Store, logger *slog.Logger) (*Server, error) {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 30 * 24 * time.Hour
	}
	s := &Server{cfg: cfg, store: st, log: logger, limiter: newLoginLimiter(10, 15*time.Minute)}
	if cfg.Dev {
		s.webFS = os.DirFS("web")
	} else {
		s.webFS = web.FS
	}
	var err error
	if s.static, err = fs.Sub(s.webFS, "static"); err != nil {
		return nil, err
	}
	if err := s.computeAssets(); err != nil {
		return nil, err
	}
	s.reloadSettings()
	if !cfg.Dev {
		if err := s.parseTemplates(); err != nil {
			return nil, err
		}
	}
	s.handler = s.routes()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) reloadSettings() {
	s.siteName.Store(s.store.Setting("site_name", "Production Planner"))
	url, token := s.trackerSettings()
	s.tracker.Store(tracker.New(url, token))
}

// trackerSettings is the maintenance tracker's address and API token:
// from the environment when set there, otherwise from Settings.
func (s *Server) trackerSettings() (url, token string) {
	url, token = s.cfg.TrackerURL, s.cfg.TrackerToken
	if url == "" {
		url = s.store.Setting("tracker_url", "")
	}
	if token == "" {
		token = s.store.Setting("tracker_token", "")
	}
	return url, token
}

func (s *Server) SiteName() string { return s.siteName.Load().(string) }

// Tracker is the maintenance tracker client; check Configured before use.
func (s *Server) Tracker() *tracker.Client { return s.tracker.Load() }

// computeAssets hashes the static tree and builds the import map. The
// import map is an inline script, so its hash goes into the CSP.
func (s *Server) computeAssets() error {
	h := sha256.New()
	var files []string
	err := fs.WalkDir(s.static, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := fs.ReadFile(s.static, f)
		if err != nil {
			return err
		}
		h.Write([]byte(f))
		h.Write(b)
	}
	s.assetVersion = hex.EncodeToString(h.Sum(nil))[:12]

	im, err := json.MarshalIndent(map[string]any{
		"imports": map[string]string{
			"@hotwired/stimulus":  s.asset("js/vendor/stimulus.js"),
			"stimulus-autoloader": s.asset("js/stimulus_autoloader.js"),
		},
	}, "", "  ")
	if err != nil {
		return err
	}
	// Emitted verbatim as a whole tag: html/template would otherwise
	// JS-escape it inside the <script> element.
	s.importMap = template.HTML(`<script type="importmap">` + string(im) + `</script>`)
	sum := sha256.Sum256(im)
	s.csp = strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'",
		"style-src 'self'",
		"img-src 'self' data:",
		"connect-src 'self'",
		"manifest-src 'self'",
		"worker-src 'self'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
	}, "; ")
	return nil
}

// asset returns the versioned URL for a file under web/static.
func (s *Server) asset(p string) string {
	return "/static/" + strings.TrimPrefix(p, "/") + "?v=" + s.assetVersion
}

// Templates --------------------------------------------------------------

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"asset":                 s.asset,
		"relDays":               s.relDays,
		"fmtDate":               fmtDate,
		"fmtTime":               fmtTime,
		"today":                 func() string { return s.today() },
		"dict":                  dict,
		"add":                   func(a, b int) int { return a + b },
		"pluralize":             pluralize,
		"lines":                 func(t string) []string { return strings.Split(strings.TrimSpace(t), "\n") },
		"productionStatusLabel": store.ProductionStatusLabel,
		"eventKindLabel":        store.EventKindLabel,
		"micKindLabel":          store.MicKindLabel,
		"deptLabel":             store.DepartmentLabel,
		"prepStatusLabel":       store.PrepStatusLabel,
		"needStatusLabel":       store.NeedStatusLabel,
		"overdue":               func(date string) bool { return date != "" && date < s.today() },
	}
}

func (s *Server) parseTemplates() error {
	pages, err := fs.Glob(s.webFS, "templates/pages/*.html")
	if err != nil {
		return err
	}
	sub, _ := fs.Glob(s.webFS, "templates/pages/*/*.html")
	pages = append(pages, sub...)
	shared := []string{"templates/layout.html", "templates/partials/*.html"}
	tmpls := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "templates/pages/"), ".html")
		t, err := template.New("layout.html").Funcs(s.funcs()).ParseFS(s.webFS, append(shared, p)...)
		if err != nil {
			return fmt.Errorf("parse %s: %w", p, err)
		}
		tmpls[name] = t
	}
	s.tmplMu.Lock()
	s.tmpls = tmpls
	s.tmplMu.Unlock()
	return nil
}

func (s *Server) template(name string) (*template.Template, error) {
	if s.cfg.Dev {
		if err := s.parseTemplates(); err != nil {
			return nil, err
		}
	}
	s.tmplMu.Lock()
	defer s.tmplMu.Unlock()
	t, ok := s.tmpls[name]
	if !ok {
		return nil, fmt.Errorf("no template %q", name)
	}
	return t, nil
}

type flash struct {
	Kind    string // "success" | "error" | "info"
	Message string
}

// render executes a page inside the layout. Every page gets the common
// keys (User, CSRF, Flash, SiteName, ...) merged into data.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["User"] = currentUser(r)
	data["CSRF"] = csrfToken(r)
	data["SiteName"] = s.SiteName()
	data["ImportMap"] = s.importMap
	data["AssetVersion"] = s.assetVersion
	data["Path"] = r.URL.Path
	data["URI"] = r.URL.RequestURI()
	if f := s.popFlash(w, r); f != nil {
		data["Flash"] = f
	}
	if _, ok := data["Title"]; !ok {
		data["Title"] = ""
	}
	t, err := s.template(page)
	if err != nil {
		s.log.Error("template lookup", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	// Render into a buffer so a template error doesn't produce half a page.
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.log.Error("template exec", "page", page, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write([]byte(buf.String()))
}

func (s *Server) setFlash(w http.ResponseWriter, r *http.Request, kind, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: "pp_flash", Value: base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + msg)),
		Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.isHTTPS(r), MaxAge: 60,
	})
}

func (s *Server) popFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie("pp_flash")
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: "pp_flash", Path: "/", MaxAge: -1})
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	kind, msg, ok := strings.Cut(string(b), "|")
	if !ok {
		return nil
	}
	return &flash{Kind: kind, Message: msg}
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to, flashMsg string) {
	if flashMsg != "" {
		s.setFlash(w, r, "success", flashMsg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// Errors -----------------------------------------------------------------

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	s.render(w, r, http.StatusInternalServerError, "error", map[string]any{
		"Title": "Something went wrong", "Message": "An unexpected error occurred. It has been logged.",
	})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "error", map[string]any{
		"Title": "Not found", "Message": "We couldn't find what you were looking for.",
	})
}

// Dates ------------------------------------------------------------------

// today is the current date in the server's local time zone (set TZ).
func (s *Server) today() string { return time.Now().Format(store.DateLayout) }

func (s *Server) todayTime() time.Time {
	t, _ := time.Parse(store.DateLayout, s.today())
	return t
}

func (s *Server) relDays(date string) string {
	d, err := time.Parse(store.DateLayout, date)
	if err != nil {
		return ""
	}
	n := int(d.Sub(s.todayTime()).Hours() / 24)
	switch {
	case n == 0:
		return "today"
	case n == 1:
		return "tomorrow"
	case n == -1:
		return "yesterday"
	case n > 1:
		return "in " + humanDays(n)
	default:
		return humanDays(-n) + " ago"
	}
}

func humanDays(n int) string {
	switch {
	case n < 14:
		return pluralize(n, "day")
	case n < 60:
		return pluralize(n/7, "week")
	case n < 730:
		return pluralize(n/30, "month")
	default:
		return pluralize(n/365, "year")
	}
}

func fmtDate(date string) string {
	d, err := time.Parse(store.DateLayout, date)
	if err != nil {
		return date
	}
	return d.Format("Jan 2, 2006")
}

// fmtTime renders a SQLite UTC timestamp ("YYYY-MM-DD HH:MM:SS") in local time.
func fmtTime(ts string) string {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", ts, time.UTC)
	if err != nil {
		return ts
	}
	return t.Local().Format("Jan 2, 2006 3:04 PM")
}

func pluralize(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func dict(kv ...any) (map[string]any, error) {
	if len(kv)%2 != 0 {
		return nil, errors.New("dict needs key/value pairs")
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			return nil, errors.New("dict keys must be strings")
		}
		m[k] = kv[i+1]
	}
	return m, nil
}

// Form helpers -----------------------------------------------------------

func formStr(r *http.Request, key string) string { return strings.TrimSpace(r.PostFormValue(key)) }

func formInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue(key)), 10, 64)
	return n
}

func queryInt(r *http.Request, key string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	return n
}

func pathID(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n
}

func validDate(d string) bool {
	_, err := time.Parse(store.DateLayout, d)
	return err == nil
}

func validTime(t string) bool {
	_, err := time.Parse("15:04", t)
	return err == nil
}

// Context ----------------------------------------------------------------

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxSession
	ctxCSRF
)

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

func csrfToken(r *http.Request) string {
	t, _ := r.Context().Value(ctxCSRF).(string)
	return t
}

func withValue(r *http.Request, k ctxKey, v any) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), k, v))
}

// safeRedirect only allows local paths, preventing open redirects.
func safeRedirect(to string) string {
	if to == "" || !strings.HasPrefix(to, "/") || strings.HasPrefix(to, "//") || strings.HasPrefix(to, "/\\") {
		return "/"
	}
	return path.Clean(to)
}
