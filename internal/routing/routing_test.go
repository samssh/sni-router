package routing

import (
	"strings"
	"testing"
	"time"
)

func mustRouter(t *testing.T, routes []Route) *SNIRouter {
	t.Helper()
	router, err := NewSNIRouter(routes)
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func TestRoute(t *testing.T) {
	router := mustRouter(t, []Route{
		{Domain: "prom.example.com", Host: "10.0.0.1", Port: 8443, UseProxy: true},
		{Domain: `.*\.internal\.example\.com`, Host: "10.0.0.2", Port: 443, UseRegex: true},
		{Domain: "blocked.example.com", Host: "10.0.0.3", Port: 9, ReverseMatch: true, UseProxy: true},
		{Domain: "non-tls", Port: 8080, UseProxy: false},
		{Domain: "default", Host: "10.0.0.9", Port: 443},
	})

	tests := []struct {
		name      string
		sni       string
		isTLS     bool
		wantProxy bool
		wantAddr  string
		wantErr   bool
	}{
		{
			name:      "exact domain match",
			sni:       "prom.example.com",
			isTLS:     true,
			wantProxy: true,
			wantAddr:  "10.0.0.1:8443",
		},
		{
			name:      "regex match",
			sni:       "api.internal.example.com",
			isTLS:     true,
			wantProxy: false,
			wantAddr:  "10.0.0.2:443",
		},
		{
			name:      "reverse match hits first non-blocked name",
			sni:       "other.example.com",
			isTLS:     true,
			wantProxy: true,
			wantAddr:  "10.0.0.3:9",
		},
		{
			name:      "default fallback when reverse match excludes the name",
			sni:       "blocked.example.com",
			isTLS:     true,
			wantProxy: false,
			wantAddr:  "10.0.0.9:443",
		},
		{
			name:      "non-tls route",
			sni:       "",
			isTLS:     false,
			wantProxy: false,
			wantAddr:  "127.0.0.1:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useProxy, addr, _, err := router.Route(tt.sni, tt.isTLS)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if useProxy != tt.wantProxy {
				t.Fatalf("useProxy = %v, want %v", useProxy, tt.wantProxy)
			}
			if addr != tt.wantAddr {
				t.Fatalf("addr = %q, want %q", addr, tt.wantAddr)
			}
		})
	}
}

func TestRouteFirstMatchWins(t *testing.T) {
	router := mustRouter(t, []Route{
		{Domain: "app.example.com", Host: "10.0.0.1", Port: 1},
		{Domain: "app.example.com", Host: "10.0.0.2", Port: 2},
		{Domain: "default", Host: "10.0.0.9", Port: 443},
	})
	_, addr, _, err := router.Route("app.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.0.0.1:1" {
		t.Fatalf("addr = %q, want first matching route", addr)
	}
}

func TestEmptyHostDefaultsToLocalhost(t *testing.T) {
	router := mustRouter(t, []Route{
		{Domain: "app.example.com", Port: 9443},
		{Domain: "default", Port: 443},
	})
	_, addr, _, err := router.Route("app.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:9443" {
		t.Fatalf("addr = %q, want 127.0.0.1:9443", addr)
	}
}

func TestNonTLSMissingRoute(t *testing.T) {
	router := mustRouter(t, []Route{
		{Domain: "default", Host: "10.0.0.9", Port: 443},
	})
	_, _, _, err := router.Route("", false)
	if err == nil {
		t.Fatal("expected error when non-tls route is missing")
	}
}

func TestMissingDefaultRoute(t *testing.T) {
	_, err := NewSNIRouter(nil)
	if err == nil {
		t.Fatal("expected error when default route is missing")
	}
}

func TestInvalidRegex(t *testing.T) {
	_, err := NewSNIRouter([]Route{
		{Domain: "(", UseRegex: true, Port: 443},
		{Domain: "default", Port: 443},
	})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error = %q, want invalid regex", err)
	}
}

func TestCompiledRegexIsReused(t *testing.T) {
	router := mustRouter(t, []Route{
		{Domain: `^prom\.example\.com$`, Host: "10.0.0.1", Port: 8443, UseRegex: true},
		{Domain: "default", Host: "10.0.0.9", Port: 443},
	})
	if router.Routes[0].compiled == nil {
		t.Fatal("expected regex to be compiled at construction")
	}
	_, addr, _, err := router.Route("prom.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.0.0.1:8443" {
		t.Fatalf("addr = %q", addr)
	}
}

func TestDialTimeoutDefaultAndOverride(t *testing.T) {
	router := mustRouter(t, []Route{
		{Domain: "slow.example.com", Host: "10.0.0.1", Port: 443, DialTimeout: 3},
		{Domain: "default", Host: "10.0.0.9", Port: 443},
	})
	_, _, timeout, err := router.Route("slow.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if timeout != 3*time.Second {
		t.Fatalf("timeout = %s, want 3s", timeout)
	}
	_, _, timeout, err = router.Route("other.example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if timeout != DefaultDialTimeoutSeconds*time.Second {
		t.Fatalf("default timeout = %s, want %ds", timeout, DefaultDialTimeoutSeconds)
	}
}
