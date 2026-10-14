package portal

import (
	"campus-net/internal/config"
	"campus-net/internal/network"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoginDiscoversTrustedParamsAndNeverFollowsCredentialRedirect(t *testing.T) {
	var got url.Values
	var gotCookie string
	foreignHits := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignHits++; w.Write([]byte(`{"code":0}`)) }))
	defer foreign.Close()
	redirectAuth := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/probe":
			http.Redirect(w, r, "/portal.do?wlanuserip=10.1.2.3&vlan=456&mac=aa%3Abb", 302)
		case "/portal.do":
			http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "fixture-session", Path: "/"})
			w.Write([]byte("login"))
		case "/quickauth.do":
			got = r.URL.Query()
			gotCookie = r.Header.Get("Cookie")
			if redirectAuth {
				http.Redirect(w, r, foreign.URL+"/?passwd=fake-secret", 302)
			} else {
				w.Write([]byte(`{"code":"0","message":"认证成功"}`))
			}
		}
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.UserID = "bare-account"
	cfg.Password = "fake-secret"
	cfg.PortalBaseURL = server.URL
	cfg.ConnectivityURL = server.URL + "/probe"
	h := &http.Client{}
	c := New(cfg, h)
	if r := c.Login(context.Background()); !r.OK() {
		t.Fatalf("login failed: %+v", r)
	}
	if got.Get("userid") != "bare-account" || got.Get("wlanuserip") != "10.1.2.3" || got.Get("vlan") != "456" || got.Get("mac") != "aa:bb" {
		t.Fatalf("wrong params %+v", got)
	}
	if got.Has("groupId") || got.Get("uuid") == "" || got.Get("timestamp") == "" {
		t.Fatal("wrong nonce/default groupId")
	}
	if !strings.Contains(gotCookie, "JSESSIONID=fixture-session") {
		t.Fatal("Portal page session cookie was lost")
	}
	first := got.Get("uuid")
	c.Login(context.Background())
	if got.Get("uuid") == first {
		t.Fatal("nonce reused")
	}
	redirectAuth = true
	if r := c.Login(context.Background()); r.OK() {
		t.Fatal("credential redirect marked success")
	}
	if foreignHits != 0 {
		t.Fatal("credential redirect followed")
	}
}
func TestForeignDiscoveryDoesNotSupplyNetworkParams(t *testing.T) {
	foreignHits := 0
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignHits++ }))
	defer foreign.Close()
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/probe" {
			http.Redirect(w, r, foreign.URL+"/?wlanuserip=10.9.9.9", 302)
		} else {
			got = r.URL.Query().Get("wlanuserip")
			w.Write([]byte(`{"code":0}`))
		}
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.UserID = "test"
	cfg.Password = "fake"
	cfg.WLANUserIP = "10.0.0.7"
	cfg.PortalBaseURL = server.URL
	cfg.ConnectivityURL = server.URL + "/probe"
	h, _ := network.New(cfg)
	New(cfg, h).Login(context.Background())
	if got != "10.0.0.7" || foreignHits != 0 {
		t.Fatal("untrusted discovery used")
	}
}
