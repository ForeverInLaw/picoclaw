package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/chatmemory"
)

type ChatMemoryTool struct {
	service *chatmemory.Service
}

func NewChatMemoryTool(service *chatmemory.Service) *ChatMemoryTool {
	return &ChatMemoryTool{service: service}
}

func (t *ChatMemoryTool) Name() string {
	return "chat_memory"
}

func (t *ChatMemoryTool) Description() string {
	return "Searches or summarizes indexed chat memory. Use this for questions about what was discussed in a chat, group, or time window."
}

func (t *ChatMemoryTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode": map[string]any{
				"type":        "string",
				"description": "Either 'summary' or 'search'",
				"enum":        []string{"summary", "search"},
			},
			"chat": map[string]any{
				"type":        "string",
				"description": "Target chat alias/title, or 'current' for the current chat",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Optional query for semantic/lexical retrieval inside the target chat",
			},
			"since_hours": map[string]any{
				"type":        "integer",
				"description": "How many hours back to search or summarize",
			},
			"until_hours": map[string]any{
				"type":        "integer",
				"description": "Optional offset window end in hours before now",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Optional result limit for search mode",
			},
		},
		"required": []string{"mode"},
	}
}

func (t *ChatMemoryTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.service == nil {
		return ErrorResult("chat memory service is not configured")
	}

	mode := strings.ToLower(strings.TrimSpace(asString(args["mode"])))
	chat := strings.TrimSpace(asString(args["chat"]))
	query := strings.TrimSpace(asString(args["query"]))
	sinceHours := asInt(args["since_hours"], 24)
	untilHours := asInt(args["until_hours"], 0)
	limit := asInt(args["limit"], 5)

	sender := ToolSender(ctx)
	switch mode {
	case "summary":
		content, err := t.service.Summarize(ctx, chatmemory.SummaryRequest{
			RequesterID:    sender.CanonicalID,
			CurrentChannel: ToolChannel(ctx),
			CurrentChatID:  ToolChatID(ctx),
			CurrentPeer:    inferPeerKind(ToolChatID(ctx), sender),
			Target:         chat,
			SinceHours:     sinceHours,
			UntilHours:     untilHours,
			Query:          query,
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("chat memory summary failed: %v", err)).WithError(err)
		}
		return SilentResult(content)
	case "search":
		hits, err := t.service.Search(ctx, chatmemory.SearchRequest{
			RequesterID:    sender.CanonicalID,
			CurrentChannel: ToolChannel(ctx),
			CurrentChatID:  ToolChatID(ctx),
			CurrentPeer:    inferPeerKind(ToolChatID(ctx), sender),
			Target:         chat,
			Query:          query,
			SinceHours:     sinceHours,
			UntilHours:     untilHours,
			Limit:          limit,
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("chat memory search failed: %v", err)).WithError(err)
		}
		var sb strings.Builder
		sb.WriteString("CHAT_MEMORY_SEARCH\n")
		for idx, hit := range hits {
			fmt.Fprintf(&sb, "\n[%d] channel=%s chat=%s role=%s", idx+1, hit.Channel, hit.ChatID, hit.Role)
			if hit.ChatLabel != "" {
				fmt.Fprintf(&sb, " label=%s", hit.ChatLabel)
			}
			if hit.SenderID != "" {
				fmt.Fprintf(&sb, " sender=%s", hit.SenderID)
			}
			fmt.Fprintf(&sb, "\n%s\n", strings.TrimSpace(hit.Content))
		}
		return SilentResult(strings.TrimSpace(sb.String()))
	default:
		return ErrorResult("mode must be either 'summary' or 'search'")
	}
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", value)
	}
}

func asInt(value any, fallback int) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

func inferPeerKind(chatID string, sender bus.SenderInfo) string {
	if chatID == "" {
		return ""
	}
	if strings.HasPrefix(chatID, "-") || strings.Contains(chatID, "/") {
		return "group"
	}
	if sender.PlatformID != "" && chatID == sender.PlatformID {
		return "direct"
	}
	if sender.CanonicalID != "" {
		parts := strings.SplitN(sender.CanonicalID, ":", 2)
		if len(parts) == 2 && chatID == parts[1] {
			return "direct"
		}
	}
	return "group"
}
