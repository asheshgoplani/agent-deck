package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostAllowlistCoversAllRoutes(t *testing.T) {
	cases := []struct {
		name, listen, host string
		extra              []string
		want               int
	}{
		{"localhost", "127.0.0.1:8420", "LOCALHOST:8420", nil, 200},
		{"ipv4", "127.0.0.1:8420", "127.0.0.1:8420", nil, 200},
		{"ipv6", "[::1]:8420", "[::1]:8420", nil, 200},
		{"loopback_other_port", "127.0.0.1:8420", "localhost:9000", nil, 200},
		{"explicit_host", "127.0.0.1:8420", "machine.tailnet.ts.net:443", []string{"machine.tailnet.ts.net"}, 200},
		{"explicit_port", "127.0.0.1:8420", "proxy.example:443", []string{"proxy.example:443"}, 200},
		{"wrong_port", "127.0.0.1:8420", "proxy.example:80", []string{"proxy.example:443"}, 421},
		{"wildcard_bind_not_wildcard_host", "0.0.0.0:8420", "evil.example:8420", nil, 421},
		{"nonloopback_ip", "192.0.2.9:8420", "192.0.2.9:8420", nil, 200},
		{"foreign_name_on_loopback", "127.0.0.1:8420", "evil.example:8420", nil, 421},
		{"substring", "127.0.0.1:8420", "localhost.evil.example:8420", nil, 421},
		{"url", "127.0.0.1:8420", "http://localhost:8420", nil, 421},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(Config{ListenAddr: tc.listen, AllowedHosts: tc.extra})
			for _, path := range []string{"/healthz", "/api/sessions", "/events/menu", "/static/app/main.js", "/api/push/config", "/ws/session/fake"} {
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Host = tc.host
				if path == "/events/menu" {
					ctx, cancel := context.WithCancel(r.Context())
					cancel()
					r = r.WithContext(ctx)
				}
				if strings.HasPrefix(path, "/ws/") {
					r.Header.Set("Origin", "http://"+tc.host)
					r.Header.Set("Upgrade", "websocket")
					r.Header.Set("Connection", "Upgrade")
					r.Header.Set("Sec-WebSocket-Version", "13")
					r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				if tc.want == 421 && w.Code != 421 {
					t.Errorf("%s: got %d, want 421", path, w.Code)
				}
				if tc.want == 200 && w.Code == 421 {
					t.Errorf("%s: allowed host rejected", path)
				}
			}
		})
	}
}

func TestWildcardBindAcceptsLocalInterfaceIP(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var host string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if ok && ipNet.IP.IsGlobalUnicast() {
			host = net.JoinHostPort(ipNet.IP.String(), "8420")
			break
		}
	}
	if host == "" {
		t.Skip("no local unicast interface address")
	}
	for _, listen := range []string{"0.0.0.0:8420", "[::]:8420", ":8420"} {
		s := NewServer(Config{ListenAddr: listen, Token: "secret"})
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Host = host
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("listen %q rejected local interface Host %q: %d", listen, host, w.Code)
		}
	}
}

func TestAppendLocalInterfaceIPs(t *testing.T) {
	addrs := []net.Addr{
		&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
		&net.IPNet{IP: net.ParseIP("192.0.2.9"), Mask: net.CIDRMask(24, 32)},
		&net.IPNet{IP: net.ParseIP("2001:db8::9"), Mask: net.CIDRMask(64, 128)},
	}
	got := appendLocalInterfaceIPs(nil, addrs)
	if len(got) != 2 || got[0].name != "192.0.2.9" || got[1].name != "2001:db8::9" {
		t.Fatalf("local interface hosts: %+v", got)
	}
}

func TestHostAllowlistRejectsMalformedAndForwardedAuthorities(t *testing.T) {
	s := NewServer(Config{ListenAddr: "127.0.0.1:8420"})
	for _, host := range []string{"", "localhost.", "localhost:bad", "[::1%lo0]:8420", "evil.example:8420"} {
		r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		r.Host = host
		r.Header.Set("X-Forwarded-Host", "localhost:8420")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q with forwarded localhost: got %d, want 421", host, w.Code)
		}
	}
}

func TestForeignHostRequestsRejected(t *testing.T) {
	s := NewServer(Config{ListenAddr: "127.0.0.1:8420"})
	for _, path := range []string{"/api/sessions", "/ws/session/fake"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "evil.example:8420"
		r.Header.Set("Origin", "http://evil.example:8420")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s: got %d, want 421", path, w.Code)
		}
	}
}
