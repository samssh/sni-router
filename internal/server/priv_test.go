package server

import "testing"

func TestDropPrivilegesNoop(t *testing.T) {
	if err := dropPrivileges(0, 0); err != nil {
		t.Fatal(err)
	}
}
