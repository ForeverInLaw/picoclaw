package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/chatmemory"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

func lookupRetrievedMemories(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey, channel, chatID, peerKind, senderID, userMessage string,
	since time.Time,
) string {
	if agent == nil || agent.MemoryIndex == nil {
		return ""
	}

	query := retrievalQuery(userMessage)
	if query == "" {
		return ""
	}

	var (
		hits []memoryindex.Hit
		err  error
	)
	if agent.ChatMemory != nil && since.IsZero() {
		hits, err = agent.ChatMemory.Search(ctx, chatmemory.SearchRequest{
			RequesterID:    senderID,
			CurrentChannel: channel,
			CurrentChatID:  chatID,
			CurrentPeer:    peerKind,
			Query:          query,
			Limit:          5,
		})
	} else {
		hits, err = agent.MemoryIndex.Search(ctx, memoryindex.SearchRequest{
			Query:      query,
			SessionKey: sessionKey,
			Channel:    channel,
			ChatID:     chatID,
			Since:      since,
		})
	}
	if err != nil || len(hits) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("RETRIEVED_MEMORY: Relevant prior dialog excerpts. Use them as reference, but prefer explicit current instructions.\n")
	for idx, hit := range hits {
		fmt.Fprintf(&sb, "\n[%d] channel=%s chat=%s role=%s", idx+1, hit.Channel, hit.ChatID, hit.Role)
		if hit.SenderID != "" {
			fmt.Fprintf(&sb, " sender=%s", hit.SenderID)
		}
		if !hit.CreatedAt.IsZero() {
			fmt.Fprintf(&sb, " at=%s", hit.CreatedAt.Format(time.RFC3339))
		}
		fmt.Fprintf(&sb, "\n%s\n", strings.TrimSpace(hit.Content))
	}
	return strings.TrimSpace(sb.String())
}

func retrievalQuery(userMessage string) string {
	message := strings.TrimSpace(userMessage)
	if message == "" {
		return ""
	}

	if strings.HasPrefix(message, "[telegram_group_message]") || strings.HasPrefix(message, "[scheduled_reminder_trigger]") {
		if parts := strings.SplitN(message, "\n\n", 2); len(parts) == 2 {
			message = strings.TrimSpace(parts[1])
		}
	}
	return message
}
