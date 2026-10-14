package config

import (
	"testing"
	"time"
)

func TestProbeTimeoutIndependentOfLoginTimeout(t *testing.T) {
	c, err := loadFixture(t, "userid='fixture'\npassword=' secret '\ntimeout=8\nprobe_timeout='90ms'\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != 8*time.Second || c.ProbeTimeout != 90*time.Millisecond || c.Password != " secret " {
		t.Fatal("probe timeout changed login timeout or credentials")
	}
	c, err = loadFixture(t, "userid='fixture'\npassword='secret'\n[probe]\ntimeout='9s'\nprobe_timeout='80ms'\ninterval='1s'\n")
	if err != nil || c.Timeout != 9*time.Second || c.ProbeTimeout != 80*time.Millisecond {
		t.Fatalf("legacy grouped timeout no longer independent: %v", err)
	}
}

func TestProbeTimeoutValidationAndDefaults(t *testing.T) {
	for _, field := range []string{"probe_timeout=0", "probe_timeout='-1s'", "probe_timeout=false"} {
		if _, err := loadFixture(t, "userid='fixture'\npassword='secret'\n"+field); err == nil {
			t.Fatalf("accepted invalid probe timeout: %s", field)
		}
	}
	c, err := loadFixture(t, "userid='fixture'\npassword='secret'\n")
	if err != nil || c.ProbeTimeout != 2*time.Second || c.CheckInterval != time.Second || c.Timeout != 8*time.Second {
		t.Fatalf("wrong active detection defaults: %v", err)
	}
}
