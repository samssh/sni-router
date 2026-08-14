package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
	"sni-router/internal/routing"
)

func TestHandleConnectionSNIPeekFailure(t *testing.T) {
	client, inbound := net.Pipe()
	_ = client.Close()

	closed := make(chan struct{})
	go func() {
		newTestListener(t, []routing.Route{
			{Domain: "default", Host: "127.0.0.1", Port: 9},
		}).handleConnection(inbound)
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConnection did not return after peek failure")
	}
}

func TestHandleConnectionRouteError(t *testing.T) {
	client, inbound := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	closed := make(chan struct{})
	go func() {
		newTestListener(t, []routing.Route{
			{Domain: "default", Host: "127.0.0.1", Port: 9},
		}).handleConnection(inbound)
		close(closed)
	}()

	if _, err := client.Write([]byte("GET / HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConnection did not return after route error")
	}
}

func TestHandleConnectionDialError(t *testing.T) {
	hello := clientHelloWithSNI(t, "prom.example.com")
	client, inbound := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	closed := make(chan struct{})
	go func() {
		newTestListener(t, []routing.Route{
			{Domain: "prom.example.com", Host: "127.0.0.1", Port: 1},
			{Domain: "default", Host: "127.0.0.1", Port: 1},
		}).handleConnection(inbound)
		close(closed)
	}()

	if _, err := client.Write(hello); err != nil {
		t.Fatal(err)
	}

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConnection did not return after dial error")
	}
}

func TestHandleConnectionWithoutProxy(t *testing.T) {
	hello := clientHelloWithSNI(t, "prom.example.com")

	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backendLn.Close() })

	received := make(chan []byte, 1)
	go func() {
		c, err := backendLn.Accept()
		if err != nil {
			received <- nil
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, len(hello)+16)
		n, _ := io.ReadFull(c, buf[:len(hello)])
		received <- append([]byte(nil), buf[:n]...)
	}()

	host, port := backendHostPort(t, backendLn.Addr())
	client, inbound := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan struct{})
	go func() {
		newTestListener(t, []routing.Route{
			{Domain: "prom.example.com", Host: host, Port: port},
			{Domain: "default", Host: host, Port: port},
		}).handleConnection(inbound)
		close(done)
	}()

	if _, err := client.Write(hello); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-received:
		if len(got) == 0 {
			t.Fatal("backend received nothing")
		}
		if got[0] != 0x16 {
			t.Fatalf("expected raw TLS record, got 0x%02x", got[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for backend payload")
	}

	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConnection did not finish")
	}
}

func TestHandleConnectionWithProxy(t *testing.T) {
	hello := clientHelloWithSNI(t, "prom.example.com")

	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backendLn.Close() })

	result := make(chan error, 1)
	go func() {
		c, err := backendLn.Accept()
		if err != nil {
			result <- err
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		br := bufio.NewReader(c)
		if _, err := proxyproto.Read(br); err != nil {
			result <- err
			return
		}
		b, err := br.ReadByte()
		if err != nil {
			result <- err
			return
		}
		if b != 0x16 {
			result <- fmt.Errorf("expected TLS handshake 0x16 after PROXY header, got 0x%02x", b)
			return
		}
		result <- nil
	}()

	host, port := backendHostPort(t, backendLn.Addr())
	client, inbound := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan struct{})
	go func() {
		newTestListener(t, []routing.Route{
			{Domain: "prom.example.com", Host: host, Port: port, UseProxy: true},
			{Domain: "default", Host: host, Port: port},
		}).handleConnection(inbound)
		close(done)
	}()

	if _, err := client.Write(hello); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for proxied payload")
	}

	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleConnection did not finish")
	}
}

