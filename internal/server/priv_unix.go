//go:build unix

package server

import "syscall"

func dropPrivileges(uid, gid int) error {
	if gid > 0 {
		if err := syscall.Setgid(gid); err != nil {
			return err
		}
	}
	if uid > 0 {
		if err := syscall.Setuid(uid); err != nil {
			return err
		}
	}
	return nil
}
