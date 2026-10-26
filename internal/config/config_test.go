package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOldVersionsAreRaised(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"kiroVersion":"1.1.70","nodeVersion":"22.21.1","kasVersion":"0.99.0"}`), 0o600)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if c.KiroVersion != d.KiroVersion || c.NodeVersion != d.NodeVersion {
		t.Fatalf("old versions must be raised: %s %s", c.KiroVersion, c.NodeVersion)
	}
	if c.KASVersion != "0.99.0" {
		t.Fatalf("newer version must be kept: %s", c.KASVersion)
	}
}

func TestOlderVersion(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1.1.70", "1.4.26", true}, {"1.4.26", "1.4.26", false}, {"1.10.0", "1.4.26", false},
		{"1.4", "1.4.26", true}, {"", "1.4.26", true}, {"x", "1.4.26", true}, {"2", "1.4.26", false},
	} {
		if got := olderVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("olderVersion(%q,%q)=%v", tc.a, tc.b, got)
		}
	}
}
