# Linux process isolation

Shell commands and local stdio MCP servers use the same process launcher. Isolation is disabled by default. Enabling it requires Linux, Bubblewrap (`bwrap`) and working user, mount and PID namespaces. An unavailable or denied sandbox fails the affected operation; PicoClaw never retries it outside isolation. HTTP/SSE MCP servers do not run a local process and are unaffected.

```json
{
  "isolation": {
    "enabled": true,
    "disable_network": false,
    "env_allowlist": ["HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"],
    "expose_paths": [
      {"source": "/srv/reference", "target": "/mnt/reference", "mode": "ro"},
      {"source": "/srv/exports", "target": "/mnt/exports", "mode": "rw"}
    ]
  }
}
```

The selected workspace is writable. Existing system runtime directories (`/usr`, `/bin`, `/lib`, `/lib64`) and required resolver, certificate, loader and identity files are mounted read-only. `/proc` belongs to the sandbox PID namespace, so host process environments are hidden. `/tmp` and the default `/home/picoclaw` are private; files there disappear when that process ends. Store durable tool data in the workspace or an explicit writable mount.

Extra paths require absolute `source` and optional absolute `target`, an existing source, and `mode` of `ro` or `rw`. Omitting `target` exposes the source at its existing path. These rules are explicit grants and can override an overlapping built-in mount. Command arguments, missing-file parents, executable directories and an alternate working directory never add mounts. Put a local MCP executable and its dependencies in the workspace or expose them explicitly. Mounted paths must also satisfy Shell's existing command and workspace/allow-path guards.

Isolated processes inherit only `PATH`, `LANG`, `LC_ALL`, `TERM`, `TZ` and names in `env_allowlist`. Home, temporary and XDG paths default to the private filesystem. Shell receives its existing request/channel metadata. MCP receives variables explicitly declared in the server's `env_file` and `env`; `env` overrides the file. Declared variables may override defaults, but their referenced files still require an allowed mount. Other inherited provider credentials and process secrets are omitted.

Network access is enabled by default. Setting `disable_network` to `true` creates a separate network namespace without host network access, including host localhost services. This does not change Shell's existing remote-channel authorization. Environment overrides are `PICOCLAW_ISOLATION_ENABLED`, `PICOCLAW_ISOLATION_DISABLE_NETWORK` and comma-separated `PICOCLAW_ISOLATION_ENV_ALLOWLIST`.

Shell cancellation and timeout stop its process group. The sandbox PID namespace also confines detached descendants that create their own sessions or process groups. Local MCP startup obeys the connecting caller's deadline; an established server belongs to the manager and survives completion of that initializing request. Closing the manager interrupts active calls and stops its owned subprocesses.

The MCP protocol cannot identify all OS descendants belonging to one tool request. Canceling or timing out a local call therefore stops the entire local server and interrupts its concurrent calls. Those calls fail explicitly and are never replayed. A later independent call reconnects once using the stored server configuration. Workspace files survive, but in-memory server session state is lost. With isolation disabled, normal environment/filesystem access is preserved and process groups still provide cooperative descendant cleanup; containment of detached processes requires enabled Linux isolation.

On a native Linux host, install Bubblewrap using the distribution's package manager and ensure its user namespace policy permits launching it. The launcher probes the actual namespace and mount setup before running the tool, so merely finding the binary is insufficient. Unsupported platforms report that enabled isolation requires Linux; disabled isolation retains their existing execution path.

The PR test job installs Bubblewrap so the real Shell and MCP isolation tests run in CI. Locally, those tests skip when `bwrap` is absent; an installed but unusable sandbox fails the tests. Tests for missing or denied isolation still run without Bubblewrap and verify that no command or MCP server starts outside the sandbox.

Containers add kernel, seccomp, AppArmor and capability restrictions. Installing Bubblewrap in an image does not establish that namespaces and mounts are permitted. Errors include the failed probe and point to these runtime prerequisites. Select a policy verified for your host and image; privileged Docker mode is not a general requirement. See [Docker documentation](docker.md) for image/runtime configuration.
