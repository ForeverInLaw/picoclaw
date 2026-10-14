// PicoClaw - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tokenizer"
)

// parseTurnBoundaries returns the starting index of each Turn in the history.
// A Turn is a complete "user input → LLM iterations → final response" cycle
// (as defined in #1316). Each Turn begins at a user message and extends
// through all subsequent assistant/tool messages until the next user message.
//
// Cutting at a Turn boundary guarantees that no tool-call sequence
// (assistant+ToolCalls → tool results) is split across the cut.
func parseTurnBoundaries(history []providers.Message) []int {
	var starts []int
	for i, msg := range history {
		if msg.Role == "user" {
			starts = append(starts, i)
		}
	}
	return starts
}

// isSafeBoundary reports whether index is a valid Turn boundary — i.e.,
// a position where the kept portion (history[index:]) begins at a user
// message, so no tool-call sequence is torn apart.
func isSafeBoundary(history []providers.Message, index int) bool {
	if index <= 0 || index >= len(history) {
		return true
	}
	return history[index].Role == "user"
}

// findSafeBoundary locates the nearest Turn boundary to targetIndex.
// It prefers the boundary at or before targetIndex (preserving more recent
// context). Falls back to the nearest boundary after targetIndex, and
// returns targetIndex unchanged only when no Turn boundary exists at all.
func findSafeBoundary(history []providers.Message, targetIndex int) int {
	if len(history) == 0 {
		return 0
	}
	if targetIndex <= 0 {
		return 0
	}
	if targetIndex >= len(history) {
		return len(history)
	}

	turns := parseTurnBoundaries(history)
	if len(turns) == 0 {
		return targetIndex
	}

	// Find the last Turn boundary at or before targetIndex.
	// Prefer backward: keeps more recent messages.
	backward := -1
	for _, t := range turns {
		if t <= targetIndex {
			backward = t
		}
	}
	if backward > 0 {
		return backward
	}

	// No valid Turn boundary before target (or only at index 0 which
	// would keep everything). Use the first Turn after targetIndex.
	for _, t := range turns {
		if t > targetIndex {
			return t
		}
	}

	// No Turn boundary after targetIndex either. The only boundary is at
	// index 0, meaning the entire history is a single Turn. Return 0 to
	// signal that safe compression is not possible — callers check for
	// mid <= 0 and skip compression in that case.
	return 0
}

// estimateMessageTokens estimates the token count for a single message,
// including Content, ReasoningContent, ToolCalls arguments, ToolCallID
// metadata, and Media items. Uses a heuristic of 2.5 characters per token.
func estimateMessageTokens(msg providers.Message) int { return tokenizer.EstimateMessageTokens(msg) }

// estimateToolDefsTokens budgets serialized names, descriptions and schemas.
func estimateToolDefsTokens(defs []providers.ToolDefinition) int {
	return tokenizer.EstimateToolDefsTokens(defs)
}

// isOverContextBudget checks whether the assembled messages plus tool definitions
// and output reserve would exceed the model's context window. This enables
// proactive compression before calling the LLM, rather than reacting to 400 errors.
func isOverContextBudget(
	contextWindow int,
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	maxTokens int,
) bool {
	return estimateRequestTokens(messages, toolDefs, maxTokens) > contextWindow
}

// estimateRequestTokens budgets the assembled input and the next answer using
// the same heuristic for proactive, final-call and background checks.
func estimateRequestTokens(messages []providers.Message, toolDefs []providers.ToolDefinition, maxTokens int) int {
	msgTokens := 0
	for _, m := range messages {
		msgTokens += estimateMessageTokens(m)
	}

	toolTokens := estimateToolDefsTokens(toolDefs)
	return msgTokens + toolTokens + max(0, maxTokens)
}

// trimContextRequest removes only older complete turns from the prepared
// request. System/contributor messages and the active turn at protectedStart
// are retained. Session history is unchanged by this final request check.
func trimContextRequest(
	messages []providers.Message,
	toolDefs []providers.ToolDefinition,
	maxTokens, contextWindow, protectedStart int,
) ([]providers.Message, int, bool) {
	dropped := 0
	for isOverContextBudget(contextWindow, messages, toolDefs, maxTokens) {
		turns := parseTurnBoundaries(messages)
		if len(turns) < 2 || turns[1] > protectedStart {
			return messages, dropped, false
		}
		cut := turns[1]
		trimmed := make([]providers.Message, 0, len(messages))
		for _, message := range messages[:cut] {
			if message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
				trimmed = append(trimmed, message)
			}
		}
		removed := cut - len(trimmed)
		trimmed = append(trimmed, messages[cut:]...)
		messages = trimmed
		protectedStart -= removed
		dropped += removed
	}
	return messages, dropped, true
}
