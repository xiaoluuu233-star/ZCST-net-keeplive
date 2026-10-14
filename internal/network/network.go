package network

import (
	"campus-net/internal/config"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Observation struct {
	Online, Captive, PortalReachable bool
	Rechecked                        bool
	Detail                           string
}

// New 的直连仅关闭 HTTP 环境代理；系统 TUN 路由仍需设置 DIRECT。
func New(cfg config.Config) (*http.Client, error) {
	d := &net.Dialer{Timeout: cfg.Timeout, KeepAlive: 30 * time.Second}
	var src net.IP
	if cfg.SourceIP != "" {
		src = net.ParseIP(cfg.SourceIP)
		if src == nil || src.To4() == nil {
			return nil, errors.New("源地址必须是 IPv4")
		}
	}
	if cfg.Interface != "" {
		iface, err := net.InterfaceByName(cfg.Interface)
		if err != nil {
			return nil, errors.New("指定网卡不存在")
		}
		if iface.Flags&net.FlagUp == 0 {
			return nil, errors.New("指定网卡未启用")
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, errors.New("无法读取网卡地址")
		}
		found := false
		for _, a := range addrs {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip.To4() == nil || ip.IsLoopback() {
				continue
			}
			if src == nil {
				src = ip.To4()
			}
			if ip.Equal(src) {
				found = true
			}
		}
		if !found {
			return nil, errors.New("网卡上没有可用的匹配 IPv4 地址")
		}
		d.Control = bindInterface(iface.Index, cfg.Interface)
	}
	if src != nil {
		d.LocalAddr = &net.TCPAddr{IP: src}
		d.Resolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, n, addr string) (net.Conn, error) {
			dns := net.Dialer{Timeout: cfg.Timeout, Control: d.Control}
			if strings.HasPrefix(n, "udp") {
				dns.LocalAddr = &net.UDPAddr{IP: src}
			} else {
				dns.LocalAddr = &net.TCPAddr{IP: src}
			}
			return dns.DialContext(ctx, n+"4", addr)
		}}
	}
	tr := &http.Transport{DialContext: d.DialContext, MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: cfg.Timeout, ResponseHeaderTimeout: cfg.Timeout}
	if cfg.AllowProxy {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr, Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func SameOrigin(raw, base string) bool {
	u, e1 := url.Parse(raw)
	b, e2 := url.Parse(base)
	if e1 != nil || e2 != nil || u.User != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, b.Scheme) && strings.EqualFold(u.Host, b.Host)
}

func get(ctx context.Context, c *http.Client, raw string) (int, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", "", errors.New("检测请求失败")
	}
	defer resp.Body.Close()
	// Redirect evidence is in the headers; do not wait for an irrelevant body.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, "", resp.Header.Get("Location"), nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil {
		return 0, "", "", errors.New("检测响应读取失败")
	}
	if len(b) > 4096 {
		return resp.StatusCode, "", resp.Header.Get("Location"), errors.New("检测响应超过大小限制")
	}
	return resp.StatusCode, string(b), resp.Header.Get("Location"), nil
}

// AddressSummary 用于无需秘密的出口提示。
func AddressSummary(cfg config.Config) string {
	if cfg.Interface != "" {
		return fmt.Sprintf("网卡 %s", cfg.Interface)
	}
	return "系统路由（TUN 请配置 DIRECT）"
}
