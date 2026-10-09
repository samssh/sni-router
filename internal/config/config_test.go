package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, content string) []Listener {
	t.Helper()
	listeners, err := Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return listeners
}

func wantParseError(t *testing.T, content, wantSubstr string) {
	t.Helper()
	_, err := Parse([]byte(content))
	if err == nil {
		t.Fatalf("expected error containing %q", wantSubstr)
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Fatalf("error = %q, want it to contain %q", err, wantSubstr)
	}
}

func routeAddr(t *testing.T, l Listener, sni string) string {
	t.Helper()
	_, addr, _, err := l.Router.Route(sni, true)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "routing.yaml")
	content := `
routes:
  - domain: prom.example.com
    host: 10.0.0.1
    port: 8443
    useProxy: true
    dialTimeout: 5
  - domain: default
    host: 127.0.0.1
    port: 443
listeners:
  - addr: 203.0.113.10:443
  - addr: "[2a01:4f8:c2c:afd3::2]:443"
    maxConnections: 500
    routes:
      - domain: api.example.com
        host: 10.0.0.9
        port: 443
  - addr: 203.0.113.12:8443
    inheritRoutes: false
    routes:
      - domain: default
        host: 10.0.0.20
        port: 443
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	listeners, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 3 {
		t.Fatalf("got %d listeners, want 3", len(listeners))
	}

	plain, v6, isolated := listeners[0], listeners[1], listeners[2]
	if plain.Name() != "203.0.113.10:443" {
		t.Fatalf("name = %q", plain.Name())
	}
	if got := routeAddr(t, plain, "prom.example.com"); got != "10.0.0.1:8443" {
		t.Fatalf("shared route = %q", got)
	}

	if v6.Name() != "[2a01:4f8:c2c:afd3::2]:443" {
		t.Fatalf("ipv6 name = %q", v6.Name())
	}
	if v6.MaxConnections != 500 {
		t.Fatalf("maxConnections = %d, want 500", v6.MaxConnections)
	}
	if got := routeAddr(t, v6, "api.example.com"); got != "10.0.0.9:443" {
		t.Fatalf("own route = %q", got)
	}
	if got := routeAddr(t, v6, "prom.example.com"); got != "10.0.0.1:8443" {
		t.Fatalf("inherited route = %q", got)
	}

	if got := routeAddr(t, isolated, "prom.example.com"); got != "10.0.0.20:443" {
		t.Fatalf("inheritRoutes false should skip shared routes, got %q", got)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseAddrNormalization(t *testing.T) {
	listeners := mustParse(t, `
routes:
  - domain: default
    port: 443
listeners:
  - addr: "[2a01:04f8:0c2c:afd3:0:0:0:2]:443"
  - addr: "[::ffff:203.0.113.10]:443"
`)
	if got := listeners[0].Name(); got != "[2a01:4f8:c2c:afd3::2]:443" {
		t.Fatalf("ipv6 name = %q", got)
	}
	if got := listeners[1].Name(); got != "203.0.113.10:443" {
		t.Fatalf("mapped ipv4 name = %q", got)
	}
}

func TestListenerOverridesSharedRoutes(t *testing.T) {
	listeners := mustParse(t, `
routes:
  - domain: prom.example.com
    host: 10.0.0.1
    port: 8443
  - domain: non-tls
    host: 10.0.0.1
    port: 80
  - domain: default
    host: 10.0.0.1
    port: 443
listeners:
  - addr: 203.0.113.11:443
    routes:
      - domain: prom.example.com
        host: 10.0.0.9
        port: 9443
      - domain: non-tls
        host: 10.0.0.9
        port: 8080
      - domain: default
        host: 10.0.0.9
        port: 443
`)
	l := listeners[0]
	if got := routeAddr(t, l, "prom.example.com"); got != "10.0.0.9:9443" {
		t.Fatalf("overridden route = %q", got)
	}
	if got := routeAddr(t, l, "other.example.com"); got != "10.0.0.9:443" {
		t.Fatalf("overridden default = %q", got)
	}
	_, addr, _, err := l.Router.Route("", false)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.0.0.9:8080" {
		t.Fatalf("overridden non-tls = %q", addr)
	}
}

func TestSharedDefaultOptionalWhenEveryListenerHasOne(t *testing.T) {
	mustParse(t, `
listeners:
  - addr: 203.0.113.10:443
    routes:
      - domain: default
        port: 443
`)
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "v1 list format",
			content: `
- domain: default
  port: 443
`,
			want: "v1 format",
		},
		{
			name:    "empty file",
			content: "",
			want:    "at least one listener",
		},
		{
			name: "no listeners",
			content: `
routes:
  - domain: default
    port: 443
`,
			want: "at least one listener",
		},
		{
			name: "unknown field",
			content: `
listeners:
  - addr: 203.0.113.10:443
    maxConns: 5
`,
			want: "maxConns",
		},
		{
			name:    "invalid yaml",
			content: "::: not yaml",
			want:    "unmarshalling",
		},
		{
			name: "missing addr",
			content: `
routes:
  - domain: default
    port: 443
listeners:
  - maxConnections: 5
`,
			want: "addr is required",
		},
		{
			name: "hostname addr",
			content: `
routes:
  - domain: default
    port: 443
listeners:
  - addr: example.com:443
`,
			want: "invalid addr",
		},
		{
			name: "ipv6 without brackets",
			content: `
routes:
  - domain: default
    port: 443
listeners:
  - addr: "2a01:4f8:c2c:afd3::2:443"
`,
			want: "invalid addr",
		},
		{
			name: "port zero",
			content: `
routes:
  - domain: default
    port: 443
listeners:
  - addr: 203.0.113.10:0
`,
			want: "port must be > 0",
		},
		{
			name: "duplicate addr after normalization",
			content: `
routes:
  - domain: default
    port: 443
listeners:
  - addr: "[2a01:4f8::2]:443"
  - addr: "[2a01:04f8:0::2]:443"
`,
			want: "duplicate listener [2a01:4f8::2]:443",
		},
		{
			name: "negative maxConnections",
			content: `
routes:
  - domain: default
    port: 443
listeners:
  - addr: 203.0.113.10:443
    maxConnections: -1
`,
			want: "maxConnections",
		},
		{
			name: "listener without default",
			content: `
routes:
  - domain: default
    port: 443
listeners:
  - addr: 203.0.113.10:443
    inheritRoutes: false
    routes:
      - domain: api.example.com
        port: 443
`,
			want: "listener 203.0.113.10:443: missing default route",
		},
		{
			name: "no default anywhere",
			content: `
listeners:
  - addr: 203.0.113.10:443
`,
			want: "missing default route",
		},
		{
			name: "duplicate shared default even if overridden",
			content: `
routes:
  - domain: default
    port: 443
  - domain: default
    port: 444
listeners:
  - addr: 203.0.113.10:443
    routes:
      - domain: default
        port: 443
`,
			want: "shared routes: duplicate default route",
		},
		{
			name: "duplicate listener default",
			content: `
listeners:
  - addr: 203.0.113.10:443
    routes:
      - domain: default
        port: 443
      - domain: default
        port: 444
`,
			want: "duplicate default route",
		},
		{
			name: "bad shared route",
			content: `
routes:
  - domain: default
    port: 0
listeners:
  - addr: 203.0.113.10:443
`,
			want: "shared routes: invalid port",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantParseError(t, tt.content, tt.want)
		})
	}
}

func TestWildcardOverlap(t *testing.T) {
	tests := []struct {
		name  string
		addrs []string
		want  string // empty means valid
	}{
		{name: "ipv4 wildcard and ipv4 address", addrs: []string{"0.0.0.0:443", "203.0.113.10:443"}, want: "overlaps wildcard listener 0.0.0.0:443"},
		{name: "ipv6 wildcard and ipv4 address", addrs: []string{"203.0.113.10:443", "[::]:443"}, want: "overlaps wildcard listener [::]:443"},
		{name: "ipv6 wildcard and ipv6 address", addrs: []string{"[::]:443", "[2a01:4f8::2]:443"}, want: "overlaps wildcard listener [::]:443"},
		{name: "both wildcards", addrs: []string{"0.0.0.0:443", "[::]:443"}, want: "overlaps wildcard listener"},
		{name: "ipv4 wildcard and ipv6 address", addrs: []string{"0.0.0.0:443", "[2a01:4f8::2]:443"}},
		{name: "wildcard on another port", addrs: []string{"0.0.0.0:8443", "[::]:9443", "203.0.113.10:443"}},
		{name: "wildcard alone", addrs: []string{"[::]:443"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("routes:\n  - domain: default\n    port: 443\nlisteners:\n")
			for _, addr := range tt.addrs {
				b.WriteString("  - addr: \"" + addr + "\"\n")
			}
			if tt.want == "" {
				mustParse(t, b.String())
				return
			}
			wantParseError(t, b.String(), tt.want)
		})
	}
}
