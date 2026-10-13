package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	k, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
	sealed, err := k.Seal("cal_secret")
	if err != nil || !strings.HasPrefix(sealed, "enc:v1:") || strings.Contains(sealed, "cal_secret") {
		t.Fatalf("sealed = %q, %v", sealed, err)
	}
	// The same file gives the same key.
	k2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k2.Open(sealed); err != nil || got != "cal_secret" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if got, _ := k.Seal(""); got != "" {
		t.Fatalf("blank sealed to %q", got)
	}
}

func TestWrongKey(t *testing.T) {
	k1, _ := LoadOrCreate(filepath.Join(t.TempDir(), "a.key"))
	k2, _ := LoadOrCreate(filepath.Join(t.TempDir(), "b.key"))
	sealed, _ := k1.Seal("mt_token")
	if _, err := k2.Open(sealed); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
	if _, err := k2.Open("enc:v1:not base64!"); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("garbage err = %v", err)
	}
}

func TestPlainPassesThrough(t *testing.T) {
	k, _ := LoadOrCreate(filepath.Join(t.TempDir(), "secrets.key"))
	if got, err := k.Open("mt_plain"); err != nil || got != "mt_plain" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestBadKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.key")
	os.WriteFile(path, []byte("short"), 0o600)
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("short key file accepted")
	}
}
