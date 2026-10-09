//go:build !linux

package server

import "syscall"

// freebind is a no-op outside Linux; a bind to a missing address fails and is retried.
func freebind(string, string, syscall.RawConn) error {
	return nil
}
