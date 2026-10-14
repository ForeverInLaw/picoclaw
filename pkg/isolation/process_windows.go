//go:build windows

package isolation

import (
	"os/exec"
	"strconv"
)

func prepareTermination(cmd *exec.Cmd) {}

func Terminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
	return nil
}
