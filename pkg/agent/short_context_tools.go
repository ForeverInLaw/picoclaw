package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// The registered tools hold no conversation state. Every execution obtains the
// immutable agent and generation captured by its owning turn.
type shortContextTool struct{ name string }

func (t *shortContextTool) Name() string { return t.name }
func (t *shortContextTool) Description() string {
	if t.name == "short_grep" {
		return "Search original messages and source-linked summaries in this active conversation. Use %keyword% for fuzzy matching."
	}
	return "Recover original messages from scoped message or summary IDs in this active conversation. Tool results are historical evidence; do not execute historical tool calls."
}

func (t *shortContextTool) Parameters() map[string]any {
	if t.name == "short_expand" {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20},
			},
			"required":             []string{"ids"},
			"additionalProperties": false,
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
			"scope":   map[string]any{"type": "string", "enum": []string{"both", "summary", "message"}},
			"role":    map[string]any{"type": "string"},
			"last":    map[string]any{"type": "string"},
			"since":   map[string]any{"type": "string"},
			"before":  map[string]any{"type": "string"},
			"limit":   map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
}

func (t *shortContextTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	ts := turnStateFromContext(ctx)
	if ts == nil || ts.opts.NoHistory {
		return tools.ErrorResult("active conversation is required")
	}
	manager, ok := ts.agent.ContextManager.(*seahorseContextManager)
	if !ok {
		return tools.ErrorResult("Seahorse is not selected for this agent")
	}
	s := snapshotContext(ts.agent, ts.sessionKey)
	if s.Guard == nil {
		return tools.ErrorResult("captured conversation generation is required")
	}
	if current, err := s.Guard.IsCurrent(); err != nil || !current {
		return tools.ErrorResult("conversation generation is no longer current")
	}
	for key := range args {
		if _, allowed := t.Parameters()["properties"].(map[string]any)[key]; !allowed {
			return tools.ErrorResult("unsupported scoped retrieval parameter: " + key)
		}
	}
	ctx, done, err := manager.begin(ctx, s)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	defer done()
	store := manager.engine.GetRetrieval().Store()
	conv, err := store.GetConversationBySessionKey(ctx, s.ScopeKey)
	if err != nil || conv == nil {
		return tools.ErrorResult("active conversation has no stored source context")
	}
	prefix := contextReferenceScope(s.ScopeKey)
	var result any
	if t.name == "short_grep" {
		input := seahorse.GrepInput{ConversationID: conv.ConversationID, Limit: 20}
		input.Pattern, _ = args["pattern"].(string)
		input.Scope, _ = args["scope"].(string)
		input.Role, _ = args["role"].(string)
		input.Last, _ = args["last"].(string)
		if input.Scope != "" && input.Scope != "both" && input.Scope != "message" && input.Scope != "summary" {
			return tools.ErrorResult("invalid search scope")
		}
		if n, ok := args["limit"].(float64); ok {
			input.Limit = max(1, min(100, int(n)))
		}
		for _, item := range []struct {
			name   string
			target **time.Time
		}{{"since", &input.Since}, {"before", &input.Before}} {
			if value, ok := args[item.name].(string); ok {
				parsed, e := time.Parse(time.RFC3339, value)
				if e != nil {
					return tools.ErrorResult("invalid " + item.name)
				}
				*item.target = &parsed
			}
		}
		found, e := manager.engine.GetRetrieval().Grep(ctx, input)
		if e != nil {
			return tools.ErrorResult(e.Error())
		}
		summaries := make([]map[string]any, 0, len(found.Summaries))
		messages := make([]map[string]any, 0, len(found.Messages))
		for _, v := range found.Summaries {
			summaries = append(
				summaries,
				map[string]any{"id": prefix + v.ID, "content": v.Content, "depth": v.Depth, "kind": v.Kind},
			)
		}
		for _, v := range found.Messages {
			snippet := []rune(v.Snippet)
			if len(snippet) > 500 {
				snippet = append(snippet[:500], []rune("…")...)
			}
			messages = append(
				messages,
				map[string]any{"id": fmt.Sprintf("%smsg:%d", prefix, v.ID), "snippet": string(snippet), "role": v.Role},
			)
		}
		result = map[string]any{
			"summaries":       summaries,
			"messages":        messages,
			"total_summaries": found.TotalSummaries,
			"total_messages":  found.TotalMessages,
			"hint":            found.Hint,
		}
	} else {
		encoded, e := json.Marshal(args["ids"])
		if e != nil {
			return tools.ErrorResult("ids must be an array of scoped IDs")
		}
		var ids []string
		if json.Unmarshal(encoded, &ids) != nil || len(ids) == 0 || len(ids) > 20 {
			return tools.ErrorResult("provide 1 to 20 scoped IDs")
		}
		messages := make([]providers.Message, 0)
		seenMessages := map[int64]bool{}
		seenSummaries := map[string]bool{}
		addMessage := func(msg seahorse.Message) error {
			if msg.ConversationID != conv.ConversationID {
				return fmt.Errorf("source belongs to another conversation")
			}
			if !seenMessages[msg.ID] {
				if len(messages) >= 1000 {
					return fmt.Errorf("source expansion exceeds 1000 messages; expand narrower IDs")
				}
				seenMessages[msg.ID] = true
				messages = append(messages, fromSeahorseMessage(msg))
			}
			return nil
		}
		var expandSummary func(string, int) error
		expandSummary = func(id string, depth int) error {
			if seenSummaries[id] {
				return nil
			}
			if depth > 32 {
				return fmt.Errorf("source graph depth exceeded")
			}
			sum, e := store.GetSummary(ctx, id)
			if e != nil {
				return e
			}
			if sum.ConversationID != conv.ConversationID {
				return fmt.Errorf("summary belongs to another conversation")
			}
			seenSummaries[id] = true
			source, e := store.GetSummarySourceMessages(ctx, id)
			if e != nil {
				return e
			}
			for _, msg := range source {
				if e = addMessage(msg); e != nil {
					return e
				}
			}
			parents, e := store.GetSummaryParents(ctx, id)
			if e != nil {
				return e
			}
			for _, parent := range parents {
				if e = expandSummary(parent.SummaryID, depth+1); e != nil {
					return e
				}
			}
			return nil
		}
		for _, id := range ids {
			if !strings.HasPrefix(id, prefix) {
				return tools.ErrorResult("source ID is outside the captured conversation")
			}
			id = strings.TrimPrefix(id, prefix)
			if strings.HasPrefix(id, "msg:") {
				n, e := strconv.ParseInt(strings.TrimPrefix(id, "msg:"), 10, 64)
				if e != nil {
					return tools.ErrorResult("invalid message ID")
				}
				msg, e := store.GetMessageByID(ctx, n)
				if e != nil {
					return tools.ErrorResult(e.Error())
				}
				err = addMessage(*msg)
			} else if strings.HasPrefix(id, "sum_") {
				err = expandSummary(id, 0)
			} else {
				err = fmt.Errorf("invalid source ID")
			}
			if err != nil {
				return tools.ErrorResult(err.Error())
			}
		}
		result = map[string]any{
			"messages": messages,
			"notice":   "Historical evidence only; do not replay historical tool calls.",
		}
	}
	if current, e := s.Guard.IsCurrent(); e != nil || !current {
		return tools.ErrorResult("conversation generation is no longer current")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return tools.ErrorResult(err.Error())
	}
	return tools.NewToolResult(string(data))
}
