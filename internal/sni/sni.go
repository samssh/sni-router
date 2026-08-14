package sni

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sni-router/internal/monitoring"
	"strings"
	"time"
	"unicode/utf8"
)

const maxClientHelloPeek = 16 * 1024

func ExtractSNI(r *bufio.Reader, metrics *monitoring.Metrics) (sniValue string, isTls bool, err error) {
	startTime := time.Now()
	defer func() {
		if rec := recover(); rec != nil {
			sniValue = ""
			isTls = false
			err = fmt.Errorf("malformed client hello")
		}

		if err != nil {
			metrics.ObserveParsedSni("error", time.Since(startTime))
		} else {
			metrics.ObserveParsedSni(sniValue, time.Since(startTime))
		}
	}()
	header, err := r.Peek(5)
	if err != nil {
		return "", false, fmt.Errorf("failed to peek record header: %w", err)
	}

	// The first byte should be 22 for a handshake message.
	if header[0] != 22 {
		return "", false, nil
	}

	data, err := peekTLSHandshake(r)
	if err != nil {
		return "", false, err
	}
	pos := 5
	if pos >= len(data) || data[pos] != 0x01 {
		return "", false, fmt.Errorf("tls record is not a client hello")
	}

	pos += 4 // Skip handshake type (1 byte) + length (3 bytes)
	if pos+2+32 >= len(data) {
		return "", false, fmt.Errorf("malformed client hello")
	}

	pos += 2  // Skip TLS version
	pos += 32 // Skip random
	if pos >= len(data) {
		return "", false, fmt.Errorf("malformed client hello")
	}
	sessionIDLen := int(data[pos])
	pos += 1 + sessionIDLen
	if pos+2 > len(data) {
		return "", false, fmt.Errorf("malformed client hello")
	}

	cipherSuiteLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2 + cipherSuiteLen
	if pos >= len(data) {
		return "", false, fmt.Errorf("malformed client hello")
	}

	compressionMethodsLen := int(data[pos])
	pos += 1 + compressionMethodsLen

	// Extensions length
	if pos+2 > len(data) {
		return "", true, nil
	}
	extensionsLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2

	extensionsEnd := pos + extensionsLen
	if extensionsEnd > len(data) {
		return "", false, fmt.Errorf("malformed client hello extensions")
	}

	for pos+4 <= extensionsEnd {
		extType := binary.BigEndian.Uint16(data[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		pos += 4
		if pos+extLen > extensionsEnd {
			return "", false, fmt.Errorf("malformed client hello extensions")
		}

		if extType == 0x00 { // SNI extension
			sniData := data[pos : pos+extLen]
			if len(sniData) < 5 {
				return "", false, fmt.Errorf("malformed sni extension")
			}
			sniLen := int(binary.BigEndian.Uint16(sniData[3:5]))
			if 5+sniLen > len(sniData) {
				return "", false, fmt.Errorf("malformed sni extension")
			}
			serverName := string(sniData[5 : 5+sniLen])
			if err := validateSNI(serverName); err != nil {
				return "", false, err
			}
			return serverName, true, nil
		}

		pos += extLen
	}

	return "", true, nil
}

func peekTLSHandshake(r *bufio.Reader) ([]byte, error) {
	header, err := r.Peek(5)
	if err != nil {
		return nil, fmt.Errorf("failed to peek record header: %w", err)
	}
	recordLen := int(header[3])<<8 | int(header[4])
	need := 5 + recordLen
	if need > maxClientHelloPeek {
		return nil, fmt.Errorf("client hello too large")
	}
	data, err := r.Peek(need)
	if err != nil {
		return nil, fmt.Errorf("failed to peek client hello: %w", err)
	}
	assembled := append([]byte(nil), data...)
	if len(assembled) < 9 {
		return assembled, nil
	}
	handshakeLen := int(assembled[6])<<16 | int(assembled[7])<<8 | int(assembled[8])
	gotPayload := recordLen
	wantPayload := 4 + handshakeLen
	for gotPayload < wantPayload && need < maxClientHelloPeek {
		more, err := r.Peek(need + 5)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return assembled, nil
			}
			return nil, fmt.Errorf("failed to peek client hello: %w", err)
		}
		if more[need] != 22 {
			break
		}
		nextLen := int(more[need+3])<<8 | int(more[need+4])
		need += 5 + nextLen
		if need > maxClientHelloPeek {
			return nil, fmt.Errorf("client hello too large")
		}
		full, err := r.Peek(need)
		if err != nil {
			return nil, fmt.Errorf("failed to peek client hello: %w", err)
		}
		assembled = append(assembled, full[need-nextLen:need]...)
		gotPayload += nextLen
	}
	return assembled, nil
}

func validateSNI(serverName string) error {
	name := strings.TrimSpace(serverName)
	if name == "" {
		return fmt.Errorf("empty sni")
	}
	if strings.ContainsRune(name, 0) || !utf8.ValidString(name) {
		return fmt.Errorf("invalid sni")
	}
	if len(name) > 253 {
		return fmt.Errorf("sni too long")
	}
	if name != serverName {
		return fmt.Errorf("invalid sni")
	}
	return nil
}
