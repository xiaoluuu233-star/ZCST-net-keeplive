package network

import (
	"campus-net/internal/config"
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type probeResult struct {
	online, captive, portal, freshFailure bool
	portalProbe                           bool
}

func probe(ctx context.Context, c *http.Client, target config.Target, portal string) probeResult {
	status, body, location, err := get(ctx, c, target.URL)
	if err == nil && status == target.StatusCode && (target.Body == "" || strings.Contains(body, target.Body)) {
		return probeResult{online: true}
	}
	if status >= 300 && status < 400 && location != "" {
		base, err := url.Parse(target.URL)
		if err == nil {
			destination, err := base.Parse(location)
			if err == nil && SameOrigin(destination.String(), portal) {
				return probeResult{captive: true, portal: true}
			}
		}
	}
	return probeResult{}
}

// probeClient keeps session and routing settings while making probe deadlines
// independent of the longer login timeout. Probes never follow login redirects.
func probeClient(c *http.Client) *http.Client {
	copy := *c
	copy.Timeout = 0
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func freshProbeClient(c *http.Client) (*http.Client, func(), bool) {
	copy := *c
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if tr, ok := transport.(*http.Transport); ok {
		fresh := tr.Clone() // Retain DialContext, interface binding, proxy and TLS policy.
		copy.Transport = fresh
		return &copy, fresh.CloseIdleConnections, true
	}
	// A custom RoundTripper may encode routing that cannot safely be replaced.
	// Keep it functional, but do not assert that its failure was a fresh sample.
	return &copy, func() {}, false
}

// Check is read-only. Its worst-case duration is one primary budget plus one
// shared recheck budget, regardless of the number of configured backup targets.
func Check(ctx context.Context, c *http.Client, cfg config.Config) Observation {
	o := Observation{Detail: "外网检测失败"}
	if ctx.Err() != nil {
		return o
	}
	budget := cfg.ProbeTimeout
	if budget <= 0 {
		budget = 2 * time.Second
	}
	client := probeClient(c)
	primary := config.Target{URL: cfg.ConnectivityURL, StatusCode: cfg.ConnectivityCode, Body: cfg.ConnectivityBody}
	primaryCtx, cancelPrimary := context.WithTimeout(ctx, budget)
	first := probe(primaryCtx, client, primary, cfg.PortalBaseURL)
	cancelPrimary()
	if ctx.Err() != nil {
		return o
	}
	if first.online {
		return Observation{Online: true, Detail: "外网检测正常"}
	}
	o.Captive, o.PortalReachable = first.captive, first.portal
	if first.captive && len(cfg.BackupTargets) == 0 {
		o.Detail = "检测到校园认证重定向"
		return o
	}

	recheckCtx, cancelRecheck := context.WithTimeout(ctx, budget)
	var workers sync.WaitGroup
	var closeFresh func()
	defer func() {
		cancelRecheck()
		workers.Wait()
		if closeFresh != nil {
			closeFresh()
		}
	}()
	count := len(cfg.BackupTargets)
	if !first.captive {
		count += 2 // Fresh primary and Portal.
	}
	results := make(chan probeResult, count)
	start := func(run func() probeResult) {
		workers.Add(1)
		go func() { defer workers.Done(); results <- run() }()
	}
	external := len(cfg.BackupTargets)
	for _, target := range cfg.BackupTargets {
		start(func() probeResult { return probe(recheckCtx, client, target, cfg.PortalBaseURL) })
	}
	if !first.captive {
		external++
		fresh, cleanup, isFresh := freshProbeClient(client)
		closeFresh = cleanup
		start(func() probeResult {
			delay := min(250*time.Millisecond, budget/4)
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-recheckCtx.Done():
				return probeResult{}
			case <-timer.C:
			}
			r := probe(recheckCtx, fresh, primary, cfg.PortalBaseURL)
			r.freshFailure = isFresh && !r.online && ctx.Err() == nil
			return r
		})
		start(func() probeResult {
			status, _, _, err := get(recheckCtx, client, strings.TrimRight(cfg.PortalBaseURL, "/")+"/")
			return probeResult{portalProbe: true, portal: err == nil && status > 0}
		})
	}
	// A trusted redirect needs all external candidates to fail before winning;
	// a healthy backup always wins, even when another result arrived first.
	for remaining := count; remaining > 0; remaining-- {
		select {
		case r := <-results:
			if ctx.Err() != nil {
				o.Rechecked = false
				return o
			}
			if r.online {
				return Observation{Online: true, Detail: "外网检测正常"}
			}
			o.Captive = o.Captive || r.captive
			o.PortalReachable = o.PortalReachable || r.portal
			o.Rechecked = o.Rechecked || r.freshFailure
			if !r.portalProbe {
				external--
			}
			if o.Captive && external == 0 {
				o.Detail = "检测到校园认证重定向"
				return o
			}
		case <-recheckCtx.Done():
			// Join and collect completed evidence, especially the fresh request's
			// deadline failure, before returning. Cancellation is not evidence.
			workers.Wait()
			for len(results) > 0 {
				r := <-results
				if ctx.Err() == nil && r.online {
					return Observation{Online: true, Detail: "外网检测正常"}
				}
				o.Captive = o.Captive || r.captive
				o.PortalReachable = o.PortalReachable || r.portal
				o.Rechecked = o.Rechecked || r.freshFailure
			}
			remaining = 0
		}
	}
	if ctx.Err() != nil {
		o.Rechecked = false
	}
	if o.Captive {
		o.Detail = "检测到校园认证重定向"
	} else if o.PortalReachable {
		o.Detail = "外网异常，Portal 可达"
	} else {
		o.Detail = "外网与 Portal 均不可达"
	}
	return o
}
