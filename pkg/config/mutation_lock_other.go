//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package config

import (
	"errors"
	"os"
)

func lockConfigPlatform(file *os.File) error {
	return errors.New("configuration file locking is unavailable on this platform")
}
