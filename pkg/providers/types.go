package providers

import (
	"context"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

type (
	ContentPhase           = protocoltypes.ContentPhase
	StreamContent          = protocoltypes.StreamContent
	ToolCall               = protocoltypes.ToolCall
	FunctionCall           = protocoltypes.FunctionCall
	LLMResponse            = protocoltypes.LLMResponse
	UsageInfo              = protocoltypes.UsageInfo
	Message                = protocoltypes.Message
	ToolDefinition         = protocoltypes.ToolDefinition
	ToolFunctionDefinition = protocoltypes.ToolFunctionDefinition
	ExtraContent           = protocoltypes.ExtraContent
	GoogleExtra            = protocoltypes.GoogleExtra
	ContentBlock           = protocoltypes.ContentBlock
	CacheControl           = protocoltypes.CacheControl
)

type LLMProvider interface {
	Chat(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
	) (*LLMResponse, error)
	GetDefaultModel() string
}

// StreamingLLMProvider is an optional capability for providers that can
// surface incremental text snapshots while a response is being generated.
// onUpdate receives the full accumulated text so far, not a raw delta.
type StreamingLLMProvider interface {
	ChatStream(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
		onUpdate func(content string),
	) (*LLMResponse, error)
}

type StatefulProvider interface {
	LLMProvider
	Close()
}

// StreamingProvider is the compatibility name for StreamingLLMProvider.
type StreamingProvider = StreamingLLMProvider

// ThinkingCapable is an optional interface for providers that support
// extended thinking (e.g. Anthropic). Used by the agent loop to warn
// when thinking_level is configured but the active provider cannot use it.
type ThinkingCapable interface {
	SupportsThinking() bool
}

// NativeSearchCapable is an optional interface for providers that support
// built-in web search during LLM inference (e.g. OpenAI web_search_preview,
// xAI Grok search). When the active provider implements this interface and
// returns true, the agent loop can hide the client-side web_search tool to
// avoid duplicate search surfaces and use the provider's native search instead.
type NativeSearchCapable interface {
	SupportsNativeSearch() bool
}

// FailoverReason classifies why an LLM request failed for fallback decisions.
type FailoverReason string

const (
	FailoverAuth         FailoverReason = "auth"
	FailoverIncompatible FailoverReason = "incompatible"
	FailoverRateLimit    FailoverReason = "rate_limit"
	FailoverBilling      FailoverReason = "billing"
	FailoverTimeout      FailoverReason = "timeout"
	FailoverFormat       FailoverReason = "format"
	FailoverOverloaded   FailoverReason = "overloaded"
	FailoverUnknown      FailoverReason = "unknown"
	FailoverNetwork      FailoverReason = "network"
)

// FailoverError wraps an LLM provider error with classification metadata.
type FailoverError struct {
	Reason     FailoverReason
	Provider   string
	Model      string
	Status     int
	RetryAfter time.Duration
	Wrapped    error
	Candidate  FallbackCandidate
}

func (e *FailoverError) Error() string {
	return fmt.Sprintf("failover(%s): provider=%s model=%s status=%d: %v",
		e.Reason, e.Provider, e.Model, e.Status, e.Wrapped)
}

func (e *FailoverError) Unwrap() error {
	return e.Wrapped
}

// IsRetriable returns true if this error should trigger fallback to next candidate.
// Non-retriable: Format errors (bad request structure, image dimension/size).
func (e *FailoverError) IsRetriable() bool {
	return e.Reason != FailoverFormat
}

// ModelConfig holds primary model and fallback list.
type ModelConfig struct {
	Primary   string
	Fallbacks []string
}

const (
	ContentPhaseUnknown    = protocoltypes.ContentPhaseUnknown
	ContentPhaseAnswer     = protocoltypes.ContentPhaseAnswer
	ContentPhaseCommentary = protocoltypes.ContentPhaseCommentary
	ContentPhaseReasoning  = protocoltypes.ContentPhaseReasoning
)

// PhasedStreamingProvider only marks text as an answer when the upstream API does.
// Unclassified content must be held until the complete response is known.
type PhasedStreamingProvider interface {
	ChatStreamContent(
		ctx context.Context,
		messages []Message,
		tools []ToolDefinition,
		model string,
		options map[string]any,
		onUpdate func(StreamContent),
	) (*LLMResponse, error)
}

// IsTransient identifies failures that can recover by retrying the same request.
func (e *FailoverError) IsTransient() bool {
	return e.Reason == FailoverTimeout || e.Reason == FailoverNetwork || e.Reason == FailoverRateLimit ||
		e.Reason == FailoverOverloaded
}

// IncompatibleCandidateError excludes a candidate from this operation without
// declaring its credentials or endpoint unhealthy.
type IncompatibleCandidateError struct{ Reason string }

func (e *IncompatibleCandidateError) Error() string { return "incompatible candidate: " + e.Reason }
