//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package config

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockConfigPlatform(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return errConfigLockBusy
		}
		if err != unix.EINTR {
			return err
		}
	}
}
