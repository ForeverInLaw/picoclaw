//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package auth

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockAuthPlatform(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return errAuthLockBusy
		}
		if err != unix.EINTR {
			return err
		}
	}
}
