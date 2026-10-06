package store

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

// Production statuses, in the order a production goes through them.
var ProductionStatuses = []string{"planning", "rehearsing", "ready", "done", "cancelled"}

var productionStatusLabels = map[string]string{
	"planning": "Planning", "rehearsing": "Rehearsing", "ready": "Ready", "done": "Done", "cancelled": "Cancelled",
}

func ProductionStatusLabel(s string) string { return productionStatusLabels[s] }

type Production struct {
	ID          int64
	Title       string
	Kind        string
	Status      string
	Venue       string
	Director    string
	Description string
	Notes       string
	CreatedAt   string
	UpdatedAt   string
}

// Active reports whether the production is still being worked on.
func (p Production) Active() bool { return p.Status != "done" && p.Status != "cancelled" }

const productionCols = `id, title, kind, status, venue, director, description, notes, created_at, updated_at`

func scanProduction(sc interface{ Scan(...any) error }) (Production, error) {
	var p Production
	err := sc.Scan(&p.ID, &p.Title, &p.Kind, &p.Status, &p.Venue, &p.Director, &p.Description, &p.Notes, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) GetProduction(id int64) (*Production, error) {
	p, err := scanProduction(s.DB.QueryRow(`SELECT `+productionCols+` FROM productions WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

func (s *Store) CreateProduction(p *Production) (int64, error) {
	if p.Status == "" {
		p.Status = "planning"
	}
	res, err := s.DB.Exec(`INSERT INTO productions (title, kind, status, venue, director, description, notes) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Title, p.Kind, p.Status, p.Venue, p.Director, p.Description, p.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateProduction(p *Production) error {
	_, err := s.DB.Exec(`UPDATE productions SET title = ?, kind = ?, status = ?, venue = ?, director = ?, description = ?, notes = ?,
		updated_at = datetime('now') WHERE id = ?`,
		p.Title, p.Kind, p.Status, p.Venue, p.Director, p.Description, p.Notes, p.ID)
	return err
}

func (s *Store) DeleteProduction(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM productions WHERE id = ?`, id)
	return err
}

// ProductionSummary is a production with what the dashboard shows about it.
type ProductionSummary struct {
	Production
	FirstDate  string // first performance, or first date of any kind
	NextEvent  *Event // next date on or after today
	LastDate   string
	Prep       Tally
	Needs      Tally // gathered (or returned) out of all
	MicVersion int   // latest final version of the mic chart, 0 if none
	Scenes     int
	Cast       int
	Mics       int
}

// Tally counts how many of something are done.
type Tally struct{ Done, Total int }

func (t Tally) Percent() int {
	if t.Total == 0 {
		return 0
	}
	return t.Done * 100 / t.Total
}

// ListProductions returns productions with their summaries: active ones
// first by their next date, then the rest newest first.
func (s *Store) ListProductions(activeOnly bool, today string) ([]ProductionSummary, error) {
	q := `SELECT ` + productionCols + ` FROM productions`
	if activeOnly {
		q += ` WHERE status NOT IN ('done', 'cancelled')`
	}
	rows, err := s.DB.Query(q + ` ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	var out []ProductionSummary
	for rows.Next() {
		p, err := scanProduction(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, ProductionSummary{Production: p})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.summarize(&out[i], today); err != nil {
			return nil, err
		}
	}
	sortSummaries(out)
	return out, nil
}

func (s *Store) Summary(p *Production, today string) (*ProductionSummary, error) {
	sum := &ProductionSummary{Production: *p}
	return sum, s.summarize(sum, today)
}

func (s *Store) summarize(sum *ProductionSummary, today string) error {
	id := sum.ID
	err := s.DB.QueryRow(`SELECT
		COALESCE((SELECT MIN(date) FROM events WHERE production_id = ?1 AND kind = 'performance'), (SELECT MIN(date) FROM events WHERE production_id = ?1), ''),
		COALESCE((SELECT MAX(date) FROM events WHERE production_id = ?1), ''),
		(SELECT COUNT(*) FROM prep_items WHERE production_id = ?1 AND status = 'ready'),
		(SELECT COUNT(*) FROM prep_items WHERE production_id = ?1),
		(SELECT COUNT(*) FROM needs WHERE production_id = ?1 AND status <> 'needed'),
		(SELECT COUNT(*) FROM needs WHERE production_id = ?1),
		COALESCE((SELECT MAX(version) FROM mic_chart_versions WHERE production_id = ?1), 0),
		(SELECT COUNT(*) FROM scenes WHERE production_id = ?1),
		(SELECT COUNT(*) FROM cast_members WHERE production_id = ?1),
		(SELECT COUNT(*) FROM mics WHERE production_id = ?1)`, id).
		Scan(&sum.FirstDate, &sum.LastDate, &sum.Prep.Done, &sum.Prep.Total, &sum.Needs.Done, &sum.Needs.Total,
			&sum.MicVersion, &sum.Scenes, &sum.Cast, &sum.Mics)
	if err != nil {
		return err
	}
	e, err := scanEvent(s.DB.QueryRow(`SELECT `+eventCols+` FROM events WHERE production_id = ? AND date >= ? ORDER BY date, time LIMIT 1`, id, today))
	if err == nil {
		sum.NextEvent = &e
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// sortSummaries puts active productions first, soonest next date first
// (those with no upcoming date after them), then the finished ones.
func sortSummaries(list []ProductionSummary) {
	key := func(p ProductionSummary) string {
		switch {
		case !p.Active():
			return "3"
		case p.NextEvent != nil:
			return "1" + p.NextEvent.Date + p.NextEvent.Time
		default:
			return "2"
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return key(list[i]) < key(list[j]) })
}

// Events -----------------------------------------------------------------

var EventKinds = []string{"performance", "dress", "tech", "rehearsal", "other"}

var eventKindLabels = map[string]string{
	"performance": "Performance", "dress": "Dress rehearsal", "tech": "Tech rehearsal", "rehearsal": "Rehearsal", "other": "Other",
}

func EventKindLabel(k string) string { return eventKindLabels[k] }

type Event struct {
	ID           int64
	ProductionID int64
	Kind         string
	Date         string
	Time         string
	Label        string
	Notes        string
}

// When is the date and time for display, e.g. "Sun, Dec 14, 2026 · 6:00 PM".
func (e Event) When() string {
	d, err := time.Parse(DateLayout, e.Date)
	if err != nil {
		return e.Date
	}
	out := d.Format("Mon, Jan 2, 2006")
	if t, err := time.Parse("15:04", e.Time); err == nil {
		out += " · " + t.Format("3:04 PM")
	}
	return out
}

// Title is the label, or the kind when there isn't one.
func (e Event) Title() string {
	if e.Label != "" {
		return e.Label
	}
	return EventKindLabel(e.Kind)
}

const eventCols = `id, production_id, kind, date, time, label, notes`

func scanEvent(sc interface{ Scan(...any) error }) (Event, error) {
	var e Event
	err := sc.Scan(&e.ID, &e.ProductionID, &e.Kind, &e.Date, &e.Time, &e.Label, &e.Notes)
	return e, err
}

func (s *Store) ListEvents(productionID int64) ([]Event, error) {
	rows, err := s.DB.Query(`SELECT `+eventCols+` FROM events WHERE production_id = ? ORDER BY date, time, id`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpcomingEvents lists dates from today on across active productions.
type UpcomingEvent struct {
	Event
	ProductionTitle string
}

func (s *Store) UpcomingEvents(today string, limit int) ([]UpcomingEvent, error) {
	rows, err := s.DB.Query(`SELECT e.id, e.production_id, e.kind, e.date, e.time, e.label, e.notes, p.title
		FROM events e JOIN productions p ON p.id = e.production_id
		WHERE e.date >= ? AND p.status NOT IN ('done', 'cancelled') ORDER BY e.date, e.time, e.id LIMIT ?`, today, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpcomingEvent
	for rows.Next() {
		var e UpcomingEvent
		if err := rows.Scan(&e.ID, &e.ProductionID, &e.Kind, &e.Date, &e.Time, &e.Label, &e.Notes, &e.ProductionTitle); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetEvent(id int64) (*Event, error) {
	e, err := scanEvent(s.DB.QueryRow(`SELECT `+eventCols+` FROM events WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &e, nil
}

func (s *Store) CreateEvent(e *Event) (int64, error) {
	res, err := s.DB.Exec(`INSERT INTO events (production_id, kind, date, time, label, notes) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ProductionID, e.Kind, e.Date, e.Time, strings.TrimSpace(e.Label), e.Notes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateEvent(e *Event) error {
	_, err := s.DB.Exec(`UPDATE events SET kind = ?, date = ?, time = ?, label = ?, notes = ? WHERE id = ?`,
		e.Kind, e.Date, e.Time, strings.TrimSpace(e.Label), e.Notes, e.ID)
	return err
}

func (s *Store) DeleteEvent(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM events WHERE id = ?`, id)
	return err
}
