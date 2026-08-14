package sni

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"io"
	"net"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"sni-router/internal/monitoring"
)

func testMetrics() *monitoring.Metrics {
	return monitoring.NewMetricsWithRegisterer(prometheus.NewRegistry())
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

func TestExtractSNI(t *testing.T) {
	validHello := clientHelloWithSNI(t, "prom.example.com")

	// TLS handshake record that is too short to parse; Peek succeeds, then recover().
	malformedPanic := []byte{
		0x16, 0x03, 0x01, 0x00, 0x0a,
		0x01, 0x00, 0x00, 0x06, 0x03, 0x03, 0x00, 0x00, 0x00, 0x00,
	}

	// Valid-looking record whose handshake type is not ClientHello.
	notClientHello := []byte{
		0x16, 0x03, 0x01, 0x00, 0x04,
		0x02, 0x00, 0x00, 0x00,
	}

	// ClientHello with empty extensions (no SNI).
	noSNI := buildClientHelloWithoutSNI()

	tests := []struct {
		name      string
		input     []byte
		wantSNI   string
		wantTLS   bool
		wantErr   bool
		checkPeek bool
	}{
		{
			name:      "valid client hello with sni",
			input:     validHello,
			wantSNI:   "prom.example.com",
			wantTLS:   true,
			checkPeek: true,
		},
		{
			name:    "non-tls first byte",
			input:   []byte("GET / HTTP/1.1\r\n\r\n"),
			wantSNI: "",
			wantTLS: false,
		},
		{
			name:    "handshake is not client hello",
			input:   notClientHello,
			wantErr: true,
		},
		{
			name:    "truncated record header",
			input:   []byte{0x16, 0x03},
			wantErr: true,
		},
		{
			name:    "truncated client hello body",
			input:   []byte{0x16, 0x03, 0x01, 0x00, 0xff, 0x01},
			wantErr: true,
		},
		{
			name:    "client hello without sni extension",
			input:   noSNI,
			wantSNI: "",
			wantTLS: true,
		},
		{
			name:    "malformed lengths return error",
			input:   malformedPanic,
			wantErr: true,
		},
		{
			name:    "empty sni is rejected",
			input:   buildClientHelloWithSNI(""),
			wantErr: true,
		},
		{
			name:    "sni with nul is rejected",
			input:   buildClientHelloWithSNI("bad\x00name.example.com"),
			wantErr: true,
		},
		{
			name:      "client hello split across two records",
			input:     splitTLSRecords(validHello),
			wantSNI:   "prom.example.com",
			wantTLS:   true,
			checkPeek: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader(tt.input))
			sniValue, isTLS, err := ExtractSNI(r, testMetrics())
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sniValue != tt.wantSNI {
				t.Fatalf("sni = %q, want %q", sniValue, tt.wantSNI)
			}
			if isTLS != tt.wantTLS {
				t.Fatalf("isTls = %v, want %v", isTLS, tt.wantTLS)
			}
			if tt.checkPeek {
				got, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, tt.input) {
					t.Fatalf("peek consumed bytes: got %d bytes, want %d", len(got), len(tt.input))
				}
			}
		})
	}
}

func buildClientHelloWithSNI(name string) []byte {
	nameBytes := []byte(name)
	sniName := []byte{0x00, byte(len(nameBytes) >> 8), byte(len(nameBytes))}
	sniName = append(sniName, nameBytes...)
	sniList := []byte{byte(len(sniName) >> 8), byte(len(sniName))}
	sniList = append(sniList, sniName...)
	ext := []byte{0x00, 0x00, byte(len(sniList) >> 8), byte(len(sniList))}
	ext = append(ext, sniList...)
	extensions := []byte{byte(len(ext) >> 8), byte(len(ext))}
	extensions = append(extensions, ext...)

	body := []byte{0x03, 0x03}
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)
	body = append(body, 0x00, 0x02, 0x00, 0x2f)
	body = append(body, 0x01, 0x00)
	body = append(body, extensions...)

	hsLen := len(body)
	handshake := []byte{0x01, byte(hsLen >> 16), byte(hsLen >> 8), byte(hsLen)}
	handshake = append(handshake, body...)
	recLen := len(handshake)
	return append([]byte{0x16, 0x03, 0x01, byte(recLen >> 8), byte(recLen)}, handshake...)
}

func splitTLSRecords(hello []byte) []byte {
	payload := hello[5:]
	mid := len(payload) / 2
	if mid < 8 {
		mid = 8
	}
	rec1 := []byte{0x16, hello[1], hello[2], byte(mid >> 8), byte(mid)}
	rec1 = append(rec1, payload[:mid]...)
	rest := payload[mid:]
	rec2 := []byte{0x16, hello[1], hello[2], byte(len(rest) >> 8), byte(len(rest))}
	return append(rec1, append(rec2, rest...)...)
}

func buildClientHelloWithoutSNI() []byte {
	// Minimal ClientHello: handshake record, no extensions.
	// handshake: type=1, length=43 (2 ver + 32 random + 1 sid + 2 cipher len + 2 cipher + 1 comp)
	handshake := []byte{0x01, 0x00, 0x00, 0x29}
	handshake = append(handshake, 0x03, 0x03)       // version
	handshake = append(handshake, make([]byte, 32)...) // random
	handshake = append(handshake, 0x00)             // session id len
	handshake = append(handshake, 0x00, 0x02)       // cipher suites len
	handshake = append(handshake, 0x00, 0x2f)       // TLS_RSA_WITH_AES_128_CBC_SHA
	handshake = append(handshake, 0x01, 0x00)       // compression methods
	// no extensions

	recordLen := len(handshake)
	header := []byte{0x16, 0x03, 0x01, byte(recordLen >> 8), byte(recordLen)}
	return append(header, handshake...)
}
