package store

import (
	"database/sql"
	"errors"
	"sort"
	"strconv"
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
	// Locked productions are only for their members (and admins).
	Locked bool
	// Public productions' performances show on the public church calendar.
	Public    bool
	CreatedAt string
	UpdatedAt string
}

// Active reports whether the production is still being worked on.
func (p Production) Active() bool { return p.Status != "done" && p.Status != "cancelled" }

const productionCols = `id, title, kind, status, venue, director, description, notes, locked, public, created_at, updated_at`

func scanProduction(sc interface{ Scan(...any) error }) (Production, error) {
	var p Production
	err := sc.Scan(&p.ID, &p.Title, &p.Kind, &p.Status, &p.Venue, &p.Director, &p.Description, &p.Notes, &p.Locked, &p.Public, &p.CreatedAt, &p.UpdatedAt)
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
	res, err := s.DB.Exec(`INSERT INTO productions (title, kind, status, venue, director, description, notes, locked, public) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Title, p.Kind, p.Status, p.Venue, p.Director, p.Description, p.Notes, p.Locked, p.Public)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateProduction(p *Production) error {
	_, err := s.DB.Exec(`UPDATE productions SET title = ?, kind = ?, status = ?, venue = ?, director = ?, description = ?, notes = ?,
		locked = ?, public = ?, updated_at = datetime('now') WHERE id = ?`,
		p.Title, p.Kind, p.Status, p.Venue, p.Director, p.Description, p.Notes, p.Locked, p.Public, p.ID)
	return err
}

func (s *Store) DeleteProduction(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM productions WHERE id = ?`, id)
	return err
}

// Who can see a production -------------------------------------------------

// visibleTo is a condition on productions p that's true for the ones u can
// see: all of them for an admin, otherwise the unlocked ones and the
// locked ones u is a member of.
func visibleTo(u *User) (string, []any) {
	if u.IsAdmin {
		return `1`, nil
	}
	return `(NOT p.locked OR EXISTS (SELECT 1 FROM production_members m WHERE m.production_id = p.id AND m.user_id = ?))`, []any{u.ID}
}

// CanSee reports whether u may see and work on p.
func (s *Store) CanSee(p *Production, u *User) (bool, error) {
	if !p.Locked || u.IsAdmin {
		return true, nil
	}
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM production_members WHERE production_id = ? AND user_id = ?`, p.ID, u.ID).Scan(&n)
	return n > 0, err
}

// Members lists the IDs of the users on a production's member list.
func (s *Store) Members(productionID int64) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT user_id FROM production_members WHERE production_id = ? ORDER BY user_id`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SetMembers replaces a production's member list. Inactive users (access
// removed) stay on it if they were already, but can't be newly added.
func (s *Store) SetMembers(productionID int64, userIDs []int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	del := `DELETE FROM production_members WHERE production_id = ?`
	if len(userIDs) > 0 {
		ids := make([]string, len(userIDs))
		for i, id := range userIDs {
			ids[i] = strconv.FormatInt(id, 10)
		}
		del += ` AND user_id NOT IN (` + strings.Join(ids, ",") + `)`
	}
	if _, err := tx.Exec(del, productionID); err != nil {
		return err
	}
	for _, id := range userIDs {
		// Skips IDs that aren't (active) users rather than failing;
		// current members are kept by the IGNORE.
		if _, err := tx.Exec(`INSERT OR IGNORE INTO production_members (production_id, user_id) SELECT ?, id FROM users WHERE id = ? AND active = 1`, productionID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	Dates      int // all dates
	Tentative  int // dates still pencilled in
	// ConfirmedShows counts performances that aren't pencilled in.
	ConfirmedShows int
}

// NotScheduled is true when there are no dates at all.
func (p ProductionSummary) NotScheduled() bool { return p.Dates == 0 }

// Unconfirmed is true when there are dates but every one is pencilled in.
func (p ProductionSummary) Unconfirmed() bool { return p.Dates > 0 && p.Tentative == p.Dates }

// NeedsDatesWarning says what's missing when a production that is
// rehearsing or ready lacks dates or a confirmed performance: "" if fine.
func (p ProductionSummary) NeedsDatesWarning() string {
	if p.Status != "rehearsing" && p.Status != "ready" {
		return ""
	}
	label := ProductionStatusLabel(p.Status)
	switch {
	case p.Dates == 0:
		return "This is marked " + label + " but has no dates yet."
	case p.ConfirmedShows == 0:
		return "This is marked " + label + " but has no confirmed performance (its performances are only pencilled in, or there are none)."
	}
	return ""
}

// Tally counts how many of something are done.
type Tally struct{ Done, Total int }

func (t Tally) Percent() int {
	if t.Total == 0 {
		return 0
	}
	return t.Done * 100 / t.Total
}

// ListProductions returns the productions u can see with their summaries:
// active ones first by their next date, then the rest newest first.
func (s *Store) ListProductions(u *User, activeOnly bool, today string) ([]ProductionSummary, error) {
	cond, args := visibleTo(u)
	q := `SELECT ` + productionCols + ` FROM productions p WHERE ` + cond
	if activeOnly {
		q += ` AND status NOT IN ('done', 'cancelled')`
	}
	rows, err := s.DB.Query(q+` ORDER BY id DESC`, args...)
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
		(SELECT COUNT(*) FROM mics WHERE production_id = ?1),
		(SELECT COUNT(*) FROM events WHERE production_id = ?1),
		(SELECT COUNT(*) FROM events WHERE production_id = ?1 AND tentative = 1),
		(SELECT COUNT(*) FROM events WHERE production_id = ?1 AND kind = 'performance' AND tentative = 0)`, id).
		Scan(&sum.FirstDate, &sum.LastDate, &sum.Prep.Done, &sum.Prep.Total, &sum.Needs.Done, &sum.Needs.Total,
			&sum.MicVersion, &sum.Scenes, &sum.Cast, &sum.Mics, &sum.Dates, &sum.Tentative, &sum.ConfirmedShows)
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
	EndTime      string // "" means until done
	Label        string
	Notes        string
	// Tentative dates are pencilled in: held, but not confirmed yet.
	Tentative bool
}

// When is the date and time for display, e.g. "Sun, Dec 14, 2026 · 6:00 PM"
// or "… · 6:00 PM–8:30 PM".
func (e Event) When() string {
	d, err := time.Parse(DateLayout, e.Date)
	if err != nil {
		return e.Date
	}
	out := d.Format("Mon, Jan 2, 2006")
	if t, err := time.Parse("15:04", e.Time); err == nil {
		out += " · " + t.Format("3:04 PM")
		if end, err := time.Parse("15:04", e.EndTime); err == nil {
			out += "–" + end.Format("3:04 PM")
		}
	}
	return out
}

// UntilDone is how long a date with a start time but no end time is taken
// to last, for the calendar and room clashes.
const UntilDone = 3 * time.Hour

// Bounds is when the date takes up its rooms, as a half-open interval. No
// start time is all day; no end time is UntilDone after the start. ok is
// false when the date can't be read.
func (e Event) Bounds() (start, end time.Time, ok bool) {
	day, err := time.Parse(DateLayout, e.Date)
	if err != nil {
		return start, end, false
	}
	at := func(clock string) (time.Time, bool) {
		c, err := time.Parse("15:04", clock)
		if err != nil {
			return day, false
		}
		return day.Add(time.Duration(c.Hour())*time.Hour + time.Duration(c.Minute())*time.Minute), true
	}
	start, has := at(e.Time)
	if !has {
		return day, day.AddDate(0, 0, 1), true
	}
	end, has = at(e.EndTime)
	if !has || !end.After(start) {
		end = start.Add(UntilDone)
	}
	return start, end, true
}

// Overlaps reports whether two dates take up any of the same time. Dates
// that can't be read count as overlapping, so a warning is never lost.
func (e Event) Overlaps(o Event) bool {
	as, ae, ok1 := e.Bounds()
	bs, be, ok2 := o.Bounds()
	if !ok1 || !ok2 {
		return true
	}
	return as.Before(be) && bs.Before(ae)
}

// Title is the label, or the kind when there isn't one.
func (e Event) Title() string {
	if e.Label != "" {
		return e.Label
	}
	return EventKindLabel(e.Kind)
}

const eventCols = `id, production_id, kind, date, time, end_time, label, notes, tentative`

func scanEvent(sc interface{ Scan(...any) error }) (Event, error) {
	var e Event
	err := sc.Scan(&e.ID, &e.ProductionID, &e.Kind, &e.Date, &e.Time, &e.EndTime, &e.Label, &e.Notes, &e.Tentative)
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

// UpcomingEvents lists dates from today on across the active productions u
// can see.
type UpcomingEvent struct {
	Event
	ProductionTitle string
}

func (s *Store) UpcomingEvents(u *User, today string, limit int) ([]UpcomingEvent, error) {
	cond, args := visibleTo(u)
	rows, err := s.DB.Query(`SELECT e.id, e.production_id, e.kind, e.date, e.time, e.end_time, e.label, e.notes, e.tentative, p.title
		FROM events e JOIN productions p ON p.id = e.production_id
		WHERE e.date >= ? AND p.status NOT IN ('done', 'cancelled') AND `+cond+`
		ORDER BY e.date, e.time, e.id LIMIT ?`, append(append([]any{today}, args...), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UpcomingEvent
	for rows.Next() {
		var e UpcomingEvent
		if err := rows.Scan(&e.ID, &e.ProductionID, &e.Kind, &e.Date, &e.Time, &e.EndTime, &e.Label, &e.Notes, &e.Tentative, &e.ProductionTitle); err != nil {
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

// CreateEvent adds a date and its rooms in one transaction, so the
// calendar outbox gets one row for it.
func (s *Store) CreateEvent(e *Event, rooms []Room) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO events (production_id, kind, date, time, end_time, label, notes, tentative) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ProductionID, e.Kind, e.Date, e.Time, e.EndTime, strings.TrimSpace(e.Label), e.Notes, e.Tentative)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := setEventRooms(tx, id, rooms); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateEvent saves a date and replaces its rooms in one transaction.
func (s *Store) UpdateEvent(e *Event, rooms []Room) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE events SET kind = ?, date = ?, time = ?, end_time = ?, label = ?, notes = ?, tentative = ? WHERE id = ?`,
		e.Kind, e.Date, e.Time, e.EndTime, strings.TrimSpace(e.Label), e.Notes, e.Tentative, e.ID); err != nil {
		return err
	}
	if err := setEventRooms(tx, e.ID, rooms); err != nil {
		return err
	}
	return tx.Commit()
}

// ConfirmEvent confirms one pencilled-in date.
func (s *Store) ConfirmEvent(id int64) error {
	_, err := s.DB.Exec(`UPDATE events SET tentative = 0 WHERE id = ? AND tentative = 1`, id)
	return err
}

// ConfirmAllEvents confirms every pencilled-in date of a production and
// says how many there were.
func (s *Store) ConfirmAllEvents(productionID int64) (int64, error) {
	res, err := s.DB.Exec(`UPDATE events SET tentative = 0 WHERE production_id = ? AND tentative = 1`, productionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) DeleteEvent(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM events WHERE id = ?`, id)
	return err
}
