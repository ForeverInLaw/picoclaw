package telegram

import (
	"strconv"
	"strings"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/identity"
)

func (c *TelegramChannel) resolveParticipantAlias(user *telego.User) string {
	if c == nil || user == nil || c.config == nil {
		return ""
	}

	aliases := c.config.Channels.Telegram.ParticipantAliases
	if len(aliases) == 0 {
		return ""
	}

	return strings.TrimSpace(aliases[strconv.FormatInt(user.ID, 10)])
}

func (c *TelegramChannel) resolveParticipantLabel(user *telego.User) string {
	if user == nil {
		return "unknown"
	}

	if alias := c.resolveParticipantAlias(user); alias != "" {
		return alias
	}
	if username := strings.TrimSpace(user.Username); username != "" {
		return username
	}
	if firstName := strings.TrimSpace(user.FirstName); firstName != "" {
		return firstName
	}
	return strconv.FormatInt(user.ID, 10)
}

func telegramCanonicalID(user *telego.User) string {
	if user == nil {
		return ""
	}
	return identity.BuildCanonicalID("telegram", strconv.FormatInt(user.ID, 10))
}
