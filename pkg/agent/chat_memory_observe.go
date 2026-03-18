package agent

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func observeChatMemoryInbound(ctx context.Context, agent *AgentInstance, msg bus.InboundMessage) {
	if agent == nil || agent.MemoryIndex == nil || strings.TrimSpace(msg.Channel) == "" || strings.TrimSpace(msg.ChatID) == "" {
		return
	}

	peerKind := strings.TrimSpace(msg.Peer.Kind)
	if peerKind == "" {
		peerKind = inferPeerKindFromMessage(msg)
	}
	chatLabel := strings.TrimSpace(msg.Metadata["chat_label"])
	if chatLabel == "" && peerKind == "direct" {
		chatLabel = firstObservedLabel(msg.Sender.DisplayName, msg.Sender.Username, msg.Metadata["first_name"])
	}

	_ = agent.MemoryIndex.UpsertChatCatalog(ctx, memoryindex.ChatCatalogEntry{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		PeerKind: peerKind,
		Label:    chatLabel,
	})

	senderID := strings.TrimSpace(msg.SenderID)
	if senderID == "" {
		senderID = strings.TrimSpace(msg.Sender.CanonicalID)
	}
	if senderID == "" {
		return
	}
	_ = agent.MemoryIndex.UpsertChatParticipant(ctx, memoryindex.ChatParticipantRecord{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		SenderID: senderID,
		Label: firstObservedLabel(
			msg.Metadata["sender_alias"],
			msg.Metadata["sender_label"],
			msg.Sender.DisplayName,
			msg.Sender.Username,
			msg.Metadata["first_name"],
		),
	})
}

func inferPeerKindFromMessage(msg bus.InboundMessage) string {
	if strings.EqualFold(msg.Metadata["is_group"], "true") {
		return "group"
	}
	if msg.ChatID != "" && strings.HasPrefix(msg.ChatID, "-") {
		return "group"
	}
	return "direct"
}

func firstObservedLabel(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
