//go:build unix

package tools

import (
	"fmt"
	"os"
	"syscall"
)

func validatePrivateRoot(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) {
		return fmt.Errorf("retained file directory must be owned by the current user or root")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("retained file directory is group/world-writable; set its permissions to 0700")
	}
	return nil
}
