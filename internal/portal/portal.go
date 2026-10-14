package portal

import (
	"campus-net/internal/config"
	"campus-net/internal/network"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	cfg  config.Config
	http *http.Client
}

func New(cfg config.Config, h *http.Client) *Client {
	// 复制客户端，避免修改调用方；认证请求禁止自动跟随重定向。
	client := *h
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Jar == nil {
		// 保留 Portal 页面建立的会话，与原 requests.Session 行为一致。
		client.Jar, _ = cookiejar.New(nil)
	}
	if client.Timeout <= 0 {
		client.Timeout = cfg.Timeout
	}
	return &Client{cfg, &client}
}

// 每次认证重新发现网络参数，不缓存上次网络的 IP/MAC。
// 只跟随同一个已配置 Portal 的无凭据重定向，最多三跳。
func (c *Client) discover(ctx context.Context) map[string]string {
	values := map[string]string{}
	current := c.cfg.ConnectivityURL
	for hop := 0; hop < 3; hop++ {
		req, err := http.NewRequestWithContext(ctx, "GET", current, nil)
		if err != nil {
			break
		}
		resp, err := c.http.Do(req)
		if err != nil {
			break
		}
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
			break
		}
		base, _ := url.Parse(current)
		next, e := base.Parse(loc)
		if e != nil || !network.SameOrigin(next.String(), c.cfg.PortalBaseURL) {
			break
		}
		q := next.Query()
		if q.Has("passwd") || q.Has("password") {
			break
		}
		for _, key := range []string{"wlanuserip", "wlanacname", "wlanacIp", "mac", "vlan"} {
			if v := q.Get(key); v != "" {
				values[key] = v
			}
		}
		current = next.String()
	}
	return values
}

func nonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:], nil
}

func (c *Client) Login(ctx context.Context) Result {
	discovered := c.discover(ctx)
	n, err := nonce()
	if err != nil {
		return Result{State: NetworkError, Message: "无法生成认证随机数"}
	}
	q := url.Values{
		"userid": {c.cfg.UserID}, "passwd": {c.cfg.Password}, "wlanuserip": {c.cfg.WLANUserIP}, "wlanacname": {c.cfg.WLANACName}, "wlanacIp": {c.cfg.WLANACIP}, "mac": {c.cfg.MAC}, "vlan": {c.cfg.VLAN},
		"ssid": {""}, "version": {"0"}, "portalpageid": {"1"}, "timestamp": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "uuid": {n}, "portaltype": {"0"}, "bindCtrlId": {""}, "validateType": {"0"}, "bindOperatorType": {c.cfg.BindOperatorType}, "sendFttrNotice": {"0"}, "skipTemporaryAccountCheck": {"false"}, "token3gpp": {""}, "noBindMac": {"0"}, "roleGroupId": {""}, "roleClassId": {""}, "testGateWay": {""}, "skipOverTopLimit": {"0"},
	}
	for key, value := range discovered {
		q.Set(key, value)
	}
	if q.Get("wlanuserip") == "" && c.cfg.SourceIP != "" {
		q.Set("wlanuserip", c.cfg.SourceIP)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.cfg.PortalBaseURL, "/")+"/quickauth.do?"+q.Encode(), nil)
	if err != nil {
		return Result{State: Failed, Message: "认证地址无效"}
	}
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", strings.TrimRight(c.cfg.PortalBaseURL, "/")+"/portal.do")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/124.0.0.0 Safari/537.36")
	resp, err := c.http.Do(req)
	if err != nil {
		return Result{State: NetworkError, Message: "认证请求失败或已取消"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil || len(body) > 16384 {
		return Result{State: Failed, HTTPStatus: resp.StatusCode, Message: "认证响应读取失败或超过大小限制"}
	}
	r := Classify(resp.StatusCode, body)
	// 未解析正文不进入日志；错误提示只使用归一化消息，日志层进一步脱敏。
	if r.Message == "" {
		r.Message = fmt.Sprintf("Portal HTTP %d，结果 %s", resp.StatusCode, r.State)
	}
	return r
}
