//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package fileutil

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockFilePlatform(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return errFileLockBusy
		}
		if err != unix.EINTR {
			return err
		}
	}
}
