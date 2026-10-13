package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tylergoza/production-planner/internal/secrets"
)

func TestSecretSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "productions.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSecretSetting("calendar_token", "cal_abc"); err != nil {
		t.Fatal(err)
	}
	if raw := st.Setting("calendar_token", ""); !strings.HasPrefix(raw, "enc:v1:") {
		t.Fatalf("stored %q, want sealed", raw)
	}
	if v, err := st.SecretSetting("calendar_token"); err != nil || v != "cal_abc" {
		t.Fatalf("got %q, %v", v, err)
	}
	// A plain token from before sealing gets sealed on the next start.
	st.SetSetting("tracker_token", "mt_plain")
	st.Close()
	if st, err = Open(path); err != nil {
		t.Fatal(err)
	}
	if raw := st.Setting("tracker_token", ""); !strings.HasPrefix(raw, "enc:v1:") {
		t.Fatalf("plain token left as %q", raw)
	}
	if v, _ := st.SecretSetting("tracker_token"); v != "mt_plain" {
		t.Fatalf("tracker_token = %q", v)
	}
	st.Close()

	// Restored without its key: the secret can't be read, nothing else breaks.
	os.Remove(filepath.Join(dir, "secrets.key"))
	if st, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.SecretSetting("calendar_token"); !errors.Is(err, secrets.ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
}
