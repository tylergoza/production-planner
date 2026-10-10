package store

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Users from User Management -----------------------------------------------
//
// With single sign-on the users table is a cache of the people the service
// knows, keyed by sso_subject (the service's user ID). Rows are never
// deleted here, so everything that points at users(id) keeps working and
// history stays attributed after someone's access is removed.

// SSOUser is a person as User Management describes them to this app.
type SSOUser struct {
	Subject     string
	Username    string
	DisplayName string
	IsAdmin     bool
	// Active is false when the person is listed but suspended for this
	// app.
	Active bool
}

// UpsertSSOUser makes sure the person signing in has a local row and
// returns it, active and up to date. In order, it:
//
//  1. uses the row already linked to subject;
//  2. else links the local user with the same username (case-insensitive),
//     but only if that row isn't linked to anyone yet (how the existing
//     local users keep their ids and history);
//  3. else adds a row with no password.
//
// Username, display name and is_admin are then copied from the service.
//
// Usernames are unique here too, so if another row still holds the
// username (someone renamed upstream, or an old account whose name was
// given to someone new), that row is renamed out of the way to
// "username#id". Its own next sign-in or sync puts its real name back.
func (s *Store) UpsertSSOUser(subject, username, displayName string, isAdmin bool) (*User, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := upsertSSOUser(tx, SSOUser{Subject: subject, Username: username, DisplayName: displayName, IsAdmin: isAdmin, Active: true})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

// SaveSSOUser is UpsertSSOUser for a person as User Management listed them,
// with u.Active deciding whether the row is active (e.g. after an admin
// suspended them on the Users page). It doesn't touch sessions; callers
// sign out someone who's no longer active with DeleteSessionsForUser.
func (s *Store) SaveSSOUser(u SSOUser) (*User, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	id, err := upsertSSOUser(tx, u)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetUser(id)
}

// SyncSSOUsers brings the cache in line with the service's list of
// everyone who has access to this app (for the people pickers): each is
// added or updated, and linked users who aren't in the list, or are listed
// as inactive, are marked inactive and signed out. Nothing is deleted.
// Users not linked to the service (sso_subject NULL) are left alone.
//
// The list is taken as complete, so the caller should only pass a list it
// got successfully from the service.
func (s *Store) SyncSSOUsers(list []SSOUser) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	keep := map[string]bool{}
	for _, u := range list {
		if _, err := upsertSSOUser(tx, u); err != nil {
			return err
		}
		if u.Active {
			keep[u.Subject] = true
		}
	}
	rows, err := tx.Query(`SELECT id, sso_subject FROM users WHERE sso_subject IS NOT NULL AND active = 1`)
	if err != nil {
		return err
	}
	var drop []int64
	for rows.Next() {
		var id int64
		var subject string
		if err := rows.Scan(&id, &subject); err != nil {
			rows.Close()
			return err
		}
		if !keep[subject] {
			drop = append(drop, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range drop {
		if _, err := tx.Exec(`UPDATE users SET active = 0 WHERE id = ?`, id); err != nil {
			return err
		}
	}
	// Listed-but-inactive users were set inactive by upsertSSOUser; sign
	// out everyone inactive and linked.
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id IN (SELECT id FROM users WHERE active = 0 AND sso_subject IS NOT NULL)`); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertSSOUser is UpsertSSOUser inside tx, with u.Active setting active.
func upsertSSOUser(tx *sql.Tx, u SSOUser) (int64, error) {
	u.Subject = strings.TrimSpace(u.Subject)
	u.Username = strings.TrimSpace(u.Username)
	u.DisplayName = strings.TrimSpace(u.DisplayName)
	if u.Subject == "" || u.Username == "" {
		return 0, errors.New("SSO user needs a subject and a username")
	}

	var id int64
	err := tx.QueryRow(`SELECT id FROM users WHERE sso_subject = ?`, u.Subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Link an existing local user of the same name, if not linked yet.
		err = tx.QueryRow(`SELECT id FROM users WHERE username = ? AND sso_subject IS NULL`, u.Username).Scan(&id)
		if err == nil {
			if _, err := tx.Exec(`UPDATE users SET sso_subject = ? WHERE id = ?`, u.Subject, id); err != nil {
				return 0, err
			}
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	// Move anyone else holding the username out of the way (see
	// UpsertSSOUser).
	var other int64
	err = tx.QueryRow(`SELECT id FROM users WHERE username = ? AND id <> ?`, u.Username, id).Scan(&other)
	if err == nil {
		aside := u.Username + "#" + strconv.FormatInt(other, 10)
		if _, err := tx.Exec(`UPDATE users SET username = ? WHERE id = ?`, aside, other); err != nil {
			return 0, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	if id == 0 {
		res, err := tx.Exec(`INSERT INTO users (username, display_name, password_hash, is_admin, sso_subject, active) VALUES (?, ?, NULL, ?, ?, ?)`,
			u.Username, u.DisplayName, u.IsAdmin, u.Subject, u.Active)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err = tx.Exec(`UPDATE users SET username = ?, display_name = ?, is_admin = ?, active = ? WHERE id = ?`,
		u.Username, u.DisplayName, u.IsAdmin, u.Active, id)
	return id, err
}
