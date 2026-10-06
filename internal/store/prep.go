package store

import "strings"

// Departments group what has to be ready and what's needed.
var Departments = []string{"props", "costumes", "set", "music", "sound", "lighting", "mics", "video", "other"}

var departmentLabels = map[string]string{
	"props": "Props", "costumes": "Costumes", "set": "Set", "music": "Music", "sound": "Sound effects",
	"lighting": "Lighting", "mics": "Mics", "video": "Slides & video", "other": "Other",
}

func DepartmentLabel(d string) string {
	if l, ok := departmentLabels[d]; ok {
		return l
	}
	return d
}

func ValidDepartment(d string) bool { _, ok := departmentLabels[d]; return ok }

// Prep item statuses, from not started to ready.
var PrepStatuses = []string{"todo", "in_progress", "blocked", "ready"}

var prepStatusLabels = map[string]string{"todo": "Not started", "in_progress": "In progress", "blocked": "Needs help", "ready": "Ready"}

func PrepStatusLabel(s string) string { return prepStatusLabels[s] }

func ValidPrepStatus(s string) bool { _, ok := prepStatusLabels[s]; return ok }

// PrepItem is one thing that has to be ready: a prop, a costume, a
// backing track, a lighting cue...
type PrepItem struct {
	ID           int64
	ProductionID int64
	Department   string
	Name         string
	Status       string
	Owner        string
	DueOn        string
	SceneID      int64
	SceneLabel   string
	Notes        string
	UpdatedBy    string
	UpdatedAt    string
}

const prepCols = `p.id, p.production_id, p.department, p.name, p.status, p.owner, p.due_on, COALESCE(p.scene_id, 0),
	COALESCE(CASE WHEN s.number <> '' AND s.title <> '' THEN s.number || ' · ' || s.title ELSE COALESCE(NULLIF(s.number, ''), s.title) END, ''),
	p.notes, p.updated_by, p.updated_at`

const prepFrom = ` FROM prep_items p LEFT JOIN scenes s ON s.id = p.scene_id`

func scanPrep(sc interface{ Scan(...any) error }) (PrepItem, error) {
	var p PrepItem
	err := sc.Scan(&p.ID, &p.ProductionID, &p.Department, &p.Name, &p.Status, &p.Owner, &p.DueOn, &p.SceneID, &p.SceneLabel,
		&p.Notes, &p.UpdatedBy, &p.UpdatedAt)
	return p, err
}

func (s *Store) ListPrep(productionID int64) ([]PrepItem, error) {
	rows, err := s.DB.Query(`SELECT `+prepCols+prepFrom+` WHERE p.production_id = ?
		ORDER BY CASE p.status WHEN 'blocked' THEN 0 WHEN 'in_progress' THEN 1 WHEN 'todo' THEN 2 ELSE 3 END,
		p.due_on = '', p.due_on, p.name COLLATE NOCASE`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrepItem
	for rows.Next() {
		p, err := scanPrep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetPrep(id int64) (*PrepItem, error) {
	p, err := scanPrep(s.DB.QueryRow(`SELECT `+prepCols+prepFrom+` WHERE p.id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &p, nil
}

func (s *Store) CreatePrep(p *PrepItem) (int64, error) {
	if p.Status == "" {
		p.Status = "todo"
	}
	res, err := s.DB.Exec(`INSERT INTO prep_items (production_id, department, name, status, owner, due_on, scene_id, notes, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ProductionID, p.Department, strings.TrimSpace(p.Name), p.Status, p.Owner, p.DueOn, nullInt(p.SceneID), p.Notes, p.UpdatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdatePrep(p *PrepItem) error {
	_, err := s.DB.Exec(`UPDATE prep_items SET department = ?, name = ?, status = ?, owner = ?, due_on = ?, scene_id = ?, notes = ?,
		updated_by = ?, updated_at = datetime('now') WHERE id = ?`,
		p.Department, strings.TrimSpace(p.Name), p.Status, p.Owner, p.DueOn, nullInt(p.SceneID), p.Notes, p.UpdatedBy, p.ID)
	return err
}

func (s *Store) SetPrepStatus(id int64, status, by string) error {
	_, err := s.DB.Exec(`UPDATE prep_items SET status = ?, updated_by = ?, updated_at = datetime('now') WHERE id = ?`, status, by, id)
	return err
}

func (s *Store) DeletePrep(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM prep_items WHERE id = ?`, id)
	return err
}

// DepartmentPrep is one department's items and how many are ready.
type DepartmentPrep struct {
	Department string
	Items      []PrepItem
	Tally      Tally
	Blocked    int
}

// GroupPrep groups items by department, in the usual department order.
func GroupPrep(items []PrepItem) []DepartmentPrep {
	byDept := map[string]*DepartmentPrep{}
	var extra []string
	for _, it := range items {
		d, ok := byDept[it.Department]
		if !ok {
			d = &DepartmentPrep{Department: it.Department}
			byDept[it.Department] = d
			if !ValidDepartment(it.Department) {
				extra = append(extra, it.Department)
			}
		}
		d.Items = append(d.Items, it)
		d.Tally.Total++
		if it.Status == "ready" {
			d.Tally.Done++
		}
		if it.Status == "blocked" {
			d.Blocked++
		}
	}
	var out []DepartmentPrep
	for _, dep := range append(Departments, extra...) {
		if d, ok := byDept[dep]; ok {
			out = append(out, *d)
		}
	}
	return out
}
