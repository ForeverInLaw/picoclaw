// Package extract turns conversation windows into atomic facts via an
// asynchronous LLM-backed pipeline.
package extract

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ExtractedFact is the wire shape returned by the extraction LLM.
type ExtractedFact struct {
	Entity     string  `json:"entity"`
	Attribute  string  `json:"attribute"`
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

type extractEnvelope struct {
	Facts []ExtractedFact `json:"facts"`
}

// ParseFacts extracts the structured facts array from an LLM response.
// It tolerates surrounding ```json fences, leading <think>...</think> blocks,
// HTML, and arbitrary preamble/postamble. The first balanced top-level JSON
// object containing a "facts" key is parsed.
func ParseFacts(raw string) ([]ExtractedFact, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// Strip a leading <think>...</think> block if present (common in
	// reasoning-mode models that prefix their reply with thoughts).
	if idx := strings.Index(s, "</think>"); idx >= 0 && strings.HasPrefix(s, "<think>") {
		s = strings.TrimSpace(s[idx+len("</think>"):])
	}

	// Fast path: try the whole string.
	var env extractEnvelope
	if err := json.Unmarshal([]byte(s), &env); err == nil {
		return env.Facts, nil
	}

	// Slow path: find the first '{' and the matching closing brace.
	if obj := firstJSONObject(s); obj != "" {
		if err := json.Unmarshal([]byte(obj), &env); err == nil {
			return env.Facts, nil
		}
	}

	snippet := s
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	return nil, fmt.Errorf("extract: parse: no JSON object found; raw=%q", snippet)
}

// firstJSONObject returns the first balanced { ... } block in s, or "" when
// the braces never balance. Quoted strings and escape sequences are honored.
func firstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if esc {
			esc = false
			continue
		}
		if inStr {
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}
