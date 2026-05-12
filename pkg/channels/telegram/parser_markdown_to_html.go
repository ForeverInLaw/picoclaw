package telegram

import (
	"fmt"
	"strings"
)

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	blockquotes := extractBlockquotes(text)
	text = blockquotes.text

	links := extractLinks(text)
	text = links.text

	text = reHeading.ReplaceAllString(text, "$1")

	text = escapeHTML(text)

	text = reBoldStar.ReplaceAllString(text, "<b>$1</b>")

	text = reBoldUnder.ReplaceAllString(text, "<b>$1</b>")

	text = reItalic.ReplaceAllStringFunc(text, func(s string) string {
		match := reItalic.FindStringSubmatch(s)
		if len(match) < 2 {
			return s
		}
		return "<i>" + match[1] + "</i>"
	})

	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	text = reListItem.ReplaceAllString(text, "• ")

	for i, link := range links.links {
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00LN%d\x00", i),
			fmt.Sprintf(`<a href="%s">%s</a>`, link.url, escapeHTML(link.label)),
		)
	}

	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00IC%d\x00", i), fmt.Sprintf("<code>%s</code>", escaped))
	}

	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00CB%d\x00", i),
			fmt.Sprintf("<pre><code>%s</code></pre>", escaped),
		)
	}

	for i, quote := range blockquotes.quotes {
		escaped := escapeHTML(quote)
		escaped = strings.ReplaceAll(escaped, "\n", "<br>")
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00BQ%d\x00", i),
			fmt.Sprintf("<blockquote>%s</blockquote>", escaped),
		)
	}

	return text
}

type linkMatch struct {
	text  string
	links []extractedLink
}

type extractedLink struct {
	label string
	url   string
}

func extractLinks(text string) linkMatch {
	matches := reLink.FindAllStringSubmatch(text, -1)

	links := make([]extractedLink, 0, len(matches))
	for _, m := range matches {
		links = append(links, extractedLink{label: m[1], url: m[2]})
	}

	i := 0
	text = reLink.ReplaceAllStringFunc(text, func(_ string) string {
		placeholder := fmt.Sprintf("\x00LN%d\x00", i)
		i++
		return placeholder
	})

	return linkMatch{text: text, links: links}
}

type codeBlockMatch struct {
	text  string
	codes []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	matches := reCodeBlock.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reCodeBlock.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00CB%d\x00", i)
		i++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	matches := reInlineCode.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reInlineCode.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00IC%d\x00", i)
		i++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}

type blockquoteMatch struct {
	text   string
	quotes []string
}

func extractBlockquotes(text string) blockquoteMatch {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	quotes := make([]string, 0)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, ">") {
			continue
		}
		content := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		placeholder := fmt.Sprintf("\x00BQ%d\x00", len(quotes))
		quotes = append(quotes, content)
		lines[i] = placeholder
	}

	return blockquoteMatch{
		text:   strings.Join(lines, "\n"),
		quotes: quotes,
	}
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}
