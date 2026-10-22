//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package auth

import (
	"errors"
	"os"
)

func lockAuthPlatform(file *os.File) error {
	return errors.New("secure credential file locking is unavailable on this platform")
}
