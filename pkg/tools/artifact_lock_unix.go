//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package tools

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockArtifactFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err != unix.EINTR {
			return err
		}
	}
}
