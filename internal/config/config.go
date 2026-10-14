package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Target struct {
	URL, Body  string
	StatusCode int
}
type Config struct {
	UserID, Password, PortalBaseURL, ConnectivityURL, ConnectivityBody       string
	WLANUserIP, WLANACName, WLANACIP, MAC, VLAN, BindOperatorType            string
	Interface, SourceIP, LogPath, ConfigPath                                 string
	CheckInterval, Timeout, ConfirmTimeout, RetryMin, RetryMax, Cooldown     time.Duration
	ProbeTimeout                                                             time.Duration
	ConnectivityCode, MaxRetries, FailThreshold, LogMaxSizeMB, LogMaxBackups int
	AllowProxy                                                               bool
	BackupTargets                                                            []Target
}

// Default preserves the Python Portal fallback. Credentials are never defaulted.
func Default() Config {
	return Config{PortalBaseURL: "http://172.32.253.17", ConnectivityURL: "http://www.msftconnecttest.com/connecttest.txt", ConnectivityBody: "Microsoft Connect Test", ConnectivityCode: 200,
		WLANACName: "VBRAS-ZHKJ4", WLANACIP: "172.32.253.8", VLAN: "0", BindOperatorType: "2", CheckInterval: time.Second, ProbeTimeout: 2 * time.Second, Timeout: 8 * time.Second, ConfirmTimeout: 8 * time.Second,
		RetryMin: 5 * time.Second, RetryMax: 120 * time.Second, Cooldown: 60 * time.Second, MaxRetries: 5, FailThreshold: 2, LogPath: "campus-network.log", LogMaxSizeMB: 5, LogMaxBackups: 3}
}

// ResolvePath never searches the old Python directory or modifies a configuration.
func ResolvePath(explicit string) (string, error) {
	if strings.ContainsRune(explicit, 0) {
		return "", errors.New("config path contains NUL")
	}
	if explicit != "" {
		p, err := filepath.Abs(explicit)
		if err != nil {
			return "", errors.New("cannot resolve config path")
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", errors.New("cannot locate executable directory")
	}
	candidate := filepath.Join(filepath.Dir(exe), "config.toml")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate, nil
	}
	return candidate, nil
}

// Load accepts legacy flat keys and the grouped Go schema. Parsing errors never
// include decoder excerpts: those excerpts may contain the password.
func Load(path string) (Config, error) {
	c := Default()
	resolved, err := ResolvePath(path)
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return Config{}, errors.New("cannot read config file")
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Config{}, errors.New("invalid TOML config")
	}
	seen := map[string]bool{}
	for key, value := range raw {
		name := normalize(key)
		if aliases, ok := sectionAliases[name]; ok {
			section, ok := value.(map[string]any)
			if !ok {
				return Config{}, errors.New("config section must be a table")
			}
			for nested, v := range section {
				canonical, ok := aliases[normalize(nested)]
				if !ok {
					return Config{}, errors.New("unknown config field")
				}
				if err := assign(&c, canonical, v, seen); err != nil {
					return Config{}, err
				}
			}
		} else {
			canonical, ok := flatAliases[name]
			if !ok {
				return Config{}, errors.New("unknown config field")
			}
			if err := assign(&c, canonical, value, seen); err != nil {
				return Config{}, err
			}
		}
	}
	c.ConfigPath = resolved
	if strings.TrimSpace(c.LogPath) == "" {
		return Config{}, fieldError("logpath")
	}
	if !filepath.IsAbs(c.LogPath) {
		c.LogPath = filepath.Join(filepath.Dir(resolved), c.LogPath)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func normalize(s string) string {
	return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s))
}
func aliasMap(entries map[string]string) map[string]string {
	out := map[string]string{}
	for names, canonical := range entries {
		for _, name := range strings.Split(names, ",") {
			out[normalize(name)] = canonical
		}
	}
	return out
}

var flatAliases = aliasMap(map[string]string{
	"userid,user_id,username": "userid", "password,passwd": "password", "portal_base_url,portal_url": "portalurl", "connectivity_url": "probeurl", "connectivity_body": "probebody", "connectivity_code,connectivity_status": "probecode",
	"wlan_user_ip,wlanuserip": "wlanuserip", "wlan_ac_name,wlanacname": "wlanacname", "wlan_ac_ip,wlanacip": "wlanacip", "mac": "mac", "vlan": "vlan", "bind_operator_type,bindOperatorType": "bindoperator",
	"interface,interface_name": "interface", "source_ip": "sourceip", "allow_proxy": "allowproxy", "log_path,log_file": "logpath", "check_interval": "checkinterval", "timeout,request_timeout": "timeout", "confirm_timeout": "confirmtimeout",
	"retry_min,min_interval": "retrymin", "retry_max,max_interval": "retrymax", "cooldown": "cooldown", "max_retries": "maxretries", "fail_threshold,failure_threshold": "failthreshold", "log_max_size_mb": "logmaxsize", "log_max_backups": "logmaxbackups", "backup_targets": "backups",
	"heartbeat_interval": "heartbeat", "groupId,group_id": "groupid",
	"probe_timeout": "probetimeout",
})
var sectionAliases = map[string]map[string]string{
	"account": aliasMap(map[string]string{"userid,user_id,username": "userid", "password,passwd": "password"}),
	"portal":  aliasMap(map[string]string{"base_url,url,portal_base_url": "portalurl", "wlan_user_ip,wlanuserip": "wlanuserip", "wlan_ac_name,wlanacname,ac_name": "wlanacname", "wlan_ac_ip,wlanacip,ac_ip": "wlanacip", "mac": "mac", "vlan": "vlan", "bind_operator_type": "bindoperator"}),
	"network": aliasMap(map[string]string{"interface,interface_name": "interface", "source_ip": "sourceip", "allow_proxy": "allowproxy", "timeout": "timeout"}),
	"probe":   aliasMap(map[string]string{"url,connectivity_url": "probeurl", "body,expected_body,connectivity_body": "probebody", "status_code,expected_status,connectivity_code": "probecode", "interval,check_interval": "checkinterval", "timeout": "timeout", "probe_timeout": "probetimeout", "confirm_timeout": "confirmtimeout", "backup_targets": "backups", "fail_threshold,failure_threshold": "failthreshold"}),
	"retry":   aliasMap(map[string]string{"min,min_interval,retry_min": "retrymin", "max,max_interval,retry_max": "retrymax", "cooldown": "cooldown", "max_retries": "maxretries", "failure_threshold,fail_threshold": "failthreshold"}),
	"log":     aliasMap(map[string]string{"path,file,log_path": "logpath", "max_size_mb": "logmaxsize", "max_backups": "logmaxbackups"}),
}

func fieldError(name string) error { return fmt.Errorf("invalid config field: %s", name) }
func assign(c *Config, name string, v any, seen map[string]bool) error {
	if seen[name] {
		return fmt.Errorf("duplicate config field: %s", name)
	}
	seen[name] = true
	stringFields := map[string]*string{"userid": &c.UserID, "password": &c.Password, "portalurl": &c.PortalBaseURL, "probeurl": &c.ConnectivityURL, "probebody": &c.ConnectivityBody, "wlanuserip": &c.WLANUserIP, "wlanacname": &c.WLANACName, "wlanacip": &c.WLANACIP, "mac": &c.MAC, "vlan": &c.VLAN, "bindoperator": &c.BindOperatorType, "interface": &c.Interface, "sourceip": &c.SourceIP, "logpath": &c.LogPath}
	if dest, ok := stringFields[name]; ok {
		s, ok := v.(string)
		if !ok && (name == "vlan" || name == "bindoperator") {
			if n, isInt := v.(int64); isInt {
				s = fmt.Sprint(n)
				ok = true
			}
		}
		if !ok {
			return fieldError(name)
		}
		*dest = s
		return nil
	}
	durations := map[string]*time.Duration{"checkinterval": &c.CheckInterval, "timeout": &c.Timeout, "probetimeout": &c.ProbeTimeout, "confirmtimeout": &c.ConfirmTimeout, "retrymin": &c.RetryMin, "retrymax": &c.RetryMax, "cooldown": &c.Cooldown}
	if dest, ok := durations[name]; ok || name == "heartbeat" {
		var d time.Duration
		switch x := v.(type) {
		case string:
			var err error
			d, err = time.ParseDuration(x)
			if err != nil {
				return fieldError(name)
			}
		case int64:
			if x <= 0 || x > int64((1<<63-1)/int64(time.Second)) {
				return fieldError(name)
			}
			d = time.Duration(x) * time.Second
		default:
			return fieldError(name)
		}
		if d <= 0 {
			return fieldError(name)
		}
		if dest != nil {
			*dest = d
		}
		return nil
	}
	counts := map[string]*int{"probecode": &c.ConnectivityCode, "maxretries": &c.MaxRetries, "failthreshold": &c.FailThreshold, "logmaxsize": &c.LogMaxSizeMB, "logmaxbackups": &c.LogMaxBackups}
	if dest, ok := counts[name]; ok {
		n, ok := v.(int64)
		if !ok || int64(int(n)) != n {
			return fieldError(name)
		}
		*dest = int(n)
		return nil
	}
	switch name {
	case "allowproxy":
		b, ok := v.(bool)
		if !ok {
			return fieldError(name)
		}
		c.AllowProxy = b
	case "groupid":
		s, ok := v.(string)
		if !ok || strings.ContainsRune(s, 0) {
			return fieldError(name)
		} // Compatibility only: never sent during login.
	case "backups":
		list, ok := v.([]any)
		if !ok {
			return fieldError(name)
		}
		c.BackupTargets = nil
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				return fieldError(name)
			}
			target := Target{StatusCode: 200}
			for key, val := range m {
				switch normalize(key) {
				case "url":
					s, ok := val.(string)
					if !ok {
						return fieldError(name)
					}
					target.URL = s
				case "body", "expectedbody":
					s, ok := val.(string)
					if !ok {
						return fieldError(name)
					}
					target.Body = s
				case "statuscode", "expectedstatus":
					n, ok := val.(int64)
					if !ok || n < 100 || n > 599 {
						return fieldError(name)
					}
					target.StatusCode = int(n)
				default:
					return fieldError(name)
				}
			}
			c.BackupTargets = append(c.BackupTargets, target)
		}
	default:
		return fieldError(name)
	}
	return nil
}

func validURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.ContainsRune(s, 0) {
		return false
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}
func (c Config) Validate() error {
	for _, s := range []string{c.UserID, c.Password, c.PortalBaseURL, c.ConnectivityURL, c.ConnectivityBody, c.WLANUserIP, c.WLANACName, c.WLANACIP, c.MAC, c.VLAN, c.BindOperatorType, c.Interface, c.SourceIP, c.LogPath, c.ConfigPath} {
		if strings.ContainsRune(s, 0) {
			return errors.New("config contains NUL")
		}
	}
	if strings.TrimSpace(c.UserID) == "" {
		return fieldError("userid")
	}
	if strings.TrimSpace(c.Password) == "" {
		return fieldError("password")
	}
	if !validURL(c.PortalBaseURL) {
		return fieldError("portalurl")
	}
	if !validURL(c.ConnectivityURL) {
		return fieldError("probeurl")
	}
	if c.SourceIP != "" {
		if _, err := netip.ParseAddr(c.SourceIP); err != nil {
			return fieldError("sourceip")
		}
	}
	for name, d := range map[string]time.Duration{"checkinterval": c.CheckInterval, "timeout": c.Timeout, "probetimeout": c.ProbeTimeout, "confirmtimeout": c.ConfirmTimeout, "retrymin": c.RetryMin, "retrymax": c.RetryMax, "cooldown": c.Cooldown} {
		if d <= 0 {
			return fieldError(name)
		}
	}
	if c.RetryMax < c.RetryMin {
		return fieldError("retrymax")
	}
	for name, n := range map[string]int{"maxretries": c.MaxRetries, "failthreshold": c.FailThreshold, "logmaxsize": c.LogMaxSizeMB, "logmaxbackups": c.LogMaxBackups} {
		if n <= 0 {
			return fieldError(name)
		}
	}
	if c.LogMaxSizeMB > int((1<<63-1)/(1024*1024)) {
		return fieldError("logmaxsize")
	}
	if c.ConnectivityCode < 100 || c.ConnectivityCode > 599 {
		return fieldError("probecode")
	}
	if strings.TrimSpace(c.LogPath) == "" {
		return fieldError("logpath")
	}
	for _, target := range c.BackupTargets {
		if !validURL(target.URL) || strings.ContainsRune(target.Body, 0) || target.StatusCode < 100 || target.StatusCode > 599 {
			return fieldError("backups")
		}
	}
	return nil
}
