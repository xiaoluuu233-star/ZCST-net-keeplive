package logging

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"campus-net/internal/config"
)

func discardConsole(t *testing.T) {
	t.Helper()
	old := os.Stderr
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = old; f.Close() })
}
func TestNestedAttributesErrorsAndBodiesArePrivate(t *testing.T) {
	discardConsole(t)
	cfg := config.Default()
	cfg.UserID = "student-private"
	cfg.Password = " s&cret+private "
	cfg.WLANUserIP = "192.0.2.19"
	cfg.MAC = "aa:bb:cc:dd:ee:ff"
	cfg.LogPath = filepath.Join(t.TempDir(), "private.log")
	logger, closer, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	logger.With(slog.Group("session", slog.String("userid", cfg.UserID), slog.Any("nested", map[string]any{"password": "another-secret", "body": "server echoed " + cfg.Password}))).WithGroup("portal").Error("failure "+cfg.Password,
		"error", errors.New("GET http://example.test/?passwd="+url.QueryEscape(cfg.Password)+"&token=token-secret&cookie=cookie-secret&jsessionid=session-secret"),
		"body", map[string]any{"payload": []any{cfg.Password, cfg.UserID, cfg.WLANUserIP, cfg.MAC}},
		"encoded", url.PathEscape(cfg.Password), "password", "different-secret")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{cfg.Password, cfg.UserID, cfg.WLANUserIP, cfg.MAC, url.QueryEscape(cfg.Password), url.PathEscape(cfg.Password), "another-secret", "different-secret", "token-secret", "cookie-secret", "session-secret"} {
		if strings.Contains(string(output), secret) {
			t.Fatalf("sensitive value leaked: %q", secret)
		}
	}
	if !strings.Contains(string(output), "failure") || !strings.Contains(string(output), "[REDACTED]") {
		t.Fatal("useful sanitized log missing")
	}
}

func TestSizeRotationKeepsBoundedBackups(t *testing.T) {
	discardConsole(t)
	cfg := config.Default()
	cfg.LogPath = filepath.Join(t.TempDir(), "rotating.log")
	cfg.LogMaxSizeMB = 1
	cfg.LogMaxBackups = 2
	logger, closer, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		logger.Info("record", "body", strings.Repeat("x", 600000), "sequence", i)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(cfg.LogPath + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("expected current file and 2 backups, got %d", len(files))
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 1024*1024 || info.Size() == 0 {
			t.Fatal("rotation size was not bounded")
		}
	}
	latest, err := os.ReadFile(cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(latest), "sequence=4") {
		t.Fatal("latest record lost in rotation")
	}
}

func TestConsoleAndFileBothScrubEscapedBody(t *testing.T) {
	cfg := config.Default()
	cfg.Password = " secret\"\n+&value "
	cfg.UserID = "private-user"
	cfg.LogPath = filepath.Join(t.TempDir(), "dual.log")
	old := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = write
	t.Cleanup(func() { os.Stderr = old; read.Close(); write.Close() })
	logger, closer, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"echo": cfg.Password})
	logger.Info("response", "body", string(raw), "query", "https://example.test/?password="+url.QueryEscape(cfg.Password))
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	write.Close()
	console := make([]byte, 4096)
	n, err := read.Read(console)
	if err != nil {
		t.Fatal(err)
	}
	console = console[:n]
	file, err := os.ReadFile(cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(console) != string(file) {
		t.Fatal("console and file receive different records")
	}
	encoded, _ := json.Marshal(cfg.Password)
	escaped := string(encoded[1 : len(encoded)-1])
	// Text logs add another escape layer, so the unambiguous tail detects a leak.
	for _, secret := range []string{cfg.Password, escaped, "value ", url.QueryEscape(cfg.Password)} {
		if strings.Contains(string(file), secret) {
			t.Fatalf("escaped response disclosed secret variant %q", secret)
		}
	}
}

func TestDynamicIPv6AndURLCredentialsArePrivate(t *testing.T) {
	discardConsole(t)
	cfg := config.Default()
	cfg.LogPath = filepath.Join(t.TempDir(), "dynamic.log")
	logger, closer, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	logger.Error("request failed", "error", errors.New("Get https://redirect-user:redirect-secret@[2001:db8::12]/?token=other-token"), "body", "source 2001:db8::99")
	closer.Close()
	output, err := os.ReadFile(cfg.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"redirect-user", "redirect-secret", "2001:db8::12", "2001:db8::99", "other-token"} {
		if strings.Contains(string(output), secret) {
			t.Fatalf("dynamic identifier disclosed: %s", secret)
		}
	}
}

func TestConcurrentRecordsSurviveRotationAndClose(t *testing.T) {
	discardConsole(t)
	cfg := config.Default()
	cfg.LogPath = filepath.Join(t.TempDir(), "concurrent.log")
	cfg.LogMaxSizeMB = 1
	cfg.LogMaxBackups = 3
	logger, closer, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for j := 0; j < 30; j++ {
				logger.Info("concurrent", "worker", worker, "record", j, "body", strings.Repeat("x", 5000))
			}
		}(i)
	}
	workers.Wait()
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closer.Close(); err != nil {
		t.Fatal("close not idempotent")
	}
	files, _ := filepath.Glob(cfg.LogPath + "*")
	records := 0
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		records += strings.Count(string(content), "msg=concurrent")
	}
	if records != 240 {
		t.Fatalf("concurrent records lost: %d", records)
	}
}
