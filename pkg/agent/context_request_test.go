package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type contextRetryProvider struct {
	recordingProvider
	calls int
}

func (p *contextRetryProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	defs []providers.ToolDefinition,
	model string,
	opts map[string]any,
) (*providers.LLMResponse, error) {
	p.calls++
	response, err := p.recordingProvider.Chat(ctx, messages, defs, model, opts)
	if p.calls == 1 {
		return nil, fmt.Errorf("context window exceeded")
	}
	return response, err
}

func TestContextRetryPreservesResolvedCurrentAttachments(t *testing.T) {
	al, cfg, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer mb.Close()
	defer al.Close()
	p := &contextRetryProvider{}
	agent := al.GetRegistry().GetDefaultAgent()
	agent.Provider = p
	agent.ContextWindow = 100000
	agent.SummarizeMessageThreshold = 1000
	agent.Sessions.SetHistory("agent:main:attachment-retry", []providers.Message{
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "recent question"},
		{Role: "assistant", Content: "recent answer"},
	})
	store := media.NewFileMediaStore()
	al.SetMediaStore(store)
	imagePath := filepath.Join(cfg.Agents.Defaults.Workspace, "image.png")
	documentPath := filepath.Join(cfg.Agents.Defaults.Workspace, "document.txt")
	require.NoError(t, os.WriteFile(imagePath, []byte("image"), 0o600))
	require.NoError(t, os.WriteFile(documentPath, []byte("document"), 0o600))
	imageRef, err := store.Store(imagePath, media.MediaMeta{ContentType: "image/png"}, "attachment-retry")
	require.NoError(t, err)
	documentRef, err := store.Store(documentPath, media.MediaMeta{ContentType: "text/plain"}, "attachment-retry")
	require.NoError(t, err)
	msg := directIngressMessage("Describe these attachments [file]")
	msg.SessionKey = "agent:main:attachment-retry"
	msg.Media = []string{imageRef, documentRef}
	response, err := al.processMessage(t.Context(), msg)
	require.NoError(t, err)
	require.Equal(t, "Mock response", response)
	require.Equal(t, 2, p.calls)
	current := p.lastMessages[len(p.lastMessages)-1]
	require.Equal(t, "user", current.Role)
	require.Equal(t, []string{"data:image/png;base64,aW1hZ2U="}, current.Media)
	require.Contains(t, current.Content, "[file:"+documentPath+"]")
	history := agent.Sessions.GetHistory(msg.SessionKey)
	require.Equal(t, msg.Media, history[len(history)-2].Media, "stored attachments must keep reusable media references")
}

func TestProactiveRebuildRetainsMediaWithoutAccompanyingText(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer mb.Close()
	defer al.Close()
	p := &recordingProvider{}
	a := al.GetRegistry().GetDefaultAgent()
	a.Provider = p
	a.ContextWindow = 6000
	a.MaxTokens = 512
	a.SummarizeMessageThreshold = 1000
	a.SummarizeTokenPercent = 1000
	a.Sessions.SetHistory("agent:main:media-only-rebuild", []providers.Message{
		{Role: "user", Content: strings.Repeat("old question ", 700)},
		{Role: "assistant", Content: strings.Repeat("old answer ", 700)},
		{Role: "user", Content: strings.Repeat("recent question ", 700)},
		{Role: "assistant", Content: strings.Repeat("recent answer ", 700)},
	})
	msg := directIngressMessage("")
	msg.SessionKey = "agent:main:media-only-rebuild"
	msg.Media = []string{"data:image/png;base64,aW1hZ2U="}
	_, err := al.processMessage(t.Context(), msg)
	require.NoError(t, err)
	current := p.lastMessages[len(p.lastMessages)-1]
	require.Equal(t, "user", current.Role)
	require.Empty(t, current.Content)
	require.Equal(t, msg.Media, current.Media)
}

type contextOverheadTool struct{ echoTextTool }

func (*contextOverheadTool) Name() string { return "context_overhead" }
func (*contextOverheadTool) Description() string {
	return strings.Repeat("Tool schema instructions. ", 160)
}

type contextSystemContributor struct {
	text   string
	mirror bool
}

func (h *contextSystemContributor) BeforeLLM(
	_ context.Context,
	req *LLMHookRequest,
) (*LLMHookRequest, HookDecision, error) {
	next := req.Clone()
	next.Messages[0].SystemParts = append(
		next.Messages[0].SystemParts,
		providers.ContentBlock{Type: "text", Text: h.text},
	)
	if h.mirror {
		next.Messages[0].Content += "\n" + h.text
	}
	return next, HookDecision{Action: HookActionModify}, nil
}

func (*contextSystemContributor) AfterLLM(
	_ context.Context,
	response *LLMHookResponse,
) (*LLMHookResponse, HookDecision, error) {
	return response, HookDecision{Action: HookActionContinue}, nil
}

var _ LLMInterceptor = (*contextSystemContributor)(nil)

func TestFinalRequestBudgetsContributorsWithoutDoubleCounting(t *testing.T) {
	for _, mirror := range []bool{false, true} {
		t.Run(fmt.Sprintf("mirroredContent=%t", mirror), func(t *testing.T) {
			al, _, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer mb.Close()
			defer al.Close()
			p := &recordingProvider{}
			a := al.GetRegistry().GetDefaultAgent()
			a.Provider = p
			a.ContextWindow = 6000
			a.MaxTokens = 512
			a.SummarizeMessageThreshold = 1000
			a.SummarizeTokenPercent = 1000
			a.Sessions.SetHistory("agent:main:contributor-budget", []providers.Message{
				{Role: "user", Content: "OLDEST_QUESTION " + strings.Repeat("x", 1000)},
				{Role: "assistant", Content: strings.Repeat("y", 1000)},
				{Role: "user", Content: "MIDDLE_QUESTION " + strings.Repeat("x", 1000)},
				{Role: "assistant", Content: strings.Repeat("y", 1000)},
				{Role: "user", Content: "RECENT_QUESTION " + strings.Repeat("x", 1000)},
				{Role: "assistant", Content: strings.Repeat("y", 1000)},
			})
			contributor := strings.Repeat("SYSTEM_CONTRIBUTOR ", 200)
			require.NoError(
				t,
				al.MountHook(NamedHook("context-contributor", &contextSystemContributor{contributor, mirror})),
			)
			msg := directIngressMessage("CURRENT_MESSAGE")
			msg.SessionKey = "agent:main:contributor-budget"
			msg.Media = []string{"data:image/png;base64,aW1hZ2U="}
			response, err := al.processMessage(t.Context(), msg)
			require.NoError(t, err)
			require.Equal(t, "Mock response", response)
			input := conversationInput(p.lastMessages)
			require.NotContains(t, input, "OLDEST_QUESTION")
			require.Contains(t, input, "MIDDLE_QUESTION", "mirrored system content must not be charged twice")
			require.Contains(t, input, "RECENT_QUESTION")
			current := p.lastMessages[len(p.lastMessages)-1]
			require.Equal(t, "CURRENT_MESSAGE", current.Content)
			require.Equal(t, msg.Media, current.Media)
			parts := p.lastMessages[0].SystemParts
			require.Equal(t, contributor, parts[len(parts)-1].Text)
		})
	}
}

func TestAsyncSummaryBudgetsSystemToolsAndOutputReserve(t *testing.T) {
	al, mb, p := startIngressLoop(t)
	a := al.GetRegistry().GetDefaultAgent()
	a.ContextWindow = 17000
	a.MaxTokens = 6000
	a.SummarizeMessageThreshold = 1000
	a.SummarizeTokenPercent = 75
	require.NoError(t, os.WriteFile(
		filepath.Join(a.Workspace, "AGENTS.md"),
		[]byte(strings.Repeat("Workspace instructions. ", 420)),
		0o600,
	))
	al.RegisterTool(&contextOverheadTool{})
	a.Sessions.SetHistory("agent:main:full-request-summary", []providers.Message{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
		{Role: "assistant", Content: "second answer"},
		{Role: "user", Content: "third question"},
		{Role: "assistant", Content: "third answer"},
	})
	sub := al.SubscribeEvents(32)
	defer al.UnsubscribeEvents(sub.ID)
	msg := directIngressMessage("continue")
	msg.SessionKey = "agent:main:full-request-summary"
	publishIncoming(t, mb, msg)
	turn := inboundRequest(t, p)
	require.Contains(t, turn.messages[0].Content, "Workspace instructions.")
	close(turn.release)
	_ = finalOutbound(t, mb)
	summary := inboundRequest(t, p)
	require.Contains(t, summary.messages[0].Content, "first question")
	close(summary.release)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-sub.C:
			if event.Kind == EventKindSessionSummarize {
				require.NotEmpty(t, a.Sessions.GetSummary(msg.SessionKey))
				return
			}
		case <-deadline.C:
			t.Fatal("full-request summary did not complete")
		}
	}
}

type contextResultTool struct {
	recordingActionTool
	result string
}

func (t *contextResultTool) Execute(ctx context.Context, args map[string]any) *tools.ToolResult {
	result := t.recordingActionTool.Execute(ctx, args)
	result.ForLLM = t.result
	return result
}

func TestFinalRequestBudgetsToolResultsWithoutReplayingActions(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversizedActiveTurn=%t", oversized), func(t *testing.T) {
			al, _, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer mb.Close()
			defer al.Close()
			resultSize := 6000
			if oversized {
				resultSize = 30000
			}
			tool := &contextResultTool{result: strings.Repeat("r", resultSize)}
			al.RegisterTool(tool)
			p := &retryScriptProvider{
				responses: map[int]*providers.LLMResponse{
					0: {
						ToolCalls: []providers.ToolCall{
							{ID: "action", Name: "mock_custom", Arguments: map[string]any{"task": "action"}},
						},
					},
				},
			}
			a := al.GetRegistry().GetDefaultAgent()
			a.Provider = p
			a.ContextWindow = 6000
			a.MaxTokens = 512
			a.SummarizeMessageThreshold = 1000
			a.SummarizeTokenPercent = 1000
			a.Sessions.SetHistory("agent:main:tool-result-budget", []providers.Message{
				{Role: "user", Content: "OLDEST_QUESTION " + strings.Repeat("x", 1000)},
				{Role: "assistant", Content: strings.Repeat("y", 1000)},
				{Role: "user", Content: "RECENT_QUESTION " + strings.Repeat("x", 1000)},
				{Role: "assistant", Content: strings.Repeat("y", 1000)},
			})
			msg := directIngressMessage("Perform the action once")
			msg.SessionKey = "agent:main:tool-result-budget"
			msg.Media = []string{"data:image/png;base64,aW1hZ2U="}
			response, err := al.processMessage(t.Context(), msg)
			require.Equal(t, 1, tool.executions)
			if oversized {
				require.ErrorContains(t, err, "assembled request exceeds configured context window")
				require.Equal(t, 1, p.calls, "an oversized active turn must stop before another provider call")
				return
			}
			require.NoError(t, err)
			require.Equal(t, "recovered", response)
			require.Equal(t, 2, p.calls)
			input := conversationInput(p.messages[1])
			require.NotContains(t, input, "OLDEST_QUESTION")
			require.Contains(t, input, "RECENT_QUESTION")
			require.Contains(t, input, "Perform the action once")
			result := p.messages[1][len(p.messages[1])-1]
			require.Equal(t, "tool", result.Role)
			require.Equal(t, "action", result.ToolCallID)
			require.Equal(t, tool.result, result.Content)
		})
	}
}

func TestContextRetryKeepsCompletedToolResultInActiveTurn(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer mb.Close()
	defer al.Close()
	tool := &contextResultTool{result: "completed action details"}
	al.RegisterTool(tool)
	p := &retryScriptProvider{
		failures: []error{nil, fmt.Errorf("context window exceeded")},
		responses: map[int]*providers.LLMResponse{
			0: {
				ToolCalls: []providers.ToolCall{
					{ID: "action", Name: "mock_custom", Arguments: map[string]any{"task": "action"}},
				},
			},
		},
	}
	a := al.GetRegistry().GetDefaultAgent()
	a.Provider = p
	a.ContextWindow = 100000
	msg := directIngressMessage("Perform the action once")
	msg.Media = []string{"data:image/png;base64,aW1hZ2U="}
	response, err := al.processMessage(t.Context(), msg)
	require.NoError(t, err)
	require.Equal(t, "recovered", response)
	require.Equal(t, 1, tool.executions)
	require.Equal(t, 3, p.calls)
	retry := p.messages[2]
	require.Equal(t, "tool", retry[len(retry)-1].Role)
	require.Equal(t, "action", retry[len(retry)-1].ToolCallID)
	require.Equal(t, tool.result, retry[len(retry)-1].Content)
	require.Equal(t, msg.Media, retry[1].Media)
}
