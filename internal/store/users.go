package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID          int64
	Username    string
	DisplayName string
	IsAdmin     bool
	CreatedAt   string
	// SSOSubject is the User Management user ID this row caches, or ""
	// for a local-only user not linked yet.
	SSOSubject string
	// Active is false once the person's access to this app was removed.
	// The row stays so their history stays attributed.
	Active bool
}

const userCols = `id, username, display_name, is_admin, created_at, COALESCE(sso_subject, ''), active`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	err := sc.Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.CreatedAt, &u.SSOSubject, &u.Active)
	return u, err
}

// Name returns the display name, falling back to the username.
func (u *User) Name() string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

type Session struct {
	Token     string
	CSRFToken string
	User      User
	// Grant is the SSO grant the session was made from ("" for a local
	// login), kept as-is because it's needed to check the grant with the
	// service. The session row is server-side only: the cookie holds just
	// the token, and the grant is never sent to the browser.
	Grant string
	// GrantCheckedAt is when the grant was last confirmed with the
	// service (zero for local logins).
	GrantCheckedAt time.Time
}

func RandomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// ListUsers lists everyone, inactive users included.
func (s *Store) ListUsers() ([]User, error) {
	return s.listUsers(`SELECT ` + userCols + ` FROM users ORDER BY username`)
}

// ListPickableUsers is the choices for a people picker: the active users,
// plus any inactive ones in keep (those already attached, e.g. a
// production's current members) so they still show and stay ticked.
func (s *Store) ListPickableUsers(keep []int64) ([]User, error) {
	ids := make([]string, 0, len(keep))
	for _, id := range keep {
		ids = append(ids, strconv.FormatInt(id, 10))
	}
	q := `SELECT ` + userCols + ` FROM users WHERE active = 1`
	if len(ids) > 0 {
		q += ` OR id IN (` + strings.Join(ids, ",") + `)`
	}
	return s.listUsers(q + ` ORDER BY username`)
}

func (s *Store) listUsers(q string, args ...any) ([]User, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) GetUser(id int64) (*User, error) {
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

// GetUserBySubject finds the user linked to a User Management user ID.
func (s *Store) GetUserBySubject(subject string) (*User, error) {
	if subject == "" {
		return nil, ErrNotFound
	}
	u, err := scanUser(s.DB.QueryRow(`SELECT `+userCols+` FROM users WHERE sso_subject = ?`, subject))
	if err != nil {
		return nil, notFound(err)
	}
	return &u, nil
}

func (s *Store) CreateUser(username, displayName, password string, isAdmin bool) (int64, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	res, err := s.DB.Exec(`INSERT INTO users (username, display_name, password_hash, is_admin) VALUES (?, ?, ?, ?)`,
		strings.TrimSpace(username), strings.TrimSpace(displayName), string(hash), isAdmin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateUser(id int64, displayName string, isAdmin bool) error {
	_, err := s.DB.Exec(`UPDATE users SET display_name = ?, is_admin = ? WHERE id = ?`, strings.TrimSpace(displayName), isAdmin, id)
	return err
}

func (s *Store) SetPassword(id int64, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), id); err != nil {
		return err
	}
	// Changing a password signs the user out everywhere else.
	return s.DeleteSessionsForUser(id)
}

// HasPassword reports whether a user can sign in with a local password.
// Users added through single sign-on have none until an admin sets one
// (e.g. with the reset-password command, for break-glass sign-in).
func (s *Store) HasPassword(id int64) bool {
	var has bool
	err := s.DB.QueryRow(`SELECT password_hash IS NOT NULL FROM users WHERE id = ?`, id).Scan(&has)
	return err == nil && has
}

// SetActive turns a user's access on or off here. The break-glass
// reset-password command uses it so an admin can always sign in over SSH,
// even if User Management had suspended them; its next sync puts back
// whatever it says.
func (s *Store) SetActive(id int64, active bool) error {
	_, err := s.DB.Exec(`UPDATE users SET active = ? WHERE id = ?`, active, id)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND active = 1`).Scan(&n)
	return n, err
}

// Authenticate checks a username/password pair. It always runs a bcrypt
// comparison so response timing does not reveal whether a username exists.
// Users with no local password (SSO-only) or whose access was removed
// can't sign in this way.
func (s *Store) Authenticate(username, password string) (*User, bool) {
	var u User
	var hash string
	err := s.DB.QueryRow(`SELECT `+userCols+`, password_hash FROM users WHERE username = ? AND password_hash IS NOT NULL AND active = 1`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.CreatedAt, &u.SSOSubject, &u.Active, &hash)
	if err != nil {
		hash = string(dummyHash())
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || err != nil {
		return nil, false
	}
	return &u, true
}

var dummyHash = sync.OnceValue(func() []byte {
	h, _ := bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)
	return h
})

func (s *Store) CreateSession(userID int64, ttl time.Duration) (*Session, error) {
	return s.createSession(userID, "", ttl)
}

// CreateSSOSession starts a session for a user signed in through User
// Management. grant is stored as-is in the (server-side only) session row
// so the grant can be checked with the service later; it counts as
// checked now.
func (s *Store) CreateSSOSession(userID int64, grant string, ttl time.Duration) (*Session, error) {
	if grant == "" {
		return nil, errors.New("SSO session needs a grant")
	}
	return s.createSession(userID, grant, ttl)
}

func (s *Store) createSession(userID int64, grant string, ttl time.Duration) (*Session, error) {
	now := time.Now().UTC()
	sess := &Session{Token: RandomToken(), CSRFToken: RandomToken(), Grant: grant}
	var checked any
	if grant != "" {
		sess.GrantCheckedAt = now.Truncate(time.Second)
		checked = sess.GrantCheckedAt.Format(time.RFC3339)
	}
	if _, err := s.DB.Exec(`INSERT INTO sessions (token, user_id, csrf_token, expires_at, sso_grant, grant_checked_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sess.Token, userID, sess.CSRFToken, now.Add(ttl).Format(time.RFC3339), nullStr(grant), checked); err != nil {
		return nil, err
	}
	return sess, nil
}

// GetSession finds a live session. Sessions of users whose access was
// removed (active = 0) count as gone.
func (s *Store) GetSession(token string) (*Session, error) {
	var sess Session
	var expires string
	var grant, checked sql.NullString
	err := s.DB.QueryRow(`
		SELECT s.token, s.csrf_token, s.expires_at, s.sso_grant, s.grant_checked_at,
		       u.id, u.username, u.display_name, u.is_admin, u.created_at, COALESCE(u.sso_subject, ''), u.active
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token = ? AND u.active = 1`, token).
		Scan(&sess.Token, &sess.CSRFToken, &expires, &grant, &checked,
			&sess.User.ID, &sess.User.Username, &sess.User.DisplayName, &sess.User.IsAdmin, &sess.User.CreatedAt, &sess.User.SSOSubject, &sess.User.Active)
	if err != nil {
		return nil, notFound(err)
	}
	if t, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(t) {
		s.DeleteSession(token)
		return nil, ErrNotFound
	}
	sess.Grant = grant.String
	if checked.Valid {
		sess.GrantCheckedAt, _ = time.Parse(time.RFC3339, checked.String)
	}
	return &sess, nil
}

// MarkGrantChecked records that the session's grant was just confirmed
// with the service.
func (s *Store) MarkGrantChecked(token string) error {
	_, err := s.DB.Exec(`UPDATE sessions SET grant_checked_at = ? WHERE token = ? AND sso_grant IS NOT NULL`,
		time.Now().UTC().Format(time.RFC3339), token)
	return err
}

// DeleteSessionsForUser signs a user out everywhere.
func (s *Store) DeleteSessionsForUser(userID int64) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *Store) PurgeExpiredSessions() error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}
