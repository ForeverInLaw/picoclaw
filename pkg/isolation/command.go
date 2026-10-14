// Package isolation applies explicit process policy to Shell and local MCP.
package isolation

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
)

type Options struct {
	Policy      config.IsolationConfig
	Workspace   string
	Dir         string
	DeclaredEnv map[string]string
}

// Command prepares a context-bound process group. Enabled isolation fails closed;
// the caller retains ownership of pipes, Start and Wait (including MCP's SDK).
func Command(ctx context.Context, options Options, name string, args ...string) (*exec.Cmd, error) {
	if err := Check(ctx, options.Policy, options.Workspace); err != nil {
		return nil, err
	}
	return prepareCommand(ctx, options, name, args...)
}

func prepareCommand(ctx context.Context, options Options, name string, args ...string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = options.Dir
	cmd.Env = commandEnvironment(cmd.Environ(), options.Policy, options.DeclaredEnv)
	if options.Policy.Enabled {
		if err := prepareIsolation(cmd, options.Policy, options.Workspace); err != nil {
			return nil, err
		}
	}
	prepareTermination(cmd)
	cmd.Cancel = func() error { return Terminate(cmd) }
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

func commandEnvironment(parent []string, policy config.IsolationConfig, declared map[string]string) []string {
	allowed := map[string]bool{"PATH": true, "LANG": true, "LC_ALL": true, "TERM": true, "TZ": true}
	for _, name := range policy.EnvAllowlist {
		allowed[name] = true
	}
	values := make(map[string]string)
	for _, entry := range parent {
		name, value, ok := strings.Cut(entry, "=")
		if ok && (!policy.Enabled || allowed[name]) {
			values[name] = value
		}
	}
	if policy.Enabled {
		values["HOME"] = "/home/picoclaw"
		values["TMPDIR"] = "/tmp"
		values["XDG_CONFIG_HOME"] = "/home/picoclaw/.config"
		values["XDG_CACHE_HOME"] = "/home/picoclaw/.cache"
		values["XDG_STATE_HOME"] = "/home/picoclaw/.local/state"
	}
	for name, value := range declared {
		values[name] = value
	}
	env := make([]string, 0, len(values))
	for name, value := range values {
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env
}

// Check verifies the actual launch policy, including namespace/mount support.
// Native and container restrictions can differ even when bwrap is installed.
func Check(ctx context.Context, policy config.IsolationConfig, workspace string) error {
	if !policy.Enabled {
		return nil
	}
	cmd, err := prepareCommand(ctx, Options{Policy: policy, Workspace: workspace, Dir: workspace}, "/bin/true")
	if err != nil {
		return err
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", capabilityHint, err, strings.TrimSpace(string(output)))
	}
	return nil
}

const capabilityHint = "Linux isolation requires working bubblewrap and user/mount/PID namespaces; check kernel support and container seccomp/AppArmor/capability policy; execution was not retried without isolation"
