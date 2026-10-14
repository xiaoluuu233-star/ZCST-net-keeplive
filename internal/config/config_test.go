package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T, text string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "配置.toml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLegacyCredentialsAndDefaults(t *testing.T) {
	c, err := loadFixture(t, "userid='bare-test'\npassword='  s&cret +  '\ncheck_interval=17\nrequest_timeout=9\nlog_file='logs/runtime.log'\n")
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "bare-test" || c.Password != "  s&cret +  " {
		t.Fatal("credential bytes changed")
	}
	if c.CheckInterval != 17*time.Second || c.Timeout != 9*time.Second {
		t.Fatal("legacy seconds were not mapped")
	}
	if c.PortalBaseURL != "http://172.32.253.17" || c.WLANACName != "VBRAS-ZHKJ4" || c.WLANACIP != "172.32.253.8" || c.VLAN != "0" || c.BindOperatorType != "2" {
		t.Fatal("Python Portal defaults changed")
	}
	if c.ConnectivityCode != 200 || c.ConnectivityBody != "Microsoft Connect Test" || c.ConfirmTimeout != 8*time.Second || c.MaxRetries != 5 || c.FailThreshold != 2 || c.RetryMin != 5*time.Second || c.RetryMax != 120*time.Second || c.Cooldown != 60*time.Second {
		t.Fatal("recovery defaults changed")
	}
	if !filepath.IsAbs(c.ConfigPath) || c.LogPath != filepath.Join(filepath.Dir(c.ConfigPath), "logs", "runtime.log") {
		t.Fatal("log path is not relative to config directory")
	}
}

func TestNestedConfiguration(t *testing.T) {
	c, err := loadFixture(t, `[account]
userid='nested-test'
password=' secret '
[portal]
base_url='https://portal.example.test'
wlan_user_ip='192.0.2.9'
wlan_ac_name='bras-test'
wlan_ac_ip='192.0.2.1'
mac='aa:bb:cc:dd:ee:ff'
vlan='9'
bind_operator_type='3'
[network]
interface='Ethernet'
source_ip='192.0.2.9'
allow_proxy=true
[probe]
url='https://probe.example.test'
expected_body='OK'
expected_status=204
check_interval='22s'
timeout='3s'
confirm_timeout='4s'
backup_targets=[{url='http://backup.example.test',body='backup',status_code=200}]
[retry]
min='2s'
max='30s'
cooldown='10s'
max_retries=3
failure_threshold=4
[log]
path='nested.log'
max_size_mb=2
max_backups=4
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.UserID != "nested-test" || c.Password != " secret " || c.PortalBaseURL != "https://portal.example.test" || c.WLANUserIP != "192.0.2.9" || c.WLANACName != "bras-test" || c.WLANACIP != "192.0.2.1" || c.MAC != "aa:bb:cc:dd:ee:ff" || c.VLAN != "9" || c.BindOperatorType != "3" {
		t.Fatal("nested Portal values were not mapped")
	}
	if c.Interface != "Ethernet" || c.SourceIP != "192.0.2.9" || !c.AllowProxy || c.ConnectivityURL != "https://probe.example.test" || c.ConnectivityBody != "OK" || c.ConnectivityCode != 204 {
		t.Fatal("nested network values were not mapped")
	}
	if c.CheckInterval != 22*time.Second || c.Timeout != 3*time.Second || c.ConfirmTimeout != 4*time.Second || c.RetryMin != 2*time.Second || c.RetryMax != 30*time.Second || c.Cooldown != 10*time.Second || c.MaxRetries != 3 || c.FailThreshold != 4 || c.LogMaxSizeMB != 2 || c.LogMaxBackups != 4 {
		t.Fatal("nested limits were not mapped")
	}
	if len(c.BackupTargets) != 1 || c.BackupTargets[0].URL != "http://backup.example.test" || c.BackupTargets[0].Body != "backup" || c.BackupTargets[0].StatusCode != 200 {
		t.Fatal("backup target was not mapped")
	}
}

func TestRejectInvalidConfigWithoutLeakingValues(t *testing.T) {
	cases := []string{"max_retries=0", "max_retries='SECRET_VALUE'", "check_interval='invalid'", "check_interval=0", "retry_min='10s'\nretry_max='1s'", "portal_base_url='file:///SECRET_VALUE'", "portal_base_url='http://SECRET_VALUE:secret@example.test'", "source_ip='SECRET_VALUE'", "allow_proxy='SECRET_VALUE'", "connectivity_code=999", "log_max_backups=-1", "password=''", "password='\u0000'", "[account]\npassword=42", "unknown_field='SECRET_VALUE'", "[probe]\nbackup_targets=[{url='file:///SECRET_VALUE'}]"}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			prefix := "userid='test'\npassword=' SECRET_VALUE '\n"
			if strings.HasPrefix(bad, "password=") {
				prefix = "userid='test'\n"
			}
			_, err := loadFixture(t, prefix+bad+"\n")
			if err == nil {
				t.Fatal("invalid config accepted")
			}
			if strings.Contains(err.Error(), "SECRET_VALUE") {
				t.Fatal("error disclosed input value")
			}
		})
	}
}

func TestValidateAndResolveExplicitPath(t *testing.T) {
	c := Default()
	c.UserID = "test"
	c.Password = "secret"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Password = "\x00"
	if c.Validate() == nil {
		t.Fatal("NUL password accepted")
	}
	path, err := ResolvePath("relative.toml")
	if err != nil || !filepath.IsAbs(path) {
		t.Fatal("explicit path not normalized")
	}
}

func TestUpstreamAliasesAndIgnoredLegacyOptions(t *testing.T) {
	c, err := loadFixture(t, `userid='test'
password=' pass '
heartbeat_interval=90
groupId='legacy-only'
bindOperatorType='2'
wlanacIp='192.0.2.1'
wlanuserip='192.0.2.2'
wlanacname='local-bras'
[probe]
interval='19s'
fail_threshold=3
connectivity_code=204
connectivity_body=''
[retry]
min_interval='3s'
max_interval='40s'
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.CheckInterval != 19*time.Second || c.FailThreshold != 3 || c.ConnectivityCode != 204 || c.ConnectivityBody != "" || c.RetryMin != 3*time.Second || c.RetryMax != 40*time.Second || c.WLANACIP != "192.0.2.1" || c.WLANUserIP != "192.0.2.2" || c.WLANACName != "local-bras" || c.Password != " pass " {
		t.Fatal("upstream aliases or ignored legacy options changed behavior")
	}
}

func TestMalformedTOMLDoesNotEchoCredentials(t *testing.T) {
	_, err := loadFixture(t, "userid='test'\npassword=' SECRET_VALUE \n")
	if err == nil || strings.Contains(err.Error(), "SECRET_VALUE") {
		t.Fatal("malformed credential line disclosed or accepted")
	}
}
