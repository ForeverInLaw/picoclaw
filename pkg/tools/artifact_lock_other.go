//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package tools

import (
	"fmt"
	"os"
)

func lockArtifactFile(*os.File) error {
	return fmt.Errorf("artifact quotas require supported OS file locking")
}
