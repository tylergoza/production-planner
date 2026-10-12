package store

import "database/sql"

// SetCalendarConflicts keeps what the calendar said clashed with a date
// (opaque JSON). An empty string means no clashes and removes the row.
func (s *Store) SetCalendarConflicts(eventID int64, conflicts string) error {
	if conflicts == "" {
		_, err := s.DB.Exec(`DELETE FROM calendar_conflicts WHERE event_id = ?`, eventID)
		return err
	}
	_, err := s.DB.Exec(`INSERT INTO calendar_conflicts (event_id, conflicts) VALUES (?, ?)
		ON CONFLICT(event_id) DO UPDATE SET conflicts = excluded.conflicts, checked_at = datetime('now')`, eventID, conflicts)
	return err
}

// CalendarConflicts is what SetCalendarConflicts kept, or "" if nothing.
func (s *Store) CalendarConflicts(eventID int64) (string, error) {
	var c string
	err := s.DB.QueryRow(`SELECT conflicts FROM calendar_conflicts WHERE event_id = ?`, eventID).Scan(&c)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return c, err
}
