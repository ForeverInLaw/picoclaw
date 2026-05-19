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
// It tolerates surrounding ```json fences but otherwise requires strict JSON.
func ParseFacts(raw string) ([]ExtractedFact, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	var env extractEnvelope
	if err := json.Unmarshal([]byte(s), &env); err != nil {
		return nil, fmt.Errorf("extract: parse: %w", err)
	}
	return env.Facts, nil
}
