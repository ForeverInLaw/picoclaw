//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package auth

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockAuthPlatform(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			return err
		}
	}
}
