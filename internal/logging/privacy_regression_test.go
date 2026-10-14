package logging

import (
	"campus-net/internal/config"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

func TestSensitiveKeysAreClassifiedBeforeSecretReplacement(t *testing.T) {
	c := config.Default()
	c.Password = "password"
	s := newScrubber(c)
	for _, value := range []string{fmt.Sprint(s.attr(slog.String("password", "server-secret"))), fmt.Sprint(s.any(reflect.ValueOf(map[string]string{"password": "server-secret"}), 0)), s.text(`{"password":"server-secret"}`), s.text("http://portal/?password=server-secret")} {
		if strings.Contains(value, "server-secret") {
			t.Fatal("sensitive key leaked server value")
		}
	}
}
func TestTrimmedEncodedSecretIsPrivate(t *testing.T) {
	c := config.Default()
	c.Password = " secret+&value "
	s := newScrubber(c)
	for _, echo := range []string{"secret%2B%26value", `secret+\u0026value`} {
		if strings.Contains(s.text(echo), "secret") {
			t.Fatal("trimmed encoded secret leaked")
		}
	}
}
