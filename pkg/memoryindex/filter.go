package memoryindex

import "strings"

// ShouldIndexObservation filters empty or internal-only messages before they
// enter the retrieval index.
func ShouldIndexObservation(obs Observation) bool {
	content := strings.TrimSpace(obs.Content)
	if content == "" {
		return false
	}

	switch obs.Role {
	case "user", "assistant":
	default:
		return false
	}

	channel := strings.ToLower(strings.TrimSpace(obs.Channel))
	sessionKey := strings.ToLower(strings.TrimSpace(obs.SessionKey))
	senderID := strings.ToLower(strings.TrimSpace(obs.SenderID))

	if channel == "heartbeat" || senderID == "heartbeat" || senderID == "cron" {
		return false
	}
	if sessionKey == "heartbeat" || strings.Contains(sessionKey, ":heartbeat") {
		return false
	}
	if content == "HEARTBEAT_OK" {
		return false
	}
	if strings.HasPrefix(content, "[scheduled_reminder_trigger]") {
		return false
	}

	return true
}
