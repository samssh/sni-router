package server

import (
	"crypto/tls"
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"sni-router/internal/monitoring"
	"sni-router/internal/routing"
)

func newTestMetrics() *monitoring.Metrics {
	return monitoring.NewMetricsWithRegisterer(prometheus.NewRegistry())
}

func newTestListener(t *testing.T, routes []routing.Route) *Listener {
	t.Helper()
	router, err := routing.NewSNIRouter(routes)
	if err != nil {
		t.Fatal(err)
	}
	return NewListener(router, newTestMetrics(), 0)
}

func clientHelloWithSNI(t *testing.T, serverName string) []byte {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	helloCh := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 16*1024)
		n, err := server.Read(buf)
		if err != nil && n == 0 {
			helloCh <- nil
			return
		}
		helloCh <- append([]byte(nil), buf[:n]...)
		_ = server.Close()
	}()

	tlsConn := tls.Client(client, &tls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: true,
	})
	_ = tlsConn.Handshake()
	_ = tlsConn.Close()

	hello := <-helloCh
	if len(hello) == 0 {
		t.Fatal("did not capture ClientHello")
	}
	return hello
}

func backendHostPort(t *testing.T, addr net.Addr) (string, int) {
	t.Helper()
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected addr type %T", addr)
	}
	return tcpAddr.IP.String(), tcpAddr.Port
}
