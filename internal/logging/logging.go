package logging

import (
	"campus-net/internal/config"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const redacted = "[REDACTED]"

// New uses one handler for console and file so both destinations receive exactly
// the same sanitization. Closing the returned closer owns only the log file.
func New(cfg config.Config) (*slog.Logger, io.Closer, error) {
	if strings.ContainsRune(cfg.LogPath, 0) || cfg.LogPath == "" || cfg.LogMaxSizeMB <= 0 || cfg.LogMaxBackups <= 0 || cfg.LogMaxSizeMB > int((1<<63-1)/(1024*1024)) {
		return nil, nil, errors.New("invalid log settings")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogPath), 0700); err != nil {
		return nil, nil, errors.New("cannot create log directory")
	}
	f, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, errors.New("cannot open log file")
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, errors.New("cannot inspect log file")
	}
	writer := &rotatingWriter{file: f, path: cfg.LogPath, size: info.Size(), limit: int64(cfg.LogMaxSizeMB) * 1024 * 1024, backups: cfg.LogMaxBackups}
	scrub := newScrubber(cfg)
	handler := &privateHandler{next: slog.NewTextHandler(io.MultiWriter(writer, os.Stderr), nil), scrub: scrub}
	return slog.New(handler), writer, nil
}

type rotatingWriter struct {
	mu          sync.Mutex
	file        *os.File
	path        string
	size, limit int64
	backups     int
	closed      bool
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, errors.New("log file is closed")
	}
	original := len(p)
	// A single unusually large record cannot defeat the configured size bound.
	if int64(len(p)) > w.limit {
		p = append(append([]byte(nil), p[:int(w.limit)-1]...), '\n')
	}
	if w.size > 0 && w.size+int64(len(p)) > w.limit {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err != nil {
		return n, errors.New("cannot write log file")
	}
	return original, nil
}
func (w *rotatingWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return errors.New("cannot rotate log file")
	}
	for i := w.backups; i >= 1; i-- {
		target := w.path + "." + strconv.Itoa(i)
		if i == w.backups {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return errors.New("cannot rotate log file")
			}
		}
		source := w.path
		if i > 1 {
			source = w.path + "." + strconv.Itoa(i-1)
		}
		if err := os.Rename(source, target); err != nil && !os.IsNotExist(err) {
			return errors.New("cannot rotate log file")
		}
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("cannot rotate log file")
	}
	w.file = f
	w.size = 0
	return nil
}
func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.file.Close(); err != nil {
		return errors.New("cannot close log file")
	}
	return nil
}

type scrubber struct {
	replacements []string
	encoded      []*regexp.Regexp
}

func newScrubber(cfg config.Config) *scrubber {
	s := &scrubber{}
	unique := map[string]bool{}
	values := []string{cfg.Password, cfg.UserID, cfg.WLANUserIP, cfg.WLANACIP, cfg.SourceIP, cfg.MAC}
	for _, v := range append([]string(nil), values...) {
		if trimmed := strings.TrimSpace(v); trimmed != "" && trimmed != v {
			values = append(values, trimmed)
		}
	}
	for _, v := range values {
		if v == "" {
			continue
		}
		unique[v] = true
		// HTTP bodies and error wrappers may escape a secret before it reaches
		// the logger. Match JSON and quoted-string representations as well.
		if encoded, err := json.Marshal(v); err == nil && len(encoded) >= 2 {
			unique[string(encoded[1:len(encoded)-1])] = true
		}
		quoted := strconv.Quote(v)
		unique[quoted[1:len(quoted)-1]] = true
		// The surrounding whitespace is significant for authentication, but a server
		// may trim its echo. Filtering both variants is safe for logs.
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			unique[trimmed] = true
		}
		for _, encoded := range []string{url.QueryEscape(v), url.PathEscape(v), strings.ReplaceAll(url.QueryEscape(v), "+", "%20")} {
			if encoded != v {
				s.encoded = append(s.encoded, regexp.MustCompile("(?i)"+regexp.QuoteMeta(encoded)))
			}
		}
	}
	for v := range unique {
		s.replacements = append(s.replacements, v)
	}
	sort.Slice(s.replacements, func(i, j int) bool { return len(s.replacements[i]) > len(s.replacements[j]) })
	return s
}

var querySecret = regexp.MustCompile(`(?i)((?:passwd|password|cookie|token|jsessionid|userid|user_id|wlanuserip|wlanacip|mac)=)[^&\s"'<>]*`)
var namedSecret = regexp.MustCompile(`(?i)(["']?(?:passwd|password|cookie|token|jsessionid|userid|user_id|wlanuserip|wlanacip|mac)["']?\s*:\s*)(?:"[^"]*"|'[^']*'|[^,}\s]+)`)
var ipv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
var macAddress = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?:[:-][0-9a-f]{2}){5}\b`)
var urlCredentials = regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`)
var ipv6 = regexp.MustCompile(`(?i)(?:[0-9a-f]{0,4}:){2,}[0-9a-f:.%a-z0-9_-]*`)

func (s *scrubber) text(v string) string {
	// 先识别字段再替换秘密本身，否则密码恰为 "password" 时会破坏字段名。
	v = querySecret.ReplaceAllString(v, "${1}"+redacted)
	v = namedSecret.ReplaceAllString(v, "${1}"+redacted)
	for _, pattern := range s.encoded {
		v = pattern.ReplaceAllLiteralString(v, redacted)
	}
	for _, secret := range s.replacements {
		v = strings.ReplaceAll(v, secret, redacted)
	}
	v = querySecret.ReplaceAllString(v, "${1}"+redacted)
	v = namedSecret.ReplaceAllString(v, "${1}"+redacted)
	v = urlCredentials.ReplaceAllString(v, "${1}"+redacted+"@")
	v = ipv6.ReplaceAllStringFunc(v, func(candidate string) string {
		if addr, err := netip.ParseAddr(candidate); err == nil && addr.Is6() {
			return redacted
		}
		return candidate
	})
	v = ipv4.ReplaceAllLiteralString(v, redacted)
	return macAddress.ReplaceAllLiteralString(v, redacted)
}
func privateKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(key))
	switch key {
	case "password", "passwd", "cookie", "setcookie", "token", "accesstoken", "refreshtoken", "authorization", "jsessionid", "userid", "username", "wlanuserip", "wlanacip", "sourceip", "ip", "mac", "macaddress":
		return true
	}
	return false
}
func (s *scrubber) attr(a slog.Attr) slog.Attr {
	private := privateKey(a.Key)
	a.Key = s.text(a.Key)
	a.Value = a.Value.Resolve()
	if private {
		a.Value = slog.StringValue(redacted)
		return a
	}
	if a.Value.Kind() == slog.KindGroup {
		children := a.Value.Group()
		clean := make([]slog.Attr, len(children))
		for i, child := range children {
			clean[i] = s.attr(child)
		}
		a.Value = slog.GroupValue(clean...)
	} else if a.Value.Kind() == slog.KindString {
		a.Value = slog.StringValue(s.text(a.Value.String()))
	} else if a.Value.Kind() == slog.KindAny {
		a.Value = slog.AnyValue(s.any(reflect.ValueOf(a.Value.Any()), 0))
	}
	return a
}
func (s *scrubber) any(v reflect.Value, depth int) any {
	if !v.IsValid() {
		return nil
	}
	if depth > 10 {
		return "[OMITTED]"
	}
	if v.CanInterface() {
		if err, ok := v.Interface().(error); ok {
			return s.text(err.Error())
		}
		if val, ok := v.Interface().(slog.LogValuer); ok {
			return s.attr(slog.Any("", val)).Value.Any()
		}
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return s.any(v.Elem(), depth+1)
	case reflect.String:
		return s.text(v.String())
	case reflect.Map:
		out := map[string]any{}
		iter := v.MapRange()
		for iter.Next() {
			originalKey := fmt.Sprint(iter.Key().Interface())
			key := s.text(originalKey)
			if privateKey(originalKey) {
				out[key] = redacted
			} else {
				out[key] = s.any(iter.Value(), depth+1)
			}
		}
		return out
	case reflect.Slice, reflect.Array:
		// Byte bodies are text, rather than a printable list of unfiltered bytes.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, v.Len())
			for i := range b {
				b[i] = byte(v.Index(i).Uint())
			}
			return s.text(string(b))
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = s.any(v.Index(i), depth+1)
		}
		return out
	case reflect.Struct:
		out := map[string]any{}
		typ := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if !v.Field(i).CanInterface() {
				continue
			}
			key := typ.Field(i).Name
			if privateKey(key) {
				out[s.text(key)] = redacted
			} else {
				out[s.text(key)] = s.any(v.Field(i), depth+1)
			}
		}
		return out
	default:
		if v.CanInterface() {
			return s.text(fmt.Sprint(v.Interface()))
		}
		return "[OMITTED]"
	}
}

type privateHandler struct {
	next  slog.Handler
	scrub *scrubber
}

func (h *privateHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}
func (h *privateHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, h.scrub.text(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool { clean.AddAttrs(h.scrub.attr(a)); return true })
	return h.next.Handle(ctx, clean)
}
func (h *privateHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.scrub.attr(a)
	}
	return &privateHandler{next: h.next.WithAttrs(clean), scrub: h.scrub}
}
func (h *privateHandler) WithGroup(name string) slog.Handler {
	return &privateHandler{next: h.next.WithGroup(h.scrub.text(name)), scrub: h.scrub}
}
