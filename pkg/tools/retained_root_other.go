//go:build !unix

package tools

import (
	"fmt"
	"os"
	"runtime"
)

func validatePrivateRoot(os.FileInfo) error {
	// Windows permission bits do not describe ACLs; retain the host ACL policy.
	if runtime.GOOS == "windows" {
		return nil
	}
	return fmt.Errorf("retained file directory ownership checks are unavailable on %s", runtime.GOOS)
}
