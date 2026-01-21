package server

import (
	"io"
	"log"
	"sync"
)

func copyStreams(wg *sync.WaitGroup, ic Connection, oc Connection) {
	defer ic.Close()
	defer oc.Close()
	defer wg.Done()
	_, err := io.Copy(oc, ic)
	if err != nil {
		log.Println("error in copy", err)
		return
	}
}

func CopyStreamsBidirectional(ic *InboundConnection, oc *OutboundConnection) {
	var wg sync.WaitGroup
	wg.Add(2)
	// Concurrent copy of data between connections
	go copyStreams(&wg, ic, oc)
	go copyStreams(&wg, oc, ic)
	wg.Wait()
}
