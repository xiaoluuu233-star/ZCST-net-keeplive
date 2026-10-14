package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestHelpAndConfigCheckDoNotMakeNetworkRequests(t *testing.T) {
	var out bytes.Buffer
	if code := execute(context.Background(), []string{"--help"}, &out, io.Discard); code != 0 {
		t.Fatal(code)
	}
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("userid='hidden-user'\npassword='hidden-password'\nportal_base_url='http://127.0.0.1:1'\n"), 0600)
	out.Reset()
	if code := execute(context.Background(), []string{"config-check", "--config", p}, &out, io.Discard); code != 0 {
		t.Fatal("config-check incorrectly accessed network", code)
	}
	if bytes.Contains(out.Bytes(), []byte("hidden-user")) || bytes.Contains(out.Bytes(), []byte("hidden-password")) {
		t.Fatal("configuration check exposed secrets")
	}
}
