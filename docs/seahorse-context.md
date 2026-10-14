# Scoped active context

Seahorse is selectable with `agents.defaults.context_manager: "seahorse"`; the default remains `legacy` until the active-session importer is accepted. An agent in `agents.list` can override `context_manager` and `context_manager_config`.

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

Canonical transcripts continue through the existing session store. Active compaction does not truncate that transcript, relocate archive/index data or change Saved facts. Existing transcript sources are synchronized idempotently; durable import completion, legacy-summary reconciliation and the default cutover are a separate migration increment. Selecting Seahorse is an operator choice until that increment is accepted. Failed compaction is visible and source evidence is retained.

The standalone engine is adapted from upstream `bbf6893ca7afad27f1d00a0f5a45982a549c6ed6`; it is integrated through the existing agent loop and does not replace the provider or tool packages.
