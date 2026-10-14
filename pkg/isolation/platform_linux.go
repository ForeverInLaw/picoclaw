//go:build linux

package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/config"
)

func prepareIsolation(cmd *exec.Cmd, policy config.IsolationConfig, workspace string) error {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return fmt.Errorf("%s; install bubblewrap: %w", capabilityHint, err)
	}
	if workspace == "" {
		return fmt.Errorf("Linux isolation requires an explicit workspace")
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return fmt.Errorf("isolation workspace: %w", err)
	}
	info, statErr := os.Stat(workspace)
	if statErr != nil || !info.IsDir() {
		return fmt.Errorf("isolation workspace must be an existing directory: %s", workspace)
	}
	if cmd.Err != nil {
		return fmt.Errorf("isolated executable lookup: %w", cmd.Err)
	}
	cwd := cmd.Dir
	if cwd == "" {
		cwd = workspace
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("isolated working directory: %w", err)
	}
	commandPath := cmd.Path
	if !filepath.IsAbs(commandPath) {
		commandPath = filepath.Join(cwd, commandPath)
	}
	args := []string{
		bwrap,
		"--die-with-parent",
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--cap-drop",
		"ALL",
		"--tmpfs",
		"/tmp",
		"--dir",
		"/home/picoclaw",
		"--proc",
		"/proc",
		"--dev",
		"/dev",
	}
	if policy.DisableNetwork {
		args = append(args, "--unshare-net")
	}
	// Only these runtime paths are inherited. Executable directories, cwd and
	// absolute command arguments never add mounts or grant writable parents.
	for _, path := range []string{
		"/usr", "/bin", "/lib", "/lib64", "/etc/ld.so.cache", "/etc/resolv.conf", "/etc/hosts",
		"/etc/nsswitch.conf", "/etc/passwd", "/etc/group", "/etc/ssl", "/etc/pki", "/etc/alternatives", "/etc/localtime",
	} {
		if _, err := os.Stat(path); err == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	args = append(args, "--bind", workspace, workspace)
	seen := make(map[string]bool)
	for _, path := range policy.ExposePaths {
		if path.Source == "" || !filepath.IsAbs(path.Source) || path.Target != "" && !filepath.IsAbs(path.Target) {
			return fmt.Errorf("isolation expose_paths source and target must be absolute paths")
		}
		if path.Mode != "ro" && path.Mode != "rw" {
			return fmt.Errorf("isolation expose_paths mode must be ro or rw")
		}
		source := filepath.Clean(path.Source)
		target := source
		if path.Target != "" {
			target = filepath.Clean(path.Target)
		}
		if seen[target] {
			return fmt.Errorf("duplicate isolation expose_paths target: %s", target)
		}
		seen[target] = true
		if _, err := os.Stat(source); err != nil {
			return fmt.Errorf("isolation expose_paths source %s: %w", source, err)
		}
		flag := "--ro-bind"
		if path.Mode == "rw" {
			flag = "--bind"
		}
		args = append(args, flag, source, target)
	}
	args = append(args, "--chdir", cwd, "--", commandPath)
	args = append(args, cmd.Args[1:]...)
	cmd.Path, cmd.Args, cmd.Dir = bwrap, args, ""
	return nil
}
