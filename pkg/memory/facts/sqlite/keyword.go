package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"unicode"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// buildFTSQuery turns a free-form text into an FTS5 MATCH query whose tokens
// each end in `*` so they match prefixes. Anything that isn't a letter,
// digit, or space is replaced with a space — this strips FTS5 operators
// (`"`, `*`, `(`, `)`, `:`) as well as punctuation like `?` and `!` that
// also confuse the FTS5 query parser.
func buildFTSQuery(in string) string {
	cleaned := make([]rune, 0, len(in))
	for _, r := range in {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '\t' || r == '\n' {
			cleaned = append(cleaned, r)
		} else {
			cleaned = append(cleaned, ' ')
		}
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
