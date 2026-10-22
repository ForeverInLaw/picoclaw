# Model order and account availability

The Models page configures an instance primary model and ordered reserves. The
same editor can override an existing agent or restore its inherited order. Every
new choice selects a configured entry, so two entries with the same model or alias
can use different account connections, API endpoints or keys.

Gateway startup checks the effective configured chain. An unavailable primary
does not prevent startup when one of its explicitly configured reserves is
available; unrelated models do not satisfy that precondition. A gateway starts
with deferred context-provider selection for every agent, so an unavailable
agent-specific primary cannot block that agent’s reserves during initialization.

The gateway reads saved routing at the start of each user request, even when
`gateway.hot_reload` is disabled. An active task retains its model configuration
and order through local tools, foreground compaction and background summaries.
A successful reserve stays first for the remainder of that task, until it fails
or becomes unavailable. A new request starts with the saved primary again,
subject to existing cooldowns. Updating routing does not replace conversation
sessions, the context manager or canonical transcripts.

## Saved references and inheritance

Each `model_list` entry has a persisted `id`. Routing uses `model-id:<id>` to select
that exact entry. Renaming it, changing its key or reordering the list keeps the
reference intact; a changed endpoint/key/model still changes the runtime provider
cache key. Missing IDs in old files receive deterministic IDs from non-secret
configuration and their occurrence order. Reads do not rewrite the file; the
next authorized configuration save persists those IDs.

Existing alias and provider/model references remain supported, including duplicate
alias round-robin selection. They keep their existing meaning until an operator
explicitly selects a configured entry. Multi-key entries remain a single saved
configuration with derived runtime key slots; editing routing does not flatten
those slots into the saved list.

An agent with no `model` overrides inherits the instance route. Within a model
override, an empty primary inherits the instance primary. An omitted or `null`
fallback list inherits reserves; `[]` explicitly disables them. When instance
fallbacks are omitted or `null`, the selected model's configured fallbacks apply.
These distinctions survive configuration saves and partial updates.

## Protected dashboard API

The existing dashboard authentication protects all these routes:

- `GET /api/models` returns configured entries with their stable `id`.
- `GET /api/models/routing` returns `defaults` and agent overrides.
- `PUT /api/models/routing` updates only the supplied default or agent routes.
- `PUT /api/models/by-id/{id}` edits the selected entry.
- `DELETE /api/models/by-id/{id}` rejects referenced entries with HTTP 409 and
  lists the routing settings that must be changed first.

For example, choose two separately configured entries and leave a worker with
an explicit empty reserve list:

```json
{
  "defaults": {
    "primary": "model-id:configured-A",
    "fallbacks": ["model-id:configured-B"]
  },
  "agents": {
    "worker": {"primary": "", "fallbacks": []}
  }
}
```

Use `{"agents":{"worker":null}}` to restore complete inheritance without
rewriting the instance default. Unknown configured IDs and unknown agent IDs are
rejected. Existing indexed model endpoints remain available for compatibility.
Model and configuration writes share a cross-process mutation guard with account
deletion, preventing a concurrent save from introducing a dangling connection.

## Failure behavior

Every eligible candidate receives its first attempt before the scheduler repeats
one, including candidates after the third. Network errors, timeouts, 5xx, rate
limits, quota exhaustion and authorization failures can advance the chain.
Cancellation and terminal request-format failures stop it. Candidate-specific
capabilities retain the complete Active context and completed local tool results;
unsupported candidates are skipped rather than receiving a truncated request.

Disabled, deleted or unauthenticated connections are checked live before each
model call, independently of the captured order. Confirmed 401 responses suppress
all aliases using that account session until it is reauthorized. An ordinary
model/policy-specific 403 permits fallback without revoking the entire account.
Rate limits use account-wide cooldowns. Hard quota exhaustion suppresses aliases
of that connection and honors a provider's known retry/reset delay. An unknown
quota reset has no fabricated reset time or ETA. Account metadata edits or
reenabling a connection do not erase its authentication/quota state; a new
session clears its own authorization suppression.

Automated coverage uses local provider fixtures and authenticated dashboard
requests. Real OpenAI registration and paid inference acceptance remains the
separate administrator checklist in [Codex accounts](codex-accounts.md).

Model settings include `inactivity_timeout_seconds`. Omit it or set it to `0` for
60 seconds; a positive integer overrides that candidate's limit. Negative values
are rejected. The dashboard exposes the same setting in the existing model
editor. Changes apply to the next user request; an active request keeps its
captured limit.

The limit covers waiting for the first meaningful event and later stalls. Decoded
text, exposed reasoning and tool-call generation reset it. Transport keepalives,
role markers and usage bookkeeping do not. A stalled attempt is canceled and its
stream closed before the next compatible reserve starts. The user's request
remains live. Completed tool results stay in the conversation, and local tool
execution does not use this timer. Retry pauses and context-compaction queue waits
also sit outside individual model attempts. Synchronous preview delivery is
excluded without resetting the clock for model progress. Streaming adapters
require their terminal event or finish reason; a clean early EOF cannot publish
a partial answer or dispatch an unfinished tool call.

`request_timeout` remains a separate total transport deadline where supported.
Non-streaming adapters have no exposed intermediate generation events, so their
inactivity window lasts until a complete response. Explicit dashboard probes keep
their existing shorter overall deadline and never use a reserve to claim success.

## Actual requests and recovery state

**Models → Actual model routing** shows observed requests separately from saved
routing. Each request retains its captured priority, actual configured model and
account connection, failures/skips and known next attempt time. Changing saved
priority does not rewrite a completed request. Subtasks and context operations
have separate records; a late internal result cannot replace its completed
parent's successful model. Normal switches stay quiet in chat. Capacity waits
reuse the existing single progress status.

An inactivity failure records the actual attempt's inactivity limit. A 503 wait
records the scheduler's next eligible attempt; an unknown account quota reset is
shown without an ETA. **Authorized** describes saved credentials, while **Last
successful test** describes an explicit selected account/model inference test.
Neither a token expiry timestamp nor viewing this panel establishes inference
success. Disabling and reauthorization remain distinct from a temporary pause.

A successful explicit account probe can clear unknown quota suppression only
when it belongs to the same authorization session and finishes after the quota
failure. Older probes, rename, enable/disable and rotating refresh do not prove
recovery. Reconnecting still clears only that account's authorization suppression;
it does not fabricate quota availability.

The gateway writes sanitized operational state beside its authoritative config
at `<config-path>.model-runtime.json`, using atomic replacement and mode `0600`.
It creates a fresh run identity before listeners start, coalesces transition
writes and refreshes availability every 30 seconds. Graceful shutdown flushes a
stopped snapshot. Diagnostics storage failures do not interrupt inference;
missing/invalid state is unknown, an unreachable gateway is offline, and a PID
mismatch or heartbeat older than 90 seconds is stale. Historical records remain
readable while current runtime availability is marked unknown. Eligibility can
lag by one heartbeat; saved disable/authorization changes are checked directly
by the protected API and next outbound operation.

The artifact retains at most 64 records, 64 events per record and 64 candidates,
with bounded identifier lengths and a 2 MiB file limit. Older details are omitted
with an explicit marker. It contains no prompts, answers, tools, chat/session
identifiers, endpoint URLs, keys, token sets, callback codes or raw provider
errors. The dashboard reads typed state through protected
`GET /api/models/runtime` (`Cache-Control: no-store`); it never probes inference
and adds no routing diagnostics to the public health endpoint. The existing
Models page uses TanStack Query to refresh this view every 5 seconds.

Verification uses full requests with fake inference endpoints and authenticated
API fixtures, including inactivity, 503 cancellation, quota/probe recovery,
parent/subtask isolation, captured priorities and bounded/offline history. Run
`go test ./web/backend/api -run TestRuntime` and
`pnpm --dir web/frontend test:models`; no personal credentials are required.

The migrated legacy Codex adapter retains its existing model-name compatibility
mapping. Its runtime record identifies the configured target; the registered
ChatGPT route and explicit model/account tests preserve the selected slug.
