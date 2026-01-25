package sni

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"sni-router/internal/monitoring"
	"time"
)

func ExtractSNI(r *bufio.Reader, metrics *monitoring.Metrics) (sniValue string, isTls bool, err error) {
	startTime := time.Now()
	defer func() {
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

	// The next two bytes are the TLS version.
	// The last two bytes are the length of the record.
	recordLen := int(header[3])<<8 | int(header[4])

	// Peek the entire ClientHello message.
	data, err := r.Peek(5 + recordLen)
	if err != nil {
		return "", false, fmt.Errorf("failed to peek client hello: %w", err)
	}
	pos := 5
	// Handshake type must be ClientHello (1)
	if data[pos] != 0x01 {
		return "", false, nil
	}

	pos += 4 // Skip handshake type (1 byte) + length (3 bytes)

	pos += 2  // Skip TLS version
	pos += 32 // Skip random
	sessionIDLen := int(data[pos])
	pos += 1 + sessionIDLen

	cipherSuiteLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2 + cipherSuiteLen

	compressionMethodsLen := int(data[pos])
	pos += 1 + compressionMethodsLen

	// Extensions length
	if pos+2 > len(data) {
		return "", false, nil
	}
	extensionsLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2

	extensionsEnd := pos + extensionsLen
	if extensionsEnd > len(data) {
		return "", false, nil
	}

	for pos+4 <= extensionsEnd {
		extType := binary.BigEndian.Uint16(data[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		pos += 4

		if extType == 0x00 { // SNI extension
			sniData := data[pos : pos+extLen]
			if len(sniData) < 5 {
				return "", false, nil
			}
			sniLen := int(binary.BigEndian.Uint16(sniData[3:5]))
			if 5+sniLen > len(sniData) {
				return "", false, nil
			}
			serverName := string(sniData[5 : 5+sniLen])
			return serverName, true, nil
		}

		pos += extLen
	}

	return "", false, nil
}
