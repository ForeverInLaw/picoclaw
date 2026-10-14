# Scoped active context

Seahorse is the default active-context engine. Select `agents.defaults.context_manager: "legacy"` to use legacy context management explicitly. An agent in `agents.list` can override `context_manager` and `context_manager_config`.

```json
{
  "agents": {
    "defaults": {
      "context_manager": "seahorse",
      "context_manager_config": {
        "compact_timeout_seconds": 60,
        "max_compact_iterations": 20,
        "max_completion_attempts": 4,
        "max_concurrent_compactions": 2
      }
    }
  }
}
```

Omitted or zero bounds use these defaults. Negative bounds and unknown engines fail configuration validation. `PICOCLAW_AGENTS_DEFAULTS_CONTEXT_MANAGER` selects the default engine through the environment. Per-agent engine options replace the default options object; omitted individual bounds then use the engine defaults.

Each agent owns `<workspace>/sessions/seahorse-<agent-id>.sqlite`. Compaction uses its exact configured primary provider/model, temperature, thinking settings and output limit. Completion calls, iterations, concurrent compactions and elapsed time are bounded. The full prepared request, including system contributors, tools, media and output reserve, still passes the request budget check.

`short_grep` searches only the captured active conversation. `short_expand` recovers complete original messages from scoped message or summary IDs, including the source graph of condensed summaries. IDs include the owning agent, conversation and generation through their scope prefix. External conversation selectors, `all_conversations` and foreign IDs are rejected. Both tools are read only and permitted in inline conversations, where direct/group histories and workspace memory remain excluded. Historical tool calls are evidence and must not be replayed.

Text beside tool calls or media, roles, tool IDs, thought signatures, response phases, model names and source IDs survive conversion and reopening. Known source timestamps are retained; missing historical timestamps remain unknown. Messages without an external source ID receive a deterministic canonical source reference within their captured generation.

A reset cancels that conversation's operations before advancing its durable generation. Persistence and summary publication use the existing generation gate; provider calls occur outside the gate. Earlier source graphs stay on disk but cannot enter active assembly or retrieval after reset. Shutdown and reload cancel and join owned operations before closing SQLite and retired providers.

Explicit legacy selection uses the same worker admission, reset cancellation,
shutdown join and provider-retention boundary. Its existing summary/truncation
policy, three-attempt retry limit and 120-second timeout remain. Maintenance
captures the owning agent's exact provider, resolved model and output settings;
model switching retains captured clients until workers finish. Canceled work
neither retries nor publishes a late summary, and reset does not wait for an
unresponsive provider while holding the conversation generation gate.

Canonical transcripts continue through the existing session store. Active compaction does not truncate that transcript, relocate archive/index/embedding data or change Saved facts. New Seahorse turns remain available after restarting with explicit legacy selection.

The first use lazily imports the effective current generation's history and legacy summary, including summary-only conversations. It reads the session store's active view and never scans archived generation files. A legacy summary is labeled `kind="imported"`, with unknown source times; any retained source evidence is linked when available. Summary-only imports do not invent original messages.

An immutable import plan fixes source identities before writes begin. Each original message and its parts persist transactionally; imported summary publication, active projection reconciliation and the completion checkpoint commit together. Partial writes resume after reopen without duplicate sources or false completion. Matching completed snapshots require no source writes. Ordinary append checkpoints replace earlier append plans; plans at canonical rewrites remain as source-identity boundaries.

Returning from legacy reconciles retained messages and any new tail against those identities. Obsolete raw messages leave the active projection; original source rows and existing compaction DAGs survive. Known messages restored by the canonical store recover their original identities and order. Sources already represented by active summaries stay compressed. Captured generation and canonical snapshot checks reject stale background reconciliation. Read or migration failures stop the turn visibly before provider input is sent; they do not silently replace history with an empty fallback. A retained malformed legacy JSON source requires repair and restart so the existing JSON-to-JSONL migration can retry.

The standalone engine is adapted from upstream `bbf6893ca7afad27f1d00a0f5a45982a549c6ed6`; it is integrated through the existing agent loop and does not replace the provider or tool packages.
