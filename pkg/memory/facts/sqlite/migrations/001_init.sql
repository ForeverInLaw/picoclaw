CREATE TABLE facts (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  namespace       TEXT    NOT NULL,
  entity          TEXT    NOT NULL,
  attribute       TEXT    NOT NULL,
  value           TEXT    NOT NULL,
  confidence      REAL    NOT NULL DEFAULT 0.7,
  source_msg_ref  TEXT    NOT NULL,
  source_actor    TEXT,
  created_at      TIMESTAMP NOT NULL,
  updated_at      TIMESTAMP NOT NULL,
  last_seen_at    TIMESTAMP NOT NULL,
  access_count    INTEGER NOT NULL DEFAULT 0,
  ttl_seconds     INTEGER,
  deleted_at      TIMESTAMP,
  embedding       BLOB,
  embedding_norm  REAL
);

CREATE INDEX idx_facts_ns_entity_attr
  ON facts(namespace, entity, attribute)
  WHERE deleted_at IS NULL;

CREATE INDEX idx_facts_ns_decay
  ON facts(namespace, last_seen_at)
  WHERE deleted_at IS NULL;

CREATE TABLE extraction_log (
  session_key            TEXT PRIMARY KEY,
  last_processed_msg_idx INTEGER NOT NULL,
  last_run_at            TIMESTAMP NOT NULL
);

CREATE TABLE failed_extractions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  session_key TEXT NOT NULL,
  msg_range   TEXT NOT NULL,
  error       TEXT NOT NULL,
  failed_at   TIMESTAMP NOT NULL
);
