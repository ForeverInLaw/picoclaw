package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// ftsSpecial is the set of characters that have meaning in FTS5 query
// syntax. We strip them from user-provided text before assembling a query.
const ftsSpecial = `"*():`

// buildFTSQuery turns a free-form text into an FTS5 MATCH query whose tokens
// each end in `*` so they match prefixes. Empty input yields "".
func buildFTSQuery(in string) string {
	cleaned := make([]byte, 0, len(in))
	for i := 0; i < len(in); i++ {
		c := in[i]
		if strings.IndexByte(ftsSpecial, c) >= 0 {
			cleaned = append(cleaned, ' ')
			continue
		}
		cleaned = append(cleaned, c)
	}
	toks := strings.Fields(string(cleaned))
	if len(toks) == 0 {
		return ""
	}
	for i, t := range toks {
		toks[i] = t + "*"
	}
	return strings.Join(toks, " ")
}

func (s *Store) KeywordSearch(ctx context.Context, namespaces []string, q string, k int) ([]facts.RecallHit, error) {
	if k <= 0 || len(namespaces) == 0 {
		return nil, nil
	}
	ftsQ := buildFTSQuery(q)
	if ftsQ == "" {
		return nil, nil
	}

	query, nsArgs := inClause(`
		SELECT f.id, f.namespace, f.entity, f.attribute, f.value, f.confidence,
		       f.source_msg_ref, COALESCE(f.source_actor, ''),
		       f.created_at, f.updated_at, f.last_seen_at,
		       f.access_count, COALESCE(f.ttl_seconds, 0),
		       f.deleted_at, f.embedding, COALESCE(f.embedding_norm, 0),
		       bm25(facts_fts) AS rank
		FROM facts_fts
		JOIN facts f ON f.id = facts_fts.rowid
		WHERE facts_fts MATCH ?
		  AND f.deleted_at IS NULL
		  AND f.namespace IN (`,
		namespaces)
	args := make([]any, 0, len(nsArgs)+2)
	args = append(args, ftsQ)
	args = append(args, nsArgs...)
	args = append(args, k)

	rows, err := s.db.QueryContext(ctx, query+`) ORDER BY rank LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []facts.RecallHit
	for rows.Next() {
		var (
			f         facts.Fact
			deletedAt sql.NullTime
			embedding []byte
			rank      float64
		)
		if err := rows.Scan(
			&f.ID, &f.Namespace, &f.Entity, &f.Attribute, &f.Value, &f.Confidence,
			&f.SourceMsgRef, &f.SourceActor,
			&f.CreatedAt, &f.UpdatedAt, &f.LastSeenAt,
			&f.AccessCount, &f.TTLSeconds,
			&deletedAt, &embedding, &f.EmbeddingNorm,
			&rank,
		); err != nil {
			return nil, err
		}
		if deletedAt.Valid {
			f.DeletedAt = deletedAt.Time
		}
		if len(embedding) > 0 {
			f.Embedding = bytesToFloats(embedding)
		}
		// bm25 returns negative numbers; flip sign so larger means more relevant.
		out = append(out, facts.RecallHit{Fact: f, Score: -rank})
	}
	return out, rows.Err()
}
