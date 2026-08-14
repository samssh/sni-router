package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/pires/go-proxyproto"
)

func TestProxyHeaderWrittenBeforePayload(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backendLn.Close() })

	const connections = 100
	results := make(chan error, connections)

	go func() {
		for {
			c, err := backendLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
				br := bufio.NewReader(c)
				if _, err := proxyproto.Read(br); err != nil {
					results <- fmt.Errorf("proxy header: %w", err)
					return
				}
				b, err := br.ReadByte()
				if err != nil {
					results <- fmt.Errorf("payload: %w", err)
					return
				}
				if b != 0x16 {
					results <- fmt.Errorf("expected TLS handshake 0x16 after PROXY header, got 0x%02x", b)
					return
				}
				results <- nil
			}(c)
		}
	}()

	routerLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routerLn.Close() })

	metrics := newTestMetrics()
	payload := []byte{0x16, 0x03, 0x01, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00}

	for i := 0; i < connections; i++ {
		client, err := net.Dial("tcp", routerLn.Addr().String())
		if err != nil {
			t.Fatal(err)
		}

		inbound, err := routerLn.Accept()
		if err != nil {
			_ = client.Close()
			t.Fatal(err)
		}

		ic := newInboundConnection(inbound, metrics)
		oc, err := dialTcp(backendLn.Addr().String(), "prom.example.com", 0, metrics)
		if err != nil {
			_ = client.Close()
			ic.Close()
			t.Fatal(err)
		}

		if _, err := client.Write(payload); err != nil {
			_ = client.Close()
			ic.Close()
			oc.Close()
			t.Fatal(err)
		}

		if err := writeProxyHeader(ic, oc); err != nil {
			_ = client.Close()
			ic.Close()
			oc.Close()
			t.Fatal(err)
		}
		_ = client.Close()

		done := make(chan struct{})
		go func() {
			CopyStreamsBidirectional(ic, oc)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = client.Close()
			ic.Close()
			oc.Close()
			t.Fatal("timed out copying streams")
		}
	}

	for i := 0; i < connections; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Errorf("connection %d: %v", i, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for backend result %d", i)
		}
	}
}

func TestCopyDoesNotPrecedeHeader(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backendLn.Close() })

	firstByte := make(chan byte, 1)
	go func() {
		c, err := backendLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var b [1]byte
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return
		}
		firstByte <- b[0]
	}()

	client, router := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = router.Close()
	})

	metrics := newTestMetrics()
	ic := newInboundConnection(router, metrics)
	oc, err := dialTcp(backendLn.Addr().String(), "prom.example.com", 0, metrics)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeProxyHeader(ic, oc); err != nil {
		t.Fatal(err)
	}

	select {
	case b := <-firstByte:
		if b != 0x0d {
			t.Fatalf("first byte should be PROXY v2 signature 0x0d, got 0x%02x", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend did not receive the PROXY header before stream copy started")
	}

	ic.Close()
	oc.Close()
}
