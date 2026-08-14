package server

import (
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync"
)

func copyStreams(wg *sync.WaitGroup, ic Connection, oc Connection) {
	defer ic.Close()
	defer oc.Close()
	defer wg.Done()
	_, err := io.Copy(oc, ic)
	if err != nil && !isExpectedCopyError(err) {
		log.Println("error in copy", err)
	}
}

func isExpectedCopyError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}

func CopyStreamsBidirectional(ic *InboundConnection, oc *OutboundConnection) {
	var wg sync.WaitGroup
	wg.Add(2)
	// Concurrent copy of data between connections
	go copyStreams(&wg, ic, oc)
	go copyStreams(&wg, oc, ic)
	wg.Wait()
}
