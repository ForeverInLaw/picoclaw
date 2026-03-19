package telegram

import (
	"strings"
	"unicode"

	"github.com/mymmrac/telego"
)

func (c *TelegramChannel) isBotNameTriggered(message *telego.Message) bool {
	if c == nil || c.config == nil || message == nil {
		return false
	}
	text, _ := telegramEntityTextAndList(message)
	return telegramTextHasNameTrigger(text, c.config.Channels.Telegram.NameTriggers)
}

func telegramTextHasNameTrigger(text string, triggers []string) bool {
	if strings.TrimSpace(text) == "" || len(triggers) == 0 {
		return false
	}

	normalizedTriggers := normalizeNameTriggers(triggers)
	if len(normalizedTriggers) == 0 {
		return false
	}

	for _, token := range tokenizeNameTriggerText(text) {
		for _, trigger := range normalizedTriggers {
			if token == trigger || strings.HasPrefix(token, trigger) {
				return true
			}
		}
	}
	return false
}

func normalizeNameTriggers(triggers []string) []string {
	out := make([]string, 0, len(triggers))
	seen := make(map[string]struct{}, len(triggers))
	for _, trigger := range triggers {
		trigger = strings.ToLower(strings.TrimSpace(trigger))
		trigger = strings.TrimPrefix(trigger, "@")
		if trigger == "" {
			continue
		}
		if _, ok := seen[trigger]; ok {
			continue
		}
		seen[trigger] = struct{}{}
		out = append(out, trigger)
	}
	return out
}

func tokenizeNameTriggerText(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}
