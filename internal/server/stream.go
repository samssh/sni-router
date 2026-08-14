package server

import (
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync"
)

func copyStreams(wg *sync.WaitGroup, src Connection, dst Connection) {
	defer wg.Done()
	_, err := io.Copy(dst, src)
	if err != nil && !isExpectedCopyError(err) {
		log.Println("error in copy", err)
	}
	closeWrite(dst)
}

func closeWrite(c Connection) {
	if cw, ok := c.(interface{ CloseWrite() }); ok {
		cw.CloseWrite()
		return
	}
	c.Close()
}

func isExpectedCopyError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "closed pipe")
}

func CopyStreamsBidirectional(ic *InboundConnection, oc *OutboundConnection) {
	var wg sync.WaitGroup
	wg.Add(2)
	// Concurrent copy of data between connections
	go copyStreams(&wg, ic, oc)
	go copyStreams(&wg, oc, ic)
	wg.Wait()
}
