package utils

import "strings"

// QuoteBlock renders plain text as a simple line-by-line quote block.
func QuoteBlock(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			lines[i] = ">"
			continue
		}
		lines[i] = "> " + trimmed
	}
	return strings.Join(lines, "\n")
}

// FormatQuotedMessage places a quoted source block above a body.
func FormatQuotedMessage(quotedSource, body string) string {
	quote := QuoteBlock(quotedSource)
	body = strings.TrimSpace(body)

	switch {
	case quote == "":
		return body
	case body == "":
		return quote
	default:
		return quote + "\n\n" + body
	}
}
