package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/memoryindex"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func recordMemoryObservation(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey, channel, chatID, role, senderID, content string,
) {
	if agent == nil || agent.MemoryIndex == nil || strings.TrimSpace(content) == "" {
		return
	}

	_ = agent.MemoryIndex.AddObservation(ctx, memoryindex.Observation{
		SessionKey: sessionKey,
		Channel:    channel,
		ChatID:     chatID,
		Role:       role,
		SenderID:   senderID,
		Content:    content,
	})
}

func recordDeliveredAssistantMessages(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey, sourceChannel, sourceChatID string,
	delivered []tools.DeliveredMessage,
) bool {
	if agent == nil || len(delivered) == 0 {
		return false
	}

	sameTargetDelivered := false
	for _, msg := range delivered {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}

		if msg.Channel == sourceChannel && msg.ChatID == sourceChatID {
			agent.Sessions.AddMessage(sessionKey, "assistant", content)
			recordMemoryObservation(ctx, agent, sessionKey, msg.Channel, msg.ChatID, "assistant", "", content)
			sameTargetDelivered = true
			continue
		}

		targetSessionKey := fmt.Sprintf("outbound:%s:%s", msg.Channel, msg.ChatID)
		recordMemoryObservation(ctx, agent, targetSessionKey, msg.Channel, msg.ChatID, "assistant", "", content)
	}

	return sameTargetDelivered
}
