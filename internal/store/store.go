// Package store wraps the SQLite database: connection setup, migrations,
// and all queries used by the web server.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO needed
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a single-row lookup finds nothing.
var ErrNotFound = errors.New("not found")

type Store struct {
	DB *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies
// any pending migrations.
func Open(path string) (*Store, error) { return openAt(path, 0) }

// openAt opens the database migrated up to version upTo (0 for all).
func openAt(path string, upTo int) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection avoids
	// SQLITE_BUSY churn and is plenty for this workload.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db}
	if err := s.migrate(upTo); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// noForeignKeys marks a migration that rebuilds tables other tables point
// at. SQLite needs foreign keys off for that (and can only switch them off
// outside a transaction); they're checked before the migration commits.
const noForeignKeys = "-- foreign_keys: off"

// migrate applies pending migrations up to version upTo (0 for all).
func (s *Store) migrate(upTo int) error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %q", e.Name())
		}
		if upTo > 0 && version > upTo {
			break
		}
		var exists int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if err := s.runMigration(version, string(body)); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return nil
}

func (s *Store) runMigration(version int, body string) error {
	ctx := context.Background()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	fkOff := strings.HasPrefix(body, noForeignKeys)
	if fkOff {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(body); err != nil {
		return err
	}
	if fkOff {
		var table string
		var parent sql.NullString
		var rowid, fkid sql.NullInt64
		err := tx.QueryRow(`PRAGMA foreign_key_check`).Scan(&table, &rowid, &parent, &fkid)
		if err == nil {
			return fmt.Errorf("broken reference in %s row %d", table, rowid.Int64)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// Backup writes a consistent snapshot of the live database to dest using
// VACUUM INTO. Safe to run while the app is serving requests.
func (s *Store) Backup(ctx context.Context, dest string) error {
	_, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}

// Settings ---------------------------------------------------------------

func (s *Store) Setting(key, fallback string) string {
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// helpers ----------------------------------------------------------------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// DateLayout is how dates are stored: YYYY-MM-DD.
const DateLayout = "2006-01-02"

// nextPosition is one past the last position in a production's list.
func (s *Store) nextPosition(table string, productionID int64) (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM `+table+` WHERE production_id = ?`, productionID).Scan(&n)
	return n, err
}

// movable lists the tables whose rows can be moved up and down.
var movable = map[string]bool{"scenes": true, "cast_members": true, "mics": true}

// Move swaps a row with its neighbour above (up) or below in its
// production's list. At either end it does nothing.
func (s *Store) Move(table string, id int64, up bool) error {
	if !movable[table] {
		return fmt.Errorf("can't move rows of %s", table)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var prod int64
	var pos int
	if err := tx.QueryRow(`SELECT production_id, position FROM `+table+` WHERE id = ?`, id).Scan(&prod, &pos); err != nil {
		return notFound(err)
	}
	q := `SELECT id, position FROM ` + table + ` WHERE production_id = ? AND position > ? ORDER BY position LIMIT 1`
	if up {
		q = `SELECT id, position FROM ` + table + ` WHERE production_id = ? AND position < ? ORDER BY position DESC LIMIT 1`
	}
	var otherID int64
	var otherPos int
	if err := tx.QueryRow(q, prod, pos).Scan(&otherID, &otherPos); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE `+table+` SET position = ? WHERE id = ?`, otherPos, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE `+table+` SET position = ? WHERE id = ?`, pos, otherID); err != nil {
		return err
	}
	return tx.Commit()
}
