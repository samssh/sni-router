package server

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"sni-router/internal/routing"
)

func startTLSBackend(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func backendRoute(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func startTestRouter(t *testing.T, routes []routing.Route) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	router, err := routing.NewSNIRouter(routes)
	if err != nil {
		t.Fatal(err)
	}
	listener := NewListener(router, newTestMetrics(), 0)
	go listener.serve(ln)
	return ln.Addr().String()
}

func startTestRouterLimited(t *testing.T, routes []routing.Route, maxConns int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	router, err := routing.NewSNIRouter(routes)
	if err != nil {
		t.Fatal(err)
	}
	listener := NewListener(router, newTestMetrics(), 0).WithMaxConns(maxConns)
	go listener.serve(ln)
	return ln.Addr().String()
}

func routerHTTPClient(routerAddr, serverName string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Dial: func(network, _ string) (net.Conn, error) {
				return net.Dial(network, routerAddr)
			},
			TLSClientConfig: &tls.Config{
				ServerName:         serverName,
				InsecureSkipVerify: true,
			},
		},
	}
}

func TestListenerRoutesBySNI(t *testing.T) {
	prom := startTLSBackend(t, "prom")
	fallback := startTLSBackend(t, "default")
	promHost, promPort := backendRoute(t, prom)
	defHost, defPort := backendRoute(t, fallback)

	routerAddr := startTestRouter(t, []routing.Route{
		{Domain: "prom.example.com", Host: promHost, Port: promPort},
		{Domain: "default", Host: defHost, Port: defPort},
	})

	t.Run("known sni", func(t *testing.T) {
		client := routerHTTPClient(routerAddr, "prom.example.com")
		resp, err := client.Get("https://prom.example.com/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "prom" {
			t.Fatalf("body = %q, want prom", body)
		}
	})

	t.Run("unknown sni uses default", func(t *testing.T) {
		client := routerHTTPClient(routerAddr, "unknown.example.com")
		resp, err := client.Get("https://unknown.example.com/health")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != "default" {
			t.Fatalf("body = %q, want default", body)
		}
	})
}

func TestListenerConcurrentClients(t *testing.T) {
	backend := startTLSBackend(t, "ok")
	host, port := backendRoute(t, backend)
	routerAddr := startTestRouter(t, []routing.Route{
		{Domain: "prom.example.com", Host: host, Port: port},
		{Domain: "default", Host: host, Port: port},
	})

	const clients = 8
	var wg sync.WaitGroup
	errs := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := routerHTTPClient(routerAddr, "prom.example.com")
			resp, err := client.Get("https://prom.example.com/health")
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				errs <- err
				return
			}
			if string(body) != "ok" {
				errs <- fmt.Errorf("body = %q, want ok", body)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestListenerMaxConnections(t *testing.T) {
	backend := startTLSBackend(t, "ok")
	host, port := backendRoute(t, backend)
	routerAddr := startTestRouterLimited(t, []routing.Route{
		{Domain: "prom.example.com", Host: host, Port: port},
		{Domain: "default", Host: host, Port: port},
	}, 1)

	client := routerHTTPClient(routerAddr, "prom.example.com")
	resp, err := client.Get("https://prom.example.com/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}

	resp, err = client.Get("https://prom.example.com/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("second request body = %q, want ok", body)
	}
}
