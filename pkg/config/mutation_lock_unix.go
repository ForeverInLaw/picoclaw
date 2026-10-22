//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package config

import (
	"golang.org/x/sys/unix"
	"os"
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
