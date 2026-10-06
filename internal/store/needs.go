package store

import "strings"

// Need statuses: still to get, gathered for the production, and put back
// afterwards.
var NeedStatuses = []string{"needed", "gathered", "returned"}

var needStatusLabels = map[string]string{"needed": "Needed", "gathered": "Gathered", "returned": "Put back"}

func NeedStatusLabel(s string) string { return needStatusLabels[s] }

func ValidNeedStatus(s string) bool { _, ok := needStatusLabels[s]; return ok }

// Need is an item or supply the production needs. TrackerKind and
// TrackerID point at a product or supply in the maintenance tracker;
// without them it's something to buy or borrow.
type Need struct {
	ID           int64
	ProductionID int64
	Name         string
	Quantity     int
	Department   string
	TrackerKind  string // "", "product" or "supply"
	TrackerID    int64
	Status       string
	Notes        string
	UpdatedBy    string
	UpdatedAt    string
}

func (n Need) FromTracker() bool { return n.TrackerKind != "" }

const needCols = `id, production_id, name, quantity, department, tracker_kind, COALESCE(tracker_id, 0), status, notes, updated_by, updated_at`

func scanNeed(sc interface{ Scan(...any) error }) (Need, error) {
	var n Need
	err := sc.Scan(&n.ID, &n.ProductionID, &n.Name, &n.Quantity, &n.Department, &n.TrackerKind, &n.TrackerID, &n.Status, &n.Notes, &n.UpdatedBy, &n.UpdatedAt)
	return n, err
}

func (s *Store) ListNeeds(productionID int64) ([]Need, error) {
	rows, err := s.DB.Query(`SELECT `+needCols+` FROM needs WHERE production_id = ?
		ORDER BY CASE status WHEN 'needed' THEN 0 WHEN 'gathered' THEN 1 ELSE 2 END, name COLLATE NOCASE`, productionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Need
	for rows.Next() {
		n, err := scanNeed(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNeed(id int64) (*Need, error) {
	n, err := scanNeed(s.DB.QueryRow(`SELECT `+needCols+` FROM needs WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &n, nil
}

func (s *Store) CreateNeed(n *Need) (int64, error) {
	if n.Status == "" {
		n.Status = "needed"
	}
	if n.Quantity < 1 {
		n.Quantity = 1
	}
	res, err := s.DB.Exec(`INSERT INTO needs (production_id, name, quantity, department, tracker_kind, tracker_id, status, notes, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ProductionID, strings.TrimSpace(n.Name), n.Quantity, n.Department, n.TrackerKind, nullInt(n.TrackerID), n.Status, n.Notes, n.UpdatedBy)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateNeed saves the editable fields. What it links to in the tracker
// stays as it was.
func (s *Store) UpdateNeed(n *Need) error {
	_, err := s.DB.Exec(`UPDATE needs SET name = ?, quantity = ?, department = ?, status = ?, notes = ?, updated_by = ?, updated_at = datetime('now')
		WHERE id = ?`, strings.TrimSpace(n.Name), n.Quantity, n.Department, n.Status, n.Notes, n.UpdatedBy, n.ID)
	return err
}

func (s *Store) SetNeedStatus(id int64, status, by string) error {
	_, err := s.DB.Exec(`UPDATE needs SET status = ?, updated_by = ?, updated_at = datetime('now') WHERE id = ?`, status, by, id)
	return err
}

func (s *Store) DeleteNeed(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM needs WHERE id = ?`, id)
	return err
}
