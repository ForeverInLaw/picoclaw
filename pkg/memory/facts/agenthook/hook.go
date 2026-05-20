// Package agenthook adapts the facts memory subsystem to PicoClaw's
// agent hook interface (LLMInterceptor). The hook injects recalled
// facts as a system-message prefix before each LLM call and asynchronously
// enqueues extraction jobs after each turn completes.
//
// Wiring lives in the bootstrap flow; this package is import-safe even
// when memory is disabled.
package agenthook

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/memory/facts"
	"github.com/sipeed/picoclaw/pkg/memory/facts/extract"
	"github.com/sipeed/picoclaw/pkg/memory/facts/recall"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// ChannelScope decides whether the hook applies to a given channel and
// builds namespace strings from the request context.
type ChannelScope interface {
	// Applies reports whether this scope should run for the given channel.
	Applies(channel string) bool
	// Namespaces returns the namespace set to query for this turn.
	Namespaces(channel, chatID, botUsername string) []string
}

// TelegramScope is the v1 scope: applies only to "telegram" and returns
// tg:chat:<id> + tg:bot:<botUsername>. Per-user namespace requires
// downstream plumbing of user IDs and can be added later.
type TelegramScope struct {
	BotUsername string
}

func (s TelegramScope) Applies(channel string) bool {
	return channel == "telegram"
}

func (s TelegramScope) Namespaces(channel, chatID, botUsername string) []string {
	if chatID == "" {
		return nil
	}
	if botUsername == "" {
		botUsername = s.BotUsername
	}
	out := []string{facts.NSPrefixTGChat + chatID}
	if botUsername != "" {
		out = append(out, facts.NSPrefixTGBot+botUsername)
	}
	return out
}

// AsyncWorker is the minimal queue interface the hook depends on.
// extract.AsyncWorker satisfies it.
type AsyncWorker interface {
	Enqueue(j extract.Job)
}

// Hook implements agent.LLMInterceptor.
type Hook struct {
	scope    ChannelScope
	recaller *recall.Recaller
	queue    AsyncWorker
}

// New builds a hook. If any required dependency is nil the hook is a no-op
// (callers may pass nils when memory is disabled to keep wiring simple).
func New(scope ChannelScope, r *recall.Recaller, q AsyncWorker) *Hook {
	return &Hook{scope: scope, recaller: r, queue: q}
}

// Compile-time check.
var _ agent.LLMInterceptor = (*Hook)(nil)

// BeforeLLM injects recalled facts as a system-prefix message.
func (h *Hook) BeforeLLM(ctx context.Context, req *agent.LLMHookRequest) (*agent.LLMHookRequest, agent.HookDecision, error) {
	if h == nil || h.recaller == nil || h.scope == nil || req == nil {
		return req, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	if !h.scope.Applies(req.Channel) {
		return req, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	ns := h.scope.Namespaces(req.Channel, req.ChatID, "")
	if len(ns) == 0 {
		return req, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	input := latestUserContent(req.Messages)
	if strings.TrimSpace(input) == "" {
		return req, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	hits, err := h.recaller.Recall(ctx, ns, input)
	if err != nil || len(hits) == 0 {
		// Recall failures are non-fatal; keep the turn moving.
		return req, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	prefix := recall.Render(hits)
	if prefix == "" {
		return req, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}

	out := req.Clone()
	out.Messages = prependSystem(out.Messages, prefix)
	return out, agent.HookDecision{Action: agent.HookActionModify}, nil
}

// AfterLLM enqueues an extraction job non-blocking.
func (h *Hook) AfterLLM(ctx context.Context, resp *agent.LLMHookResponse) (*agent.LLMHookResponse, agent.HookDecision, error) {
	if h == nil || h.queue == nil || h.scope == nil || resp == nil {
		return resp, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	if !h.scope.Applies(resp.Channel) {
		return resp, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	if resp.Meta.SessionKey == "" {
		return resp, agent.HookDecision{Action: agent.HookActionContinue}, nil
	}
	h.queue.Enqueue(extract.Job{
		SessionKey: resp.Meta.SessionKey,
		// Window + indices are populated by the worker when it reads
		// recent history. Iteration is a best-effort upper bound.
		EndIdx: resp.Meta.Iteration + 1,
	})
	return resp, agent.HookDecision{Action: agent.HookActionContinue}, nil
}

// --- helpers ---

func latestUserContent(msgs []protocoltypes.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}

func prependSystem(msgs []protocoltypes.Message, prefix string) []protocoltypes.Message {
	// If the first message is already a system message, prepend to its content.
	if len(msgs) > 0 && msgs[0].Role == "system" {
		merged := strings.TrimRight(msgs[0].Content, "\n") + "\n\n" + prefix
		out := make([]protocoltypes.Message, len(msgs))
		copy(out, msgs)
		out[0].Content = merged
		return out
	}
	out := make([]protocoltypes.Message, 0, len(msgs)+1)
	out = append(out, protocoltypes.Message{Role: "system", Content: prefix})
	out = append(out, msgs...)
	return out
}

// SafeNamespace returns a safe namespace string for the given channel and chat.
// Exposed for tests.
func SafeNamespace(prefix, id string) string {
	id = strings.ReplaceAll(id, "/", "_")
	return fmt.Sprintf("%s%s", prefix, id)
}
