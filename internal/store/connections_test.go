package store

import "testing"

func TestClearConnection(t *testing.T) {
	st := openTest(t)
	set := map[string]string{
		"sso_url":            "https://um.example",
		"sso_pending_url":    "https://pending.example",
		"sso_pending_secret": "x",
		"tracker_url":        "https://tracker.example",
		"site_name":          "Church",
		"ssoXpending_url":    "should survive: _ in prefix is literal",
	}
	for k, v := range set {
		if err := st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}

	n, err := st.ClearConnection([]string{"sso_url", "missing_key"}, "sso_pending_")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("removed = %d, want 3", n)
	}
	for _, gone := range []string{"sso_url", "sso_pending_url", "sso_pending_secret"} {
		if v := st.Setting(gone, "<gone>"); v != "<gone>" {
			t.Errorf("%s still set to %q", gone, v)
		}
	}
	for _, kept := range []string{"tracker_url", "site_name", "ssoXpending_url"} {
		if v := st.Setting(kept, ""); v == "" {
			t.Errorf("%s was removed", kept)
		}
	}
}
