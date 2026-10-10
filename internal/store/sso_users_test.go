package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Migration 003 rebuilds users; with foreign keys off the rebuild must
// not cascade into production_members or sessions, and ids stay the same.
func TestMigrationKeepsMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := openAt(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	old.CreateUser("filler", "", "a long password", false)
	old.DeleteUser(1) // so ids don't just count from 1
	annID, _ := old.CreateUser("Ann", "Ann A", "a long password", true)
	bobID, _ := old.CreateUser("bob", "", "a long password", false)
	prod, _ := old.CreateProduction(&Production{Title: "Show", Locked: true})
	// Raw SQL: today's SetMembers and CreateSession need the new columns.
	if _, err := old.DB.Exec(`INSERT INTO production_members (production_id, user_id) VALUES (?, ?), (?, ?)`, prod, annID, prod, bobID); err != nil {
		t.Fatal(err)
	}
	token := RandomToken()
	old.DB.Exec(`INSERT INTO sessions (token, user_id, csrf_token, expires_at) VALUES (?, ?, 'x', ?)`,
		token, annID, time.Now().UTC().Add(time.Hour).Format(time.RFC3339))
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if m, _ := st.Members(prod); len(m) != 2 || m[0] != annID || m[1] != bobID {
		t.Errorf("members after migration: %v (want %d, %d)", m, annID, bobID)
	}
	if got, err := st.GetSession(token); err != nil || got.User.ID != annID || got.Grant != "" {
		t.Errorf("session after migration: %+v %v", got, err)
	}
	u, ok := st.Authenticate("ann", "a long password")
	if !ok || u.ID != annID || u.SSOSubject != "" || !u.Active || u.DisplayName != "Ann A" || !u.IsAdmin {
		t.Errorf("user after migration: %+v %v", u, ok)
	}
	var fk int
	st.DB.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Error("foreign keys should be back on")
	}
	// Deleting a user still cascades (the references point at the new table).
	st.DeleteUser(bobID)
	if m, _ := st.Members(prod); len(m) != 1 {
		t.Errorf("cascade after migration: %v", m)
	}
}

func TestUpsertSSOUser(t *testing.T) {
	st := openTest(t)
	localID, _ := st.CreateUser("Tyler", "", "a long password", true)

	// The existing local user is linked by username, case-insensitively.
	u, err := st.UpsertSSOUser("sub-1", "tyler", "Tyler G", false)
	if err != nil || u.ID != localID || u.SSOSubject != "sub-1" || u.Username != "tyler" || u.DisplayName != "Tyler G" || u.IsAdmin || !u.Active {
		t.Fatalf("link: %+v %v", u, err)
	}
	// Found by subject from now on, even after a rename upstream.
	u, _ = st.UpsertSSOUser("sub-1", "tgoza", "Tyler G", true)
	if u.ID != localID || u.Username != "tgoza" || !u.IsAdmin {
		t.Errorf("rename: %+v", u)
	}
	if got, err := st.GetUserBySubject("sub-1"); err != nil || got.ID != localID {
		t.Errorf("by subject: %+v %v", got, err)
	}
	if _, err := st.GetUserBySubject("nope"); err != ErrNotFound {
		t.Errorf("unknown subject: %v", err)
	}

	// A linked user is never taken over by a different subject with the
	// same username: the new person gets a new row and the old one is
	// moved aside.
	other, err := st.UpsertSSOUser("sub-2", "TGOZA", "Someone else", false)
	if err != nil || other.ID == localID || other.SSOSubject != "sub-2" {
		t.Fatalf("collision: %+v %v", other, err)
	}
	old, _ := st.GetUser(localID)
	if old.SSOSubject != "sub-1" || old.Username == "tgoza" {
		t.Errorf("old row should keep its subject and lose the name: %+v", old)
	}
	// Its next sign-in, under a new name, puts it right.
	if u, _ = st.UpsertSSOUser("sub-1", "tyler", "Tyler G", true); u.ID != localID || u.Username != "tyler" {
		t.Errorf("after collision: %+v", u)
	}

	// SSO users have no password, so no local login.
	if _, ok := st.Authenticate("TGOZA", ""); ok {
		t.Error("an SSO-only user signed in with no password")
	}
	if _, ok := st.Authenticate("tyler", "a long password"); !ok {
		t.Error("the linked user keeps their local password")
	}
	if _, err := st.UpsertSSOUser("", "x", "", false); err == nil {
		t.Error("a subject is required")
	}
}

func TestSyncSSOUsers(t *testing.T) {
	st := openTest(t)
	local, _ := st.CreateUser("local", "", "a long password", false)
	ann, _ := st.UpsertSSOUser("a", "ann", "", false)
	bob, _ := st.UpsertSSOUser("b", "bob", "", false)
	prod, _ := st.CreateProduction(&Production{Title: "Show", Locked: true})
	st.SetMembers(prod, []int64{ann.ID, bob.ID})
	bobSess, _ := st.CreateSSOSession(bob.ID, "grant-b", time.Hour)

	// Bob's access is removed, Cat is new, Dan is listed but suspended.
	err := st.SyncSSOUsers([]SSOUser{
		{Subject: "a", Username: "ann", DisplayName: "Ann", Active: true},
		{Subject: "c", Username: "cat", Active: true, IsAdmin: true},
		{Subject: "d", Username: "dan", Active: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	users, _ := st.ListUsers()
	active := map[string]bool{}
	for _, u := range users {
		active[u.Username] = u.Active
	}
	want := map[string]bool{"local": true, "ann": true, "bob": false, "cat": true, "dan": false}
	if len(users) != len(want) {
		t.Fatalf("users: %+v", users)
	}
	for name, a := range want {
		if active[name] != a {
			t.Errorf("%s active = %v, want %v", name, active[name], a)
		}
	}
	if _, err := st.GetSession(bobSess.Token); err != ErrNotFound {
		t.Errorf("bob should be signed out: %v", err)
	}
	if _, ok := st.Authenticate("local", "a long password"); !ok {
		t.Error("unlinked local users are left alone")
	}

	// Pickers offer active users, plus inactive ones already attached.
	members, _ := st.Members(prod)
	pick, _ := st.ListPickableUsers(members)
	var names []string
	for _, u := range pick {
		names = append(names, u.Username)
	}
	if got := strings.Join(names, ","); got != "ann,bob,cat,local" {
		t.Errorf("pickable: %s", got)
	}
	if pick, _ = st.ListPickableUsers(nil); len(pick) != 3 {
		t.Errorf("pickable with nothing attached: %+v", pick)
	}

	// Bob stays a member while ticked, but inactive Dan can't be added.
	dan, _ := st.GetUserBySubject("d")
	st.SetMembers(prod, []int64{ann.ID, bob.ID, dan.ID, local})
	if m, _ := st.Members(prod); len(m) != 3 {
		t.Errorf("members: %v", m)
	}
	st.SetMembers(prod, []int64{ann.ID})
	st.SetMembers(prod, []int64{ann.ID, bob.ID})
	if m, _ := st.Members(prod); len(m) != 1 {
		t.Errorf("an inactive user can't be added back: %v", m)
	}
	st.SetMembers(prod, nil)
	if m, _ := st.Members(prod); len(m) != 0 {
		t.Errorf("clearing members: %v", m)
	}

	// Back on the list: active again, history intact.
	st.SyncSSOUsers([]SSOUser{{Subject: "b", Username: "bob", Active: true}})
	if u, _ := st.GetUser(bob.ID); !u.Active {
		t.Error("bob should be active again")
	}
	if u, _ := st.GetUser(ann.ID); u.Active {
		t.Error("ann was left off the list")
	}
}

func TestSSOSessions(t *testing.T) {
	st := openTest(t)
	u, _ := st.UpsertSSOUser("a", "ann", "", false)
	if _, err := st.CreateSSOSession(u.ID, "", time.Hour); err == nil {
		t.Error("an SSO session needs a grant")
	}
	sess, err := st.CreateSSOSession(u.ID, "grant-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(sess.Token)
	if err != nil || got.Grant != "grant-1" || got.GrantCheckedAt.IsZero() || got.User.SSOSubject != "a" {
		t.Fatalf("session: %+v %v", got, err)
	}
	st.DB.Exec(`UPDATE sessions SET grant_checked_at = '2000-01-01T00:00:00Z'`)
	if err := st.MarkGrantChecked(sess.Token); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetSession(sess.Token); time.Since(got.GrantCheckedAt) > time.Minute {
		t.Errorf("checked at: %v", got.GrantCheckedAt)
	}
	st.CreateSession(u.ID, time.Hour)
	if err := st.DeleteSessionsForUser(u.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Errorf("%d sessions left", n)
	}
}
