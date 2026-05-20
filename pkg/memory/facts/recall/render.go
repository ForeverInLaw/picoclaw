// Package recall provides synchronous fact recall used by channel handlers
// before invoking the agent.
package recall

import (
	"strings"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

// Render formats recall hits into a compact Russian system-prefix block.
// Returns an empty string when there are no hits.
func Render(hits []facts.RecallHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Известно:\n")
	for _, h := range hits {
		b.WriteString("- ")
		b.WriteString(h.Fact.Entity)
		b.WriteString(" ")
		b.WriteString(h.Fact.Attribute)
		b.WriteString(": ")
		b.WriteString(h.Fact.Value)
		b.WriteString("\n")
	}
	return b.String()
}
