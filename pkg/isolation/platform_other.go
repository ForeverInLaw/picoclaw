//go:build !linux

package isolation

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/sipeed/picoclaw/pkg/config"
)

func prepareIsolation(cmd *exec.Cmd, policy config.IsolationConfig, workspace string) error {
	return fmt.Errorf("process isolation is supported on Linux only; enabled isolation cannot run on %s", runtime.GOOS)
}
