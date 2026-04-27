//go:build linux

package securebytes

import "golang.org/x/sys/unix"

func mlock(b []byte) error {
	return unix.Mlock(b)
}

func munlock(b []byte) error {
	return unix.Munlock(b)
}
