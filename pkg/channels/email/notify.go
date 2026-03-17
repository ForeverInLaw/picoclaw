package email

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
)

const maxNotificationSummaryLength = 220

func (c *EmailChannel) notifyInboundMessage(msg incomingMessage) error {
	if len(c.config.NotifyTelegramIDs) == 0 {
		return nil
	}

	content := buildTelegramNotification(msg)
	for _, chatID := range c.config.NotifyTelegramIDs {
		chatID = strings.TrimSpace(chatID)
		if chatID == "" {
			continue
		}
		if err := c.PublishOutbound(context.Background(), bus.OutboundMessage{
			Channel: "telegram",
			ChatID:  chatID,
			Content: content,
		}); err != nil {
			return err
		}
	}

	return nil
}

func buildTelegramNotification(msg incomingMessage) string {
	from := strings.TrimSpace(msg.ChatID)
	if strings.TrimSpace(msg.SenderName) != "" {
		from = fmt.Sprintf("%s <%s>", msg.SenderName, msg.ChatID)
	}

	subject := strings.TrimSpace(msg.Subject)
	if subject == "" {
		subject = "(без темы)"
	}

	return fmt.Sprintf(
		"📬 Новое письмо\n\nОт: %s\nТема: %s\nКратко: %s\nСтатус: %s",
		from,
		subject,
		extractNotificationSummary(msg.Content),
		classifyNotificationStatus(msg),
	)
}

func extractNotificationSummary(content string) string {
	content = strings.TrimSpace(content)
	if marker := "\n\nBody:\n"; strings.Contains(content, marker) {
		parts := strings.SplitN(content, marker, 2)
		content = strings.TrimSpace(parts[1])
	}

	content = strings.Join(strings.Fields(content), " ")
	if content == "" {
		return "Письмо без текста."
	}
	if len([]rune(content)) <= maxNotificationSummaryLength {
		return content
	}

	runes := []rune(content)
	return strings.TrimSpace(string(runes[:maxNotificationSummaryLength])) + "..."
}

func classifyNotificationStatus(msg incomingMessage) string {
	text := strings.ToLower(strings.Join([]string{msg.Subject, msg.Content}, "\n"))
	for _, keyword := range []string{
		"urgent", "asap", "security", "credential", "password", "login",
		"invoice", "payment", "wire", "bank", "contract", "legal", "quote",
		"pricing", "proposal", "partnership", "access", "domain", "hosting",
		"срочно", "договор", "оплата", "счет", "счёт", "доступ", "пароль",
		"ключ", "безопас", "юрид", "коммерч",
	} {
		if strings.Contains(text, keyword) {
			return "требует внимания"
		}
	}

	return "рутинное"
}
