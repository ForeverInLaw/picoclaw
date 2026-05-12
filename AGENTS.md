# AGENTS.md

Operating rules for AI coding agents working in this repo. Read **before** editing code, committing, or deploying.

## Repository layout

This project has **multiple local clones** with diverging branches. Confusing them caused a production outage (deployed a binary built from a branch with a different config schema → gateway refused to start).

- **Canonical working tree** for active development: the worktree on the user's machine that tracks the active dev branch (currently `merge/origin-main-into-dev`). All edits, builds and deploys MUST come from there.
- A second clone tracking `main` exists for upstream sync work. **Never** build or deploy from it unless the deploy target is also on `main`. The two branches have different struct schemas (e.g. `PlaceholderConfig.Text` is `string` on `main`, `FlexibleStringSlice` on dev).

Before doing anything that touches the server, verify:

```bash
git rev-parse --abbrev-ref HEAD     # should be the dev branch
git log -1 --oneline                # head commit should match what server expects
```

If unsure which tree is canonical, ask the user.

## Commit conventions

- Conventional Commits (`feat(scope): …`, `fix(scope): …`, `refactor(scope): …`, etc.).
- Subject ≤ 70 chars, imperative mood.
- **No AI attribution.** Do not append `Co-Authored-By: Claude …`, `Generated with …`, or similar trailers to commit messages or PR bodies.
- Atomic commits. One logical change per commit. Don't bundle a feature with an unrelated refactor.
- Use HEREDOC for multiline commit messages to keep formatting clean.

## Build

Server target is `linux/arm64`. Build with:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build \
  -tags "goolm,stdjson" \
  -ldflags "-s -w" \
  -o build/picoclaw-linux-arm64 \
  ./cmd/picoclaw
```

Verify locally before deploy:

```bash
go build ./...                                              # all packages compile
go test ./pkg/channels/telegram ./pkg/agent -count=1        # touched-area tests pass
```

Pre-existing test failures unrelated to your change (e.g. Windows path-separator tests, regex edge cases) are acceptable — confirm by `git stash`-ing your diff and re-running, or by inspecting the test contents. Never disable a failing test to make the suite green.

## Deploy

The server runs a binary at `/usr/local/bin/picoclaw`. The picoclaw-launcher systemd unit owns the gateway process and exposes a local HTTP API to restart it.

Deploy procedure (replace placeholders with values from the user's environment / private notes — do not hard-code them in the repo):

1. Upload binary:
   ```bash
   scp build/picoclaw-linux-arm64 $DEPLOY_USER@$DEPLOY_HOST:/tmp/picoclaw-new
   ```

2. Install + restart + health check, single ssh call:
   ```bash
   ssh $DEPLOY_USER@$DEPLOY_HOST '
     sudo install -m 0755 /tmp/picoclaw-new /usr/local/bin/picoclaw && \
     TOKEN=$(sudo systemctl show picoclaw-launcher.service -p Environment \
             | grep -oP "PICOCLAW_LAUNCHER_TOKEN=\K[^[:space:]]+") && \
     curl -sX POST http://127.0.0.1:$LAUNCHER_PORT/api/gateway/restart \
          -H "Authorization: Bearer $TOKEN" -o /dev/null \
          -w "restart=%{http_code}\n" && \
     sleep 5 && \
     curl -s http://127.0.0.1:$GATEWAY_PORT/health -o /dev/null \
          -w "health=%{http_code}\n"
   '
   ```

   Expect `restart=200` and `health=200`. Anything else means the new binary failed to start — check launcher and gateway logs on the server (see "Diagnostics" below).

3. If direct SSH is unreliable, use the configured ProxyJump host (`ssh -J $JUMP_USER@$JUMP_HOST $DEPLOY_USER@$DEPLOY_HOST`).

### Token retrieval

The launcher token lives in the systemd unit's `Environment=` directive, **not** in `~/.picoclaw/config.json`. Earlier attempts to read it from the config file failed; the unit is the source of truth.

If the `systemctl show` lookup returns nothing, fall back to:

```bash
sudo grep -hoP 'PICOCLAW_LAUNCHER_TOKEN=\K[^[:space:]"]+' /etc/systemd/system/picoclaw*.service | head -1
```

## Diagnostics

Logs on the server (paths relative to the deploy user's home):

- `~/.picoclaw/logs/launcher.log` — launcher (long-lived; covers gateway lifecycle events).
- `~/.picoclaw/logs/gateway.log` — gateway (the picoclaw process started by the launcher).
- `~/.picoclaw/logs/gateway_panic.log` — gateway panics / fatal stderr.

If the gateway fails to start after deploy:

```bash
ssh $DEPLOY_USER@$DEPLOY_HOST '
  tail -50 ~/.picoclaw/logs/launcher.log | grep -iE "gateway|exit|error";
  tail -30 ~/.picoclaw/logs/gateway_panic.log
'
```

Common failure mode: schema mismatch between the deployed binary and the existing `~/.picoclaw/config.json`. Symptom: `json: cannot unmarshal X into Go struct field Y`. Fix by building from the branch whose schema matches the live config (see "Repository layout" above).

To reproduce the startup error manually (helpful when launcher log is noisy):

```bash
ssh $DEPLOY_USER@$DEPLOY_HOST 'timeout 8 /usr/local/bin/picoclaw gateway 2>&1 | tail -20'
```

## Safety

- Never push to upstream `main` without explicit user instruction.
- Never `git stash` shared working trees behind the user's back. If a `stash`/`reset` would be useful, ask first.
- Never overwrite the production binary with one built from the wrong branch — verify the branch and `git log -1` first.
- Don't skip pre-commit hooks (`--no-verify`) or signing flags unless the user explicitly asks.
- Rollback: there is no automatic backup of the previous binary. If you are about to install a binary you suspect might be wrong, copy the live binary to `/tmp/picoclaw-prev` first:
  ```bash
  ssh $DEPLOY_USER@$DEPLOY_HOST 'sudo cp /usr/local/bin/picoclaw /tmp/picoclaw-prev'
  ```

## Code style

- Read the repo's existing patterns before adding new ones; mirror what's already there.
- For frontend / agent / animation specifics, follow the user's global AGENTS guidelines (TanStack Query for server state, Zustand with selectors for UI state, GSAP via `useGSAP()` for animation, etc.) — these live in the user's private notes, not here.
- `go vet ./...` clean before commit.
- Do not introduce abstractions, fallbacks, or comments beyond what the task requires.
