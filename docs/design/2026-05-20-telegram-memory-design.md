# Telegram Facts Memory — Design Spec

**Date:** 2026-05-20
**Status:** Approved (design phase complete)
**Scope:** PicoClaw Telegram channel
**Branch:** `feat/telegram-memory` (forks from `merge/origin-main-into-dev` @ `9eac69b`)

## 1. Motivation

PicoClaw's existing `pkg/memory` provides per-session conversation history (append-only JSONL + summary). It has no mechanism for:

- Extracting atomic facts about users and chats
- Recalling past facts semantically when they become relevant
- Reconciling contradictions ("Андрей любит мохито" → later "Андрей мохито не любит")
- Forgetting stale or low-confidence information

The Telegram bot "Коробъка" needs cross-session knowledge of its participants and chats. Without it, the bot starts every conversation from zero.

This spec adds a Go-native facts memory subsystem inspired by mem0, integrated into PicoClaw without external services. Initial scope is the Telegram channel only; the design is structured so other channels can adopt it later without changes to the core.

## 2. Goals and non-goals

### Goals
- Atomic-fact storage with semantic recall (kNN over embeddings)
- Update-vs-add semantics on contradiction
- Confidence-based decay
- Single Go binary, no external services on the deploy host
- Fits PicoClaw's edge ethos: runs comfortably on Raspberry Pi 4 / 7.6 GB RAM, ~3.6 GB free disk

### Non-goals (v1)
- Cross-channel sharing (Telegram facts do not leak to Pico/Discord/etc.)
- UI for browsing or editing facts (CLI command may be added in a follow-up PR)
- Local embedding model (we use provider `/embeddings` via existing routing)
- Multi-tenant isolation beyond namespace strings

## 3. Architecture

### 3.1 Components

| Component | Package | Responsibility |
|-----------|---------|----------------|
| `FactStore` interface | `pkg/memory/facts` | CRUD, kNN, namespace queries |
| `SQLiteFactStore` | `pkg/memory/facts/sqlite` | Pure-Go SQLite (`modernc.org/sqlite`) backend; embeddings as BLOB; brute-force kNN in Go scoped by namespace |
| `Extractor` | `pkg/memory/facts/extract` | Async LLM pipeline turning message windows into facts |
| `Embedder` | `pkg/memory/facts/embed` | Wraps provider routing, calls `/embeddings` |
| `Recaller` | `pkg/memory/facts/recall` | Pre-turn: embed input, kNN, return top-K facts |
| `Consolidator` | `pkg/memory/facts/consolidate` | Periodic dedupe, contradiction resolution, decay |
| Telegram hook | `pkg/channels/telegram` | Pre-agent recall injection + post-agent extraction enqueue |
| Config | `pkg/config` | New `agents.memory.facts` block |

### 3.2 Boundaries

The conversation history `Store` (existing `pkg/memory`) is untouched. The new `FactStore` is independent and parallel. Code that produces messages keeps using `Store`; new code that needs entity-level recall uses `FactStore`. Both can coexist within an agent turn.

The Extractor consumes raw messages from `Store.GetHistory()` to derive facts but does not write back to it.

## 4. Data model

### 4.1 `facts` table

```sql
CREATE TABLE facts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  namespace       TEXT    NOT NULL,                -- "tg:chat:-100…", "tg:user:807…", "tg:bot:c0md"
  entity          TEXT    NOT NULL,                -- "Андрей", "this chat"
  attribute       TEXT    NOT NULL,                -- "likes", "lives_in"
  value           TEXT    NOT NULL,
  confidence      REAL    NOT NULL DEFAULT 0.7,    -- 0..1
  source_msg_ref  TEXT    NOT NULL,                -- channel msg id for provenance
  source_actor    TEXT,                            -- which user asserted it (NULL = bot inferred)
  created_at      TIMESTAMP NOT NULL,
  updated_at      TIMESTAMP NOT NULL,
  last_seen_at    TIMESTAMP NOT NULL,
  access_count    INTEGER NOT NULL DEFAULT 0,
  ttl_seconds     INTEGER,                         -- NULL = config default
  deleted_at      TIMESTAMP                        -- soft delete
);

CREATE INDEX idx_facts_ns_entity_attr
  ON facts(namespace, entity, attribute)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_facts_ns_decay
  ON facts(namespace, last_seen_at)
  WHERE deleted_at IS NULL;
```

### 4.2 Embeddings (BLOB column on `facts`)

PicoClaw builds with `CGO_ENABLED=0`, which rules out `sqlite-vec` (a CGo loadable extension). Instead the embedding lives directly on `facts`:

```sql
ALTER TABLE facts ADD COLUMN embedding BLOB;   -- float32 little-endian, length = embedding_dim * 4
ALTER TABLE facts ADD COLUMN embedding_norm REAL;  -- pre-computed L2 norm for cosine
```

kNN at recall time is a brute-force scan in Go, scoped by `namespace` (and `deleted_at IS NULL`). For the bot's scale (per-namespace fact counts in the hundreds, rarely thousands), brute force is sub-millisecond on the rpi3. The driver is `modernc.org/sqlite` — pure Go, no CGo, fits the existing build flags.

The embedding key is the canonical text form: `"{entity} {attribute} {value}"` lowercased and trimmed. The embedding is recomputed when `value` changes.

### 4.3 `facts_fts` (FTS5 fallback)

```sql
CREATE VIRTUAL TABLE facts_fts USING fts5(
  entity, attribute, value,
  content='facts', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);
```

Synced via triggers on `facts` insert/update/delete. Used as recall fallback when embedding lookup fails.

### 4.4 `extraction_log`

```sql
CREATE TABLE extraction_log (
  session_key            TEXT PRIMARY KEY,
  last_processed_msg_idx INTEGER NOT NULL,
  last_run_at            TIMESTAMP NOT NULL
);
```

Guarantees we never extract the same message window twice across restarts.

### 4.5 `failed_extractions`

```sql
CREATE TABLE failed_extractions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_key TEXT NOT NULL,
  msg_range   TEXT NOT NULL,            -- "[start_idx, end_idx)"
  error       TEXT NOT NULL,
  failed_at   TIMESTAMP NOT NULL
);
```

Dead-letter for extraction jobs that exhausted their retries.

## 5. Data flow

### 5.1 Recall (synchronous, in agent turn)

1. Telegram handler receives a message in a chat. It builds the namespace set `["tg:chat:<id>", "tg:user:<id>", "tg:bot:<botname>"]`.
2. The handler calls `Recaller.Recall(ctx, namespaces, input)`:
   - Embed `input` via `Embedder`.
   - SELECT facts where `namespace IN (...)` AND `deleted_at IS NULL` AND `embedding IS NOT NULL`.
   - In Go, compute cosine similarity against each row's BLOB embedding (using the pre-computed `embedding_norm`).
   - Drop results with cosine score below `recall_min_score`.
   - Take top `top_k`.
   - Bump `access_count` and `last_seen_at` for each returned fact.
3. The handler renders facts into a compact system prefix (Russian, since the bot's locale is Russian):
   ```
   Известно:
   - Андрей любит грейпфрутовый сок
   - В этом чате принято обращаться на "ты"
   ```
4. The agent runs as usual. The recall step adds at most one embedding call and one SQLite query.

### 5.2 Extraction (asynchronous, post-turn)

1. After the agent replies, the Telegram handler enqueues an extraction job with `(session_key, message_range)` into an in-process buffered channel.
2. A single worker goroutine drains the channel. Per job:
   - Read `extraction_log.last_processed_msg_idx` for the session. Skip if already covered.
   - Fetch the message window from `Store.GetHistory()`.
   - Call the extraction LLM with a strict JSON-schema prompt asking for an array of `{entity, attribute, value, confidence}`.
   - For each parsed fact:
     - Embed the canonical form.
     - Search `facts` for `(namespace, entity, attribute)` matches with `deleted_at IS NULL`.
     - If a near-duplicate exists (cosine ≥ 0.9 with stored embedding): `UPDATE` — bump `confidence` by `+0.05` (capped at 1.0), refresh `last_seen_at`.
     - If a same-key fact with different value exists: contradiction. Soft-delete the old, insert new, log the resolution.
     - Otherwise: insert new.
   - Update `extraction_log`.
3. Failures: retry the LLM call up to 3 times with exponential backoff (1s, 3s, 9s). On final failure the job is recorded in `failed_extractions`; the worker continues with the next job. The bot's user-facing turn is not affected — extraction is fully async.

### 5.3 Consolidation (cron, daily)

A daily `cron.Service` job runs `Consolidator.Run()`:
- Walk facts with `last_seen_at < now - decay_window_days`.
- Halve `confidence`.
- Soft-delete facts whose `confidence < min_confidence`.
- Optionally re-embed facts older than 30 days if the embedding model version has changed (config flag).

## 6. Namespacing

| Namespace | Purpose |
|-----------|---------|
| `tg:chat:<chat_id>` | Properties of the chat as a whole (language, tone, rules) |
| `tg:user:<user_id>` | Properties of an individual user (preferences, identity, history) |
| `tg:bot:<bot_username>` | Properties of the bot persona itself (e.g., self-naming, self-style) |

Cross-references: if user A says "Андрей живёт в Минске" about user B, the fact is written to user B's namespace with `source_actor=<A's user_id>`. Provenance is preserved without leaking facts between users.

## 7. Configuration

Added to `config.json` under `agents.memory.facts`:

```yaml
enabled: true
channel_scope: ["telegram"]
extraction_model: "<slug from model_list>"
embedding_model: "<slug from model_list>"
embedding_dim: 1536                      # must match the embedding model
top_k: 8
recall_min_score: 0.55
extraction_window_msgs: 20
extraction_idle_seconds: 30              # how long to wait after last msg before extracting
ttl_default_days: 90
decay_window_days: 30
min_confidence: 0.2
sqlite_path: "/home/andy/.picoclaw/facts.db"
```

A missing or `enabled: false` block disables the subsystem entirely; agent behavior is identical to today.

## 8. Telegram integration points

1. `pkg/channels/telegram/handler.go` (or equivalent) — call `Recaller.Recall(...)` before constructing the agent request, append the resulting system prefix.
2. Same handler — after the agent emits its final reply, enqueue an extraction job.
3. Tests under `pkg/channels/telegram/*_test.go` use a `FakeFactStore` to assert injection and enqueueing without hitting SQLite.

## 9. Error handling

- LLM extraction failure: retry 3× with exponential backoff. Final failure goes to `failed_extractions`. Bot keeps replying normally.
- Embedding failure during recall: degrade to FTS5 keyword recall. Logged at warning level; the agent turn never blocks on memory.
- SQLite write contention: a single writer goroutine fed by a buffered channel. Reads are concurrent (WAL mode).
- Schema migrations: up-only, numbered files under `pkg/memory/facts/migrate`. Run on startup before serving requests; failure to migrate aborts boot.
- Configuration validation: at startup, ensure `embedding_dim` matches the model declared in `model_list`. Mismatch aborts boot with a clear error message rather than silently producing garbage vectors.

## 10. Testing strategy

- **Unit tests** per component:
  - `Extractor` with a mocked LLM returning canned JSON, including malformed-JSON paths.
  - `Recaller` against an in-memory `FactStore` to assert ordering, threshold filtering, namespace scoping.
  - `Consolidator` with fixtures exercising decay, dedupe, and contradiction resolution.
  - `SQLiteFactStore` exercised through the public interface with a temporary file.
- **Integration test**: end-to-end with a temporary SQLite, a stub embedder, and a fake LLM. Walks a scripted Telegram dialogue and asserts facts table state at each checkpoint.
- **Channel hook test**: in `pkg/channels/telegram` tests, confirm pre-agent recall injection and post-agent extraction enqueue happen on the right events.
- **Property test**: feeding the same message N times produces ≤ 1 fact (dedupe holds).
- **Smoke test against rpi3**: after deploy, send a known message to the bot, observe extraction in logs, send a recall-triggering message in a later session, assert the recalled fact appears in the bot's reply.

## 11. Risks and open questions

- **Disk pressure on rpi3.** `/` currently at 87% full (3.6 GB free). The facts DB will grow slowly (atomic facts are small) but embeddings dominate (`embedding_dim × 4 bytes` per fact, ~6 KB at dim=1536). At 100k facts that is ~600 MB. Mitigation: `sqlite_path` is configurable; can be moved to an attached volume if pressure becomes real.
- **Russian-language extraction quality.** The extraction prompt must explicitly instruct the LLM to extract in the input's language. Smoke testing on rpi3 will validate.
- **Embedding model availability.** The existing proxy may or may not expose `/embeddings`. If not, this needs a separate provider entry in `model_list`. A pre-flight check at startup will catch this and abort cleanly.
- **Coupling to existing model routing.** Embeddings go through the same `model_list` slug → resolved model mechanism that summaries do. If the summary routing fixes (`1c498f7`, `4172ccb`) regress, embeddings break similarly. Tests must cover slug-resolution for the embedding model.

## 12. Migration / rollback

- v1 launches behind `agents.memory.facts.enabled` flag, defaulting to `false`.
- Rollback = flip the flag to `false` and restart. Tables persist; nothing else is affected.
- Schema migrations are up-only. Rolling back to an older binary that lacks new columns is supported because newer migrations only add columns / tables, never drop or rename.

## 13. Out of scope (v1)

- Cross-channel fact sharing (telegram ↔ pico ↔ discord)
- UI/CLI for fact browsing or editing
- Multi-bot isolation beyond namespace strings
- Local embedding model (ONNX / GGUF)
- Web-based admin panel
- Streaming fact updates mid-turn

## 14. Implementation order

1. SQLite schema + migrations + `FactStore` interface and SQLite implementation
2. `Embedder` wrapping provider routing
3. `Extractor` async worker with `extraction_log` idempotency
4. `Recaller` with embedding kNN and FTS5 fallback
5. `Consolidator` cron job
6. Telegram channel integration (recall inject + extraction enqueue)
7. Config wiring + startup pre-flight validation
8. Tests at each layer
9. End-to-end smoke on rpi3

Each step lands as its own commit. Each commit must build and pass tests in isolation.

## 15. Deploy notes — 2026-05-20

First deploy of the subsystem to rpi3 (`172.30.0.3`). Binary built locally on Windows
(`GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags "goolm,stdjson" -ldflags "-s -w"`),
installed via `sudo install -m 755` to `/usr/local/bin/picoclaw`. Restart via the
launcher API at `:18800/api/gateway/restart`.

The launcher config (`/home/andy/.picoclaw/config.json`) gained:

```json
"agents": {
  "memory": {
    "facts": {
      "enabled": true,
      "channel_scope": ["telegram"],
      "extraction_model": "gemma-4-31b-it gouter",
      "embedding_model": "disabled",
      "embedding_dim": 8,
      "top_k": 8,
      "recall_min_score": 0.0,
      "extraction_window_msgs": 20,
      "extraction_idle_seconds": 30,
      "ttl_default_days": 90,
      "decay_window_days": 30,
      "min_confidence": 0.2,
      "sqlite_path": "/home/andy/.picoclaw/facts.db"
    }
  }
}
```

`embedding_model: "disabled"` is a sentinel — v1 wires a `NullProvider`, so embeddings
always fail and the subsystem degrades to FTS5 keyword recall plus string-equality
dedup. When a real embeddings backend is wired in, this entry becomes a real model slug.

### v1 verification on rpi3

- Restart returned `{"pid":2833892,"status":"ok"}`, health uptime 5.9s.
- `/home/andy/.picoclaw/facts.db` materialised at 4096 bytes — bootstrap ran the
  migrations successfully.

### Smoke checklist (manual, in Telegram)

- [ ] Send "Запомни: я люблю грейпфрутовый сок" to Коробъка in a private chat.
- [ ] Wait ~60 seconds (extraction worker idle window).
- [ ] On rpi3: `sqlite3 /home/andy/.picoclaw/facts.db "SELECT namespace, entity, attribute, value, confidence FROM facts WHERE deleted_at IS NULL"` — expect a row mentioning "грейпфрутовый сок".
- [ ] In a fresh session (or after `/clear`), ask: "что я люблю?"
- [ ] Verify the bot's reply mentions грейпфрутовый сок.

Smoke results to be filled in once the manual run completes.

