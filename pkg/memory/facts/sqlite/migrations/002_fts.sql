CREATE VIRTUAL TABLE facts_fts USING fts5(
  entity, attribute, value,
  content='facts', content_rowid='id',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER facts_fts_ai AFTER INSERT ON facts BEGIN
  INSERT INTO facts_fts(rowid, entity, attribute, value)
  VALUES (new.id, new.entity, new.attribute, new.value);
END;

CREATE TRIGGER facts_fts_ad AFTER DELETE ON facts BEGIN
  INSERT INTO facts_fts(facts_fts, rowid, entity, attribute, value)
  VALUES('delete', old.id, old.entity, old.attribute, old.value);
END;

CREATE TRIGGER facts_fts_au AFTER UPDATE ON facts BEGIN
  INSERT INTO facts_fts(facts_fts, rowid, entity, attribute, value)
  VALUES('delete', old.id, old.entity, old.attribute, old.value);
  INSERT INTO facts_fts(rowid, entity, attribute, value)
  VALUES (new.id, new.entity, new.attribute, new.value);
END;
