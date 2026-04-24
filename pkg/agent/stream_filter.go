package agent

import "strings"

type visibleStreamFilter struct {
	lastVisible string
}

func (f *visibleStreamFilter) Update(accumulated string) (string, bool) {
	visible := sanitizeVisibleAssistantContent(accumulated)
	if visible == "" || visible == f.lastVisible {
		return "", false
	}
	if isWaitingForHiddenThinkingClose(accumulated, visible) {
		return "", false
	}
	f.lastVisible = visible
	return visible, true
}

func (f *visibleStreamFilter) Final(accumulated string) string {
	visible := sanitizeVisibleAssistantContent(accumulated)
	f.lastVisible = visible
	return visible
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
