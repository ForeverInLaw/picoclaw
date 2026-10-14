package seahorse

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseLastDuration parses a "last" duration string like "6h", "7d", "2w", "1m".
// Returns the duration and nil error, or zero and error if invalid.
func ParseLastDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}

	re := regexp.MustCompile(`^(\d+)([hdwm])$`)
	matches := re.FindStringSubmatch(s)
	if matches == nil {
		return 0, fmt.Errorf("invalid duration format: %q (use format like 6h, 7d, 2w, 1m)", s)
	}

	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return 0, fmt.Errorf("invalid duration value: %q", matches[1])
	}
	unit := matches[2]

	switch unit {
	case "h":
		return time.Duration(value) * time.Hour, nil
	case "d":
		return time.Duration(value) * 24 * time.Hour, nil
	case "w":
		return time.Duration(value) * 7 * 24 * time.Hour, nil
	case "m":
		return time.Duration(value) * 30 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit: %q", unit)
	}
}

// GrepInput controls search across summaries and messages.
type GrepInput struct {
	ConversationID   int64      `json:"-"`
	Pattern          string     `json:"pattern"`
	Scope            string     `json:"scope,omitempty"` // "both" (default), "summary", or "message"
	Role             string     `json:"role,omitempty"`  // "user", "assistant", or "" (all)
	AllConversations bool       `json:"allConversations,omitempty"`
	Since            *time.Time `json:"since,omitempty"`
	Before           *time.Time `json:"before,omitempty"`
	Last             string     `json:"last,omitempty"` // shortcut: "6h", "7d", "2w", "1m"
	Limit            int        `json:"limit,omitempty"`
}

// GrepResult contains search results.
type GrepResult struct {
	Success        bool                `json:"success"`
	Summaries      []GrepSummaryResult `json:"summaries"`
	Messages       []GrepMessageResult `json:"messages"`
	TotalSummaries int                 `json:"totalSummaries"`
	TotalMessages  int                 `json:"totalMessages"`
	Hint           string              `json:"hint,omitempty"`
}

// GrepSummaryResult is a summary match from grep.
type GrepSummaryResult struct {
	ID             string      `json:"id"`
	Content        string      `json:"content"`
	Depth          int         `json:"depth"`
	Kind           SummaryKind `json:"kind"`
	ConversationID int64       `json:"conversationId"`
	// Rank is the bm25 relevance score (negative value, lower = better match).
	// Examples: -5.0 = excellent match, -2.0 = good match, -0.5 = partial match.
	Rank float64 `json:"rank,omitempty"`
}

// GrepMessageResult is a message match from grep.
type GrepMessageResult struct {
	ID             int64   `json:"id,string"`
	Snippet        string  `json:"snippet"`
	Role           string  `json:"role"`
	ConversationID int64   `json:"conversationId"`
	Rank           float64 `json:"rank,omitempty"` // Relevance score (more negative = better match)
}

// ExpandMessagesResult contains expanded messages.
type ExpandMessagesResult struct {
	Messages   []Message `json:"messages"`
	TokenCount int       `json:"tokenCount"`
}

func (r *RetrievalEngine) conversation(ctx context.Context, sessionKey string) (int64, error) {
	conv, err := r.store.GetConversationBySessionKey(ctx, sessionKey)
	if err != nil {
		return 0, err
	}
	if conv == nil {
		return 0, fmt.Errorf("active conversation has no stored source context")
	}
	return conv.ConversationID, nil
}

// GrepConversation searches only the caller's captured conversation scope.
func (r *RetrievalEngine) GrepConversation(
	ctx context.Context,
	sessionKey string,
	input GrepInput,
) (*GrepResult, error) {
	if input.AllConversations {
		return nil, fmt.Errorf("cross-conversation retrieval is not allowed")
	}
	id, err := r.conversation(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	input.ConversationID = id
	return r.Grep(ctx, input)
}

// ExpandSources recovers bounded, deduplicated originals through summary parents.
// Every traversed record must belong to the captured conversation.
func (r *RetrievalEngine) ExpandSources(
	ctx context.Context,
	sessionKey string,
	ids []string,
) (*ExpandMessagesResult, error) {
	if len(ids) == 0 || len(ids) > 20 {
		return nil, fmt.Errorf("provide 1 to 20 source IDs")
	}
	conversationID, err := r.conversation(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	result := &ExpandMessagesResult{Messages: []Message{}}
	seenMessages := map[int64]bool{}
	seenSummaries := map[string]bool{}
	addMessage := func(msg Message) error {
		if msg.ConversationID != conversationID {
			return fmt.Errorf("source belongs to another conversation")
		}
		if !seenMessages[msg.ID] {
			if len(result.Messages) >= 1000 {
				return fmt.Errorf("source expansion exceeds 1000 messages; expand narrower IDs")
			}
			seenMessages[msg.ID] = true
			result.Messages = append(result.Messages, msg)
			result.TokenCount += msg.TokenCount
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
		summary, e := r.store.GetSummary(ctx, id)
		if e != nil {
			return e
		}
		if summary.ConversationID != conversationID {
			return fmt.Errorf("summary belongs to another conversation")
		}
		seenSummaries[id] = true
		source, e := r.store.GetSummarySourceMessages(ctx, id)
		if e != nil {
			return e
		}
		for _, msg := range source {
			if e = addMessage(msg); e != nil {
				return e
			}
		}
		parents, e := r.store.GetSummaryParents(ctx, id)
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
		if raw, ok := strings.CutPrefix(id, "msg:"); ok {
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil {
				return nil, fmt.Errorf("invalid message ID")
			}
			msg, e := r.store.GetMessageByID(ctx, n)
			if e != nil {
				return nil, e
			}
			err = addMessage(*msg)
		} else if strings.HasPrefix(id, "sum_") {
			err = expandSummary(id, 0)
		} else {
			err = fmt.Errorf("invalid source ID")
		}
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Grep searches summaries and messages for matching content.
func (r *RetrievalEngine) Grep(ctx context.Context, input GrepInput) (*GrepResult, error) {
	if input.Pattern == "" {
		return nil, fmt.Errorf("grep: pattern is required")
	}

	limit := input.Limit
	if limit == 0 {
		limit = 20
	}

	// Handle Last parameter: convert to Since
	since := input.Since
	if input.Last != "" {
		dur, err := ParseLastDuration(input.Last)
		if err != nil {
			return nil, fmt.Errorf("grep: invalid last: %w", err)
		}
		t := time.Now().UTC().Add(-dur)
		since = &t
	}

	// Auto-detect mode: use LIKE if pattern contains %, otherwise full-text
	mode := ""
	if strings.Contains(input.Pattern, "%") {
		mode = "like"
	}

	searchInput := SearchInput{
		ConversationID:   input.ConversationID,
		Pattern:          input.Pattern,
		Mode:             mode,
		Role:             input.Role,
		AllConversations: input.AllConversations,
		Since:            since,
		Before:           input.Before,
		Limit:            limit,
	}

	result := &GrepResult{
		Success:        true,
		Summaries:      make([]GrepSummaryResult, 0),
		Messages:       make([]GrepMessageResult, 0),
		TotalSummaries: 0,
		TotalMessages:  0,
	}

	// Determine scope
	scope := input.Scope
	if scope == "" {
		scope = "both"
	}

	// Search summaries if requested
	if scope == "both" || scope == "summary" {
		sumResults, err := r.store.SearchSummaries(ctx, searchInput)
		if err != nil {
			return nil, fmt.Errorf("search summaries: %w", err)
		}
		for _, sr := range sumResults {
			if sr.SummaryID != "" {
				result.Summaries = append(result.Summaries, GrepSummaryResult{
					ID:             sr.SummaryID,
					Content:        sr.Content,
					Depth:          sr.Depth,
					Kind:           sr.Kind,
					ConversationID: sr.ConversationID,
					Rank:           sr.Rank,
				})
			}
		}
		if len(sumResults) > 0 {
			result.TotalSummaries = sumResults[0].TotalCount
		}
	}

	// Search messages if requested
	if scope == "both" || scope == "message" {
		msgResults, err := r.store.SearchMessages(ctx, searchInput)
		if err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		for _, sr := range msgResults {
			if sr.MessageID > 0 {
				result.Messages = append(result.Messages, GrepMessageResult{
					ID:             sr.MessageID,
					Snippet:        sr.Snippet,
					Role:           sr.Role,
					ConversationID: sr.ConversationID,
					Rank:           sr.Rank,
				})
			}
		}
		if len(msgResults) > 0 {
			result.TotalMessages = msgResults[0].TotalCount
		}
	}

	// Add hint if no results
	if len(result.Summaries) == 0 && len(result.Messages) == 0 {
		result.Hint = "No matches. Try: %keyword% for fuzzy search in this conversation"
	}

	return result, nil
}

// ExpandMessages retrieves full message content by IDs.
func (r *RetrievalEngine) ExpandMessages(ctx context.Context, messageIDs []int64) (*ExpandMessagesResult, error) {
	result := &ExpandMessagesResult{
		Messages: make([]Message, 0, len(messageIDs)),
	}

	for _, msgID := range messageIDs {
		msg, err := r.store.GetMessageByID(ctx, msgID)
		if err != nil {
			continue
		}
		result.Messages = append(result.Messages, *msg)
		result.TokenCount += msg.TokenCount
	}

	return result, nil
}
