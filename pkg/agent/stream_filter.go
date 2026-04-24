package agent

import "strings"

type visibleStreamFilter struct {
	raw         string
	lastInput   string
	lastVisible string
}

func (f *visibleStreamFilter) Update(input string) (string, bool) {
	raw := f.accumulate(input)
	visible := sanitizeVisibleAssistantContent(raw)
	if visible == "" || visible == f.lastVisible {
		return "", false
	}
	if isWaitingForHiddenThinkingClose(raw, visible) {
		return "", false
	}
	f.lastVisible = visible
	return visible, true
}

func (f *visibleStreamFilter) Final(input string) string {
	raw := input
	if raw == "" {
		raw = f.raw
	}
	visible := sanitizeVisibleAssistantContent(raw)
	f.raw = raw
	f.lastInput = input
	f.lastVisible = visible
	return visible
}

func (f *visibleStreamFilter) accumulate(input string) string {
	if input == "" {
		return f.raw
	}
	if f.raw == "" || strings.HasPrefix(input, f.raw) {
		f.raw = input
	} else if isWaitingForHiddenThinkingClose(f.raw, "") {
		f.raw += input
	} else if len(input) >= len(f.lastInput) {
		f.raw = input
	} else {
		f.raw += input
	}
	f.lastInput = input
	return f.raw
}

func isWaitingForHiddenThinkingClose(raw, visible string) bool {
	trimmedRaw := strings.TrimSpace(raw)
	if trimmedRaw == "" || visible != "" {
		return false
	}
	open := hiddenThinkingOpenTagPattern.FindStringIndex(trimmedRaw)
	if open == nil || strings.TrimSpace(trimmedRaw[:open[0]]) != "" {
		return false
	}
	return !hiddenThinkingClosedPrefixPattern.MatchString(trimmedRaw)
}
