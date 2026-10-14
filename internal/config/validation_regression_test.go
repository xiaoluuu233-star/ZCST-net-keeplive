package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEmptyLogAndOutOfRangePortAreRejected(t *testing.T) {
	for _, extra := range []string{"log_path=''", "portal_base_url='http://portal:65536'", "connectivity_url='http://probe:99999999999999'"} {
		p := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(p, []byte("userid='fake'\npassword='fake'\n"+extra), 0600)
		if _, err := Load(p); err == nil {
			t.Fatalf("bad configuration accepted: %s", extra)
		}
	}
}
