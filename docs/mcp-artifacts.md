# Durable MCP text artifacts

Agent MCP tools return short text normally. Results exceeding the configured
Unicode character threshold are saved as raw artifact records; the model gets a
bounded preview and an `artifact://mcp/<id>` reference. MCP transport and server
calls are unchanged, and each external operation runs once.

```json
{
  "tools": {
    "mcp": {
      "artifacts": {
        "directory": "/srv/picoclaw/artifacts/mcp",
        "max_inline_runes": 16384,
        "preview_runes": 1000,
        "max_disk_bytes": 0
      }
    }
  }
}
```

Defaults are `$PICOCLAW_HOME/artifacts/mcp` (or `~/.picoclaw/artifacts/mcp`),
16,384 inline characters, a 1,000-character preview and no quota. Missing or
nonpositive character limits use those defaults; previews are additionally
capped at 4,096 characters and the inline threshold. Environment overrides are
`PICOCLAW_TOOLS_MCP_ARTIFACTS_DIRECTORY`,
`PICOCLAW_TOOLS_MCP_ARTIFACTS_MAX_INLINE_RUNES`,
`PICOCLAW_TOOLS_MCP_ARTIFACTS_PREVIEW_RUNES`, and
`PICOCLAW_TOOLS_MCP_ARTIFACTS_MAX_DISK_BYTES`.

Use a dedicated absolute directory outside the ordinary workspace. In the
Docker images the default is `/data/artifacts/mcp`; persist the writable `/data`
state volume. New directories use mode 0700 and atomic records use 0600.
Records contain versioned agent/conversation ownership and full raw text.
The existing durable atomic writer anchors its operations through `os.Root`.

Read details through the existing enabled `read_file` tool:

```json
{"path":"artifact://mcp/0123456789abcdef0123456789abcdef","offset":0,"length":65536}
```

The reference authorizes only its captured agent and exact base conversation
SessionKey. Tool arguments cannot override this identity. References remain
usable after restart and `/clear`; generation fencing of active context does
not delete or revoke explicitly requested artifacts. Independent conversations,
agents and named channels cannot reuse them. Inline tools retain their existing
allowlist; inline or history-free requests without an owning conversation cannot
read artifacts. SubTurns retain their owning base conversation and actual worker
agent identity, even though their ephemeral session IDs change.

Physical record paths and symlink aliases are denied by `read_file`, including
otherwise allowed/outside-workspace paths. Artifact records are opened through
the owner root; replaced symlink records are rejected. Keep normal filesystem,
Shell and OS permissions appropriate for your tools: references do not grant
general access to the storage directory.

Known configured secrets, including decoded model API keys and short key values,
are filtered from the **whole source** before preview truncation or pagination.
This artifact boundary always redacts them, independently of general tool
filtering switches and minimum lengths. Raw records remain intact for explicit
operator access. `read_file` offsets, total lengths and its existing per-read
limit apply to the filtered view, so partial reads cannot reveal fragments of a
credential. Ordinary file reads retain their existing policy and behavior.
MCP private context does not become public tool activity; explicit public and
terminal tool results retain their separate semantics.

`max_disk_bytes: 0` disables quota. A positive value limits the total logical
bytes of regular files in the dedicated flat record directory, including JSON
ownership metadata and any retained temporary files; it is not an OS filesystem
reservation or a count of allocated blocks. Quota writes use a shared,
nonblocking OS file lock across gateway/CLI instances. Busy, unsupported-lock,
quota and write errors report that the MCP operation **already completed** and
stop the turn with a public notice. The manager, model recovery and remaining
requested tools do not replay that operation. Fix storage or quota before new
independent calls. Quota locking supports Linux/macOS/BSD and Windows; unsupported
platforms fail explicitly when a quota is enabled.

Artifacts have no TTL and no automatic cleanup. To remove one record, stop all
gateway/agent processes using the directory, then explicitly delete the JSON
file whose basename matches its reference ID. For example, with the configured
directory above:

```sh
# After stopping every process that can write this store:
rm -- /srv/picoclaw/artifacts/mcp/0123456789abcdef0123456789abcdef.json
```

To clear the entire store, stop its writers and explicitly remove its contents,
including metadata/temporary files and `.lock`. Do not remove or replace `.lock`
while processes are running. Cleanup is an operator decision; removed references
return an unavailable error. `/clear`, reload and shutdown never perform cleanup.
