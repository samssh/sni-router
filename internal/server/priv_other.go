//go:build !unix

package server

import "fmt"

func dropPrivileges(uid, gid int) error {
	if uid > 0 || gid > 0 {
		return fmt.Errorf("dropping privileges is not supported")
	}
	return nil
}
