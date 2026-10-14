# Selected upstream sync: IVO-94

This sync adapts the selected improvements from upstream
`bbf6893ca7afad27f1d00a0f5a45982a549c6ed6` to this fork's existing boundaries.
The baseline is `dev` at `38b16eda57c263bc0827c6c9ebe22902212b867b`; the feature
branch is `feat/ivo-94-upstream-sync`. The [master specification](https://linear.app/ivoxygen/issue/IVO-94)
defines the finite scope. It adds no new channel/provider product direction or
second delegation runtime. Existing Telegram replies, inline privacy, ASR/Riva,
spawn/subagent, web search, archives and Saved facts remain preservation contracts.

## Implemented increments

The table points to the issue and main implementation boundary, rather than
duplicating each ticket's complete file inventory.

| Issue | Result | Main affected modules/files |
| --- | --- | --- |
| [IVO-95](https://linear.app/ivoxygen/issue/IVO-95) | v3 channel/config migration and strict diagnostics | `pkg/config/channel_list.go`, `channel_security.go`, `diagnostics.go`; channel/routing/chatmemory adapters; launcher API/TUI/forms |
| [IVO-96](https://linear.app/ivoxygen/issue/IVO-96) | Telegram location with text/topic/reply association | `pkg/channels/telegram/telegram_batching.go` |
| [IVO-97](https://linear.app/ivoxygen/issue/IVO-97) | Benchmark-network SSRF exclusion | `pkg/tools/web.go` |
| [IVO-98](https://linear.app/ivoxygen/issue/IVO-98) | Scheme-less URL handling in Shell workspace guard | `pkg/tools/shell.go` |
| [IVO-91](https://linear.app/ivoxygen/issue/IVO-91) | Empty MCP arguments become an object | `pkg/mcp/manager.go` |
| [IVO-99](https://linear.app/ivoxygen/issue/IVO-99) | Durable, authorized large MCP text artifacts | `pkg/tools/artifacts.go`, `mcp_tool.go`, `filesystem.go`; agent request scopes and atomic writer |
| [IVO-100](https://linear.app/ivoxygen/issue/IVO-100) | Explicit Linux Shell/stdio MCP isolation | `pkg/isolation/`; Shell/process helpers and MCP manager |
| [IVO-101](https://linear.app/ivoxygen/issue/IVO-101) | Exact fallback identity and safe actionable errors | `pkg/providers/config_identity.go`, factory/fallback/classifier/guidance; `pkg/agent/model_resolution.go`, `error_format.go` |
| [IVO-102](https://linear.app/ivoxygen/issue/IVO-102) | Complete prepared-request context budget | `pkg/agent/context_budget.go`, `context.go`, `loop.go`; tokenizer estimator |
| [IVO-103](https://linear.app/ivoxygen/issue/IVO-103) | Scoped Seahorse retrieval, compaction and lifetime | `pkg/seahorse/`; agent context manager/workers/tools/reset/session snapshot |
| [IVO-104](https://linear.app/ivoxygen/issue/IVO-104) | Resumable active-context import, Seahorse default | `pkg/seahorse/import.go`, schema/source store; agent context manager and config defaults |
| [IVO-105](https://linear.app/ivoxygen/issue/IVO-105) | Persistent dashboard password and revocable sessions | `web/backend/dashboardauth/`, auth API/middleware; launcher routes/menu/pages |
| [IVO-106](https://linear.app/ivoxygen/issue/IVO-106) | Explicit selected-model minimal inference | model inference/credential API; shared model editor probe component |
| [IVO-107](https://linear.app/ivoxygen/issue/IVO-107) | Thought/tool activity and reconnect outcomes | agent progress/events, Pico protocol/replay, existing chat controller and Zustand store |
| [IVO-108](https://linear.app/ivoxygen/issue/IVO-108) | Paste/drop/select images to current compatible model | Pico upload/ownership and launcher proxy; composer/upload hook/store/media input |
| [IVO-109](https://linear.app/ivoxygen/issue/IVO-109) | Authenticated Skill Hub search/detail/install | skill registry/install/provenance, Hub/config API, existing skills page |
| [IVO-110](https://linear.app/ivoxygen/issue/IVO-110) | Reproducible launcher/runtime images | Dockerfiles/Compose/security profile, Makefile, packaging acceptance script |
| [IVO-92](https://linear.app/ivoxygen/issue/IVO-92) | Actual documented MCP management CLI | `cmd/picoclaw/internal/mcp/`, root registration; config transport and MCP diagnostics |

## Upgrade and operator contracts

Back up application configuration and persistent state before deployment. Old
unversioned/v1/v2 configuration loads in memory; loading never rewrites it.
Explicit saves use v3. For a read-only configuration mount, prepare a new file
with `picoclaw migrate --config ./config.json --output ./config.v3.json`.
[Configuration v3](configuration-v3.md) covers sidecars, encryption, environment
precedence and named-channel settings. Keep the existing Telegram instance named
`telegram` to preserve its conversation/archive identity; renaming creates a
different boundary. Channel credentials remain instance-scoped; model credentials
follow the exact selected configuration.

Seahorse is the active-context default. First use imports only the effective
current generation, including summary-only history; interrupted imports resume
without duplicate sources. Full canonical transcripts, archive/index/embeddings
and Saved facts remain in their existing stores. Explicit
`agents.defaults.context_manager: "legacy"` preserves legacy compatibility and
new canonical turns. Both engines cancel and join their workers before closing
owned storage/providers; reset stays nonblocking and fences stale publication.
See [scoped active context](seahorse-context.md) and [request budget](context-budget.md).

Dashboard setup stores a bcrypt hash in `launcher-auth.db` alongside the config.
Persist its directory. Session cookies require login again after restart;
logout closes the current session's active proxies, and password changes revoke
all sessions. HTTP, WebSocket and media routes share the authentication boundary
while existing IP/listen and Pico token policies still apply. See
[dashboard access](dashboard-auth.md). Model validation is local; **Test model
(may incur usage)** is an explicit single short inference using that selected
saved/edited configuration. An endpoint/provider edit cannot carry a saved secret
to a different destination. See [model tests](web-model-tests.md).

Web [activity](web-chat-activity.md), [images](web-chat-images.md) and
[Skill Hub](skill-hub.md) extend their existing runtime/controller/install paths.
Reasoning remains excluded from Telegram answers. Uploads require ownership and
configured limits; no image fallback silently discards attachments. Registry
credentials stay server-side, install safety checks remain enforced, and existing
import/delete remain available.

[MCP commands](mcp-cli.md) use the existing config writer and runtime transports.
Plain list and disabled-server show are read only; status, enabled-server show
and test are explicit connectivity probes and never execute tools. [Large text artifacts](mcp-artifacts.md) retain exact agent
and base-conversation authority across restart/reset. They have no TTL; optional
quota defaults off and cleanup is explicit after stopping all writers. A storage
failure reports that the external operation completed and stops without replay.

[Linux isolation](isolation.md) defaults off. Enabled Shell/local MCP processes
share one launcher with explicit workspace/runtime/extra mounts, filtered env and
default network access. Denied isolation never retries unsandboxed. Canceling a
local MCP call stops its whole server and concurrent calls; a later independent
call can reconnect. [Docker deployment](docker.md) persists `/data`, runs as UID
1000, and offers an explicit tested Bubblewrap policy overlay. Writable state is
still needed when the config file itself is read-only. Host namespace/LSM policy
must support the chosen isolation contract.

## Verification and remaining limits

The verified runtime code is `6d827319` (followed by documentation and test-format
changes). `make check` passed dependency verification, formatting, vet, complete
Go tests and frontend lint. Full `golangci-lint run ./...` reports zero issues.
The eight frontend auth/chat/image/Hub/model protocol tests passed. The unchanged
frontend also passed the embedded TypeScript/Vite build with the frozen lockfile.
Focused race checks cover named Pico setup/start/token/media, legacy and Seahorse
reset/close/reload/model ownership, source import/retrieval, public provider
errors, MCP artifacts and shared Hub/agent installation. Providers and registries
in those checks are local fakes; no paid credentials or live Telegram/ASR calls
are used. The final two-axis review at this code revision closed all seven
Standards and four Spec findings, with none open. The fixes preserve one
active-context import path, existing generation gates and shared config/install
boundaries.

For final source-image acceptance, build the three source tags and run
`make docker-test-packaging` as described in [Docker](docker.md). The script prints
source labels, image content IDs and the acceptance checkout; those identify the
actual selected artifacts independently of this document. Frozen frontend
packages and source Go build targets do not make changing base/APK tags
byte-identical across dates.

Remaining limits:

- Request budgeting is heuristic. A custom provider ignoring cancellation can
  delay shutdown join; reset remains nonblocking and rejects late publication.
- Activity replay is bounded and in-memory. Uploaded images/media references use
  temporary storage and its cleanup lifetime; gateway restart loses the reference
  mapping. Saved dashboard history restores text, not durable image previews.
- Artifact reads load and filter the whole raw record into RAM before pagination.
  The per-read limit bounds returned content, not record allocation. Operator
  records retain raw text, including secrets; preserve private storage/backup
  permissions and use explicit cleanup or optional quota as needed.
- Skill Hub uses bounded ranked-prefix pagination (at most 200 results). Shared
  skill mutation locks coordinate one process, not separate launcher/gateway
  processes. Agent force replaces a staged, validated archive.
- Minimal inference supports OpenAI-compatible HTTP API-key/anonymous endpoints,
  can incur usage and may get no text from a reasoning-heavy model within 32
  output tokens. It does not validate image, OAuth, CLI or native protocols.
- Container state runs as UID 1000; old root-owned volumes need compatible
  ownership. Optional Bubblewrap isolation depends on host kernel/LSM policy and
  explicitly relaxes the outer restrictions described in `docker/ISOLATION.md`.

[Changed-file inventory](upstream-sync-ivo-94-files.txt) lists every affected path
against the pinned dev baseline, including this document and the inventory itself.
