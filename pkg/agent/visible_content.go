package agent

import (
	"regexp"
	"strings"
)

var (
	hiddenThinkingBlockPattern = regexp.MustCompile(`(?is)<(?:think|thought|thinking)\b[^>]*>.*?</(?:think|thought|thinking)\s*>`)
	hiddenThinkingOpenTail     = regexp.MustCompile(`(?is)<(?:think|thought|thinking)\b[^>]*>.*$`)
	hiddenThinkingTagPattern   = regexp.MustCompile(`(?is)</?(?:think|thought|thinking)\b[^>]*>`)
	excessBlankLinesPattern    = regexp.MustCompile(`\n{3,}`)
)

// sanitizeVisibleAssistantContent removes XML-like hidden thinking blocks that
// some providers leak into visible assistant content.
func sanitizeVisibleAssistantContent(content string) string {
	if content == "" {
		return ""
	}

	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	content = hiddenThinkingBlockPattern.ReplaceAllString(content, "")
	content = hiddenThinkingOpenTail.ReplaceAllString(content, "")
	content = hiddenThinkingTagPattern.ReplaceAllString(content, "")
	content = excessBlankLinesPattern.ReplaceAllString(content, "\n\n")

	return strings.TrimSpace(content)
}
