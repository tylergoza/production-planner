package store

import (
	"crypto/rand"
	"encoding/base64"
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

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.DB.Query(`SELECT id, username, display_name, is_admin, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) GetUser(id int64) (*User, error) {
	var u User
	err := s.DB.QueryRow(`SELECT id, username, display_name, is_admin, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.CreatedAt)
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
	_, err = s.DB.Exec(`DELETE FROM sessions WHERE user_id = ?`, id)
	return err
}

func (s *Store) DeleteUser(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&n)
	return n, err
}

// Authenticate checks a username/password pair. It always runs a bcrypt
// comparison so response timing does not reveal whether a username exists.
func (s *Store) Authenticate(username, password string) (*User, bool) {
	var u User
	var hash string
	err := s.DB.QueryRow(`SELECT id, username, display_name, is_admin, created_at, password_hash FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.DisplayName, &u.IsAdmin, &u.CreatedAt, &hash)
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
	sess := &Session{Token: RandomToken(), CSRFToken: RandomToken()}
	expires := time.Now().UTC().Add(ttl).Format(time.RFC3339)
	if _, err := s.DB.Exec(`INSERT INTO sessions (token, user_id, csrf_token, expires_at) VALUES (?, ?, ?, ?)`,
		sess.Token, userID, sess.CSRFToken, expires); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) GetSession(token string) (*Session, error) {
	var sess Session
	var expires string
	err := s.DB.QueryRow(`
		SELECT s.token, s.csrf_token, s.expires_at, u.id, u.username, u.display_name, u.is_admin, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token = ?`, token).
		Scan(&sess.Token, &sess.CSRFToken, &expires, &sess.User.ID, &sess.User.Username, &sess.User.DisplayName, &sess.User.IsAdmin, &sess.User.CreatedAt)
	if err != nil {
		return nil, notFound(err)
	}
	if t, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(t) {
		s.DeleteSession(token)
		return nil, ErrNotFound
	}
	return &sess, nil
}

func (s *Store) DeleteSession(token string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

func (s *Store) PurgeExpiredSessions() error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}
