package store

import (
	"strings"
)

// ClearConnection deletes saved settings for one connection: the exact keys
// given, plus every key starting with prefix when prefix is not empty. It
// runs in one transaction and returns how many settings were removed.
func (s *Store) ClearConnection(keys []string, prefix string) (int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	removed := 0
	for _, k := range keys {
		res, err := tx.Exec(`DELETE FROM settings WHERE key = ?`, k)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		removed += int(n)
	}
	if prefix != "" {
		// Escape LIKE wildcards so "_" in the prefix matches only itself.
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
		res, err := tx.Exec(`DELETE FROM settings WHERE key LIKE ? ESCAPE '\'`, escaped+"%")
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		removed += int(n)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return removed, nil
}
