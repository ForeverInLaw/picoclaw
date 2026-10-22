//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package fileutil

import (
	"errors"
	"os"
)

func lockFilePlatform(file *os.File) error {
	return errors.New("protected file locking is unavailable on this platform")
}
