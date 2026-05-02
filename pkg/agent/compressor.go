package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	PrunedToolPlaceholder = "[Old tool output cleared to save context space]"
	minSummaryTokens      = 2000
	summaryRatio          = 0.20
	summaryTokensCeiling  = 12000
	charsPerToken         = 4
)

// CompressionResult describes what the compressor did.
type CompressionResult struct {
	DroppedMessages    int
	KeptMessages       int
	CompressedMessages int
	SummaryLen         int
	PrunedToolOutputs  int
	IterativeSummary   bool
}

// CompressContext applies token-based context compression with:
//   - tool output pruning (cheap pre-pass)
//   - head protection (first 3 messages always kept)
//   - tail token budget protection (~20% of threshold)
//   - iterative LLM summary of the middle section
//   - fallback to legacy truncation if LLM unavailable
func (al *AgentLoop) CompressContext(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
) (CompressionResult, bool) {
	return al.compressContext(ctx, agent, sessionKey, true)
}

func (al *AgentLoop) compressContext(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	allowFallbackDrop bool,
) (CompressionResult, bool) {
	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 4 {
		return CompressionResult{}, false
	}

	// Step 1: Prune old tool outputs (cheap, no LLM call).
	pruned := al.pruneOldToolOutputs(history)

	// Step 2: Compute token budget boundaries.
	tailBudget := al.computeTailBudget(agent.ContextWindow)
	minHeadMessages := 3
	middleStart := nextTurnBoundaryAtOrAfter(history, minHeadMessages)
	if middleStart <= 0 || middleStart >= len(history) {
		return CompressionResult{}, false
	}

	// Step 3: Find tail boundary by cumulative tokens from end.
	rawTailStart := 0
	runningTokens := 0
	for i := len(history) - 1; i >= 0; i-- {
		toks := estimateMessageTokensForBudget(history[i])
		runningTokens += toks
		if runningTokens > tailBudget && i > middleStart {
			rawTailStart = i + 1
			break
		}
	}
	if rawTailStart < middleStart+1 {
		rawTailStart = middleStart + 1
	}

	// Step 4: The middle section is what we compress.
	tailStart := findSafeBoundary(history, rawTailStart)
	if tailStart <= middleStart {
		tailStart = nextTurnBoundaryAtOrAfter(history, rawTailStart)
	}
	if tailStart <= middleStart || tailStart > len(history) {
		return CompressionResult{}, false
	}
	middle := history[middleStart:tailStart]
	if len(middle) == 0 {
		return CompressionResult{
			PrunedToolOutputs: pruned,
			KeptMessages:      len(history),
		}, false
	}

	// Step 5: Build iterative summary from middle section.
	existing := agent.Sessions.GetSummary(sessionKey)
	middleSummary := al.compressMiddle(ctx, agent, middle, existing)
	if middleSummary == "" {
		if !allowFallbackDrop {
			return CompressionResult{
				KeptMessages:       len(history),
				CompressedMessages: len(middle),
				PrunedToolOutputs:  pruned,
			}, false
		}
		// Compression failed — truncate.
		kept := make([]providers.Message, 0, middleStart+1+len(history)-tailStart)
		kept = append(kept, history[:middleStart]...)
		if len(middle) > 0 {
			kept = append(kept, middle[0])
		}
		kept = append(kept, history[tailStart:]...)
		note := fmt.Sprintf("[Emergency compression dropped %d oldest messages]", len(history)-len(kept))
		if existing != "" {
			note = existing + "\n\n" + note
		}
		agent.Sessions.SetHistory(sessionKey, kept)
		agent.Sessions.SetSummary(sessionKey, note)
		agent.Sessions.Save(sessionKey)
		return CompressionResult{
			DroppedMessages:    len(history) - len(kept),
			KeptMessages:       len(kept),
			CompressedMessages: len(middle),
			PrunedToolOutputs:  pruned,
		}, true
	}

	// Step 6: Rebuild history; the merged summary lives in session summary only.
	kept := make([]providers.Message, 0, middleStart+len(history)-tailStart)
	kept = append(kept, history[:middleStart]...)
	kept = append(kept, history[tailStart:]...)

	agent.Sessions.SetHistory(sessionKey, kept)
	agent.Sessions.SetSummary(sessionKey, middleSummary)
	agent.Sessions.Save(sessionKey)

	dropped := len(history) - len(kept)
	iterative := existing != ""

	logger.InfoCF("agent", "Context compressed",
		map[string]any{
			"session_key":       sessionKey,
			"dropped_msgs":      dropped,
			"kept_msgs":         len(kept),
			"summary_len":       len(middleSummary),
			"pruned_tools":      pruned,
			"iterative_summary": iterative,
		})

	return CompressionResult{
		DroppedMessages:    dropped,
		KeptMessages:       len(kept),
		CompressedMessages: len(middle),
		SummaryLen:         len(middleSummary),
		PrunedToolOutputs:  pruned,
		IterativeSummary:   iterative,
	}, true
}

// pruneOldToolOutputs replaces content of older tool-result messages in the first
// 60% of history with a placeholder after the first such message in that window.
// Returns the number of pruned outputs.
func (al *AgentLoop) pruneOldToolOutputs(history []providers.Message) int {
	if len(history) < 6 {
		return 0
	}
	pruneUpTo := len(history) * 6 / 10
	pruned := 0
	seenTool := false
	for i := 0; i < pruneUpTo && i < len(history); i++ {
		m := &history[i]
		if m.Role == "tool" || m.ToolCallID != "" {
			if seenTool && len(m.Content) > 200 {
				m.Content = PrunedToolPlaceholder
				pruned++
			} else {
				seenTool = true
			}
		}
	}
	return pruned
}

func nextTurnBoundaryAtOrAfter(history []providers.Message, minIndex int) int {
	if minIndex <= 0 {
		return 0
	}
	for _, idx := range parseTurnBoundaries(history) {
		if idx >= minIndex {
			return idx
		}
	}
	return len(history)
}

// computeTailBudget returns the token budget for tail protection.
func (al *AgentLoop) computeTailBudget(contextWindow int) int {
	threshold := int(float64(contextWindow) * 0.70)
	budget := int(float64(threshold) * summaryRatio)
	if budget > summaryTokensCeiling {
		budget = summaryTokensCeiling
	}
	if budget < minSummaryTokens {
		budget = minSummaryTokens
	}
	return budget
}

// compressMiddle generates a summary for the middle section.
// If existing is not empty, iteratively merges.
func (al *AgentLoop) compressMiddle(
	ctx context.Context,
	agent *AgentInstance,
	middle []providers.Message,
	existing string,
) string {
	totalChars := 0
	for _, m := range middle {
		totalChars += len(m.Content)
	}
	// Scaled summary budget: 20% of compressed content, clamped.
	budgetChars := totalChars * 20 / 100
	if budgetChars < minSummaryTokens*charsPerToken {
		budgetChars = minSummaryTokens * charsPerToken
	}
	if budgetChars > summaryTokensCeiling*charsPerToken {
		budgetChars = summaryTokensCeiling * charsPerToken
	}
	maxTokens := budgetChars / charsPerToken

	// Build the prompt.
	var sb strings.Builder
	if existing != "" {
		sb.WriteString("You are updating an existing conversation summary. ")
		sb.WriteString("Incorporate new information from the segment below while ")
		sb.WriteString("preserving all critical details from the existing summary.\n\n")
		sb.WriteString("EXISTING SUMMARY (keep this, update with new details):\n")
		sb.WriteString(existing + "\n\n")
		sb.WriteString("NEW CONVERSATION SEGMENT (summarize and merge):\n")
	} else {
		sb.WriteString("Provide a concise summary of this conversation segment. ")
		sb.WriteString("Preserve: key decisions, file changes, tool results, ")
		sb.WriteString("pending tasks, and user instructions. ")
		sb.WriteString("Drop: verbose tool call arguments, intermediate LLM ")
		sb.WriteString("reasoning, and redundant confirmations.\n\n")
	}
	sb.WriteString("CONVERSATION:\n")
	for _, m := range middle {
		content := m.Content
		if len(content) > 8000 {
			content = content[:4000] + "\n...[truncated]...\n" + content[len(content)-4000:]
		}
		fmt.Fprintf(&sb, "[%s]: %s\n", m.Role, content)
	}

	return al.summarizeWithRetry(ctx, agent, sb.String(), maxTokens)
}

// summarizeWithRetry calls the LLM for summarization with retries.
func (al *AgentLoop) summarizeWithRetry(
	ctx context.Context,
	agent *AgentInstance,
	prompt string,
	maxTokens int,
) string {
	for attempt := 0; attempt < summaryLLMRetryLimit; attempt++ {
		resp, err := al.callSummaryLLM(ctx, agent, prompt, maxTokens, nil)
		if err == nil && resp != nil && strings.TrimSpace(resp.Content) != "" {
			return strings.TrimSpace(resp.Content)
		}
		if attempt < summaryLLMRetryLimit-1 && !waitBeforeSummaryRetry(ctx) {
			break
		}
	}
	return ""
}

func (al *AgentLoop) callSummaryLLM(
	ctx context.Context,
	agent *AgentInstance,
	prompt string,
	maxTokens int,
	extraOptions map[string]any,
) (*providers.LLMResponse, error) {
	if agent == nil || agent.Provider == nil {
		return nil, fmt.Errorf("summary LLM unavailable")
	}

	options := map[string]any{
		"max_tokens":  maxTokens,
		"temperature": 0.3,
	}
	for k, v := range extraOptions {
		options[k] = v
	}

	messages := []providers.Message{{Role: "user", Content: prompt}}
	run := func(ctx context.Context, model string) (*providers.LLMResponse, error) {
		al.activeRequests.Add(1)
		defer al.activeRequests.Done()
		return agent.Provider.Chat(ctx, messages, nil, model, options)
	}

	if len(agent.Candidates) > 1 && al.fallback != nil {
		result, err := al.fallback.Execute(ctx, agent.Candidates, func(ctx context.Context, _, model string) (*providers.LLMResponse, error) {
			return run(ctx, model)
		})
		if err != nil {
			return nil, err
		}
		return result.Response, nil
	}

	return run(ctx, resolvedCandidateModel(agent.Candidates, agent.Model))
}

// estimateMessageTokensForBudget estimates tokens for a single message.
// Uses char/4 heuristic for content, JSON for tool args.
func estimateMessageTokensForBudget(m providers.Message) int {
	tokens := len(m.Content) / charsPerToken
	if m.ToolCallID != "" {
		tokens += 10
	}
	for _, arg := range m.ToolCalls {
		if b, err := json.Marshal(arg.Arguments); err == nil {
			tokens += len(b) / charsPerToken
		}
		if arg.Name != "" {
			tokens += 20
		}
	}
	return tokens
}
