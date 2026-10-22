package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func (al *AgentLoop) toolProgressEnabled(ts *turnState) bool {
	if ts.opts.DisableToolFeedback || ts.channel == "" || constants.IsInternalChannel(ts.channel) {
		return false
	}
	if al.cfg.IsChannelType(ts.channel, "telegram") {
		return !strings.HasPrefix(strings.TrimSpace(ts.chatID), "inline:")
	}
	if al.webActivityEnabled(ts) {
		return true
	}
	return al.cfg.Agents.Defaults.ShouldSendToolFeedback(ts.opts.PeerKind, ts.opts.SenderID)
}

func (al *AgentLoop) webActivityEnabled(ts *turnState) bool {
	if ts.opts.NoHistory || constants.IsInternalChannel(ts.channel) {
		return false
	}
	if al.channelManager != nil {
		if channel, ok := al.channelManager.GetChannel(ts.channel); ok {
			web, ok := channel.(channels.ReasoningProgressCapable)
			return ok && web.SupportsReasoningProgress()
		}
	}
	return al.cfg.IsChannelType(ts.channel, "pico")
}

func (al *AgentLoop) publishWebReasoning(
	ctx context.Context,
	ts *turnState,
	actionID string,
	state bus.ProgressState,
	content string,
) {
	if content == "" || !al.webActivityEnabled(ts) {
		return
	}
	content = al.cfg.FilterSensitiveData(content)
	al.publishProgress(ctx, ts, &bus.ProgressMessage{
		RequestID: ts.turnID, ActionID: actionID, Kind: bus.ProgressReasoning,
		State: state, Result: content,
	}, "")
}

func (al *AgentLoop) publishProgress(
	ctx context.Context,
	ts *turnState,
	progress *bus.ProgressMessage,
	content string,
) {
	providers.WithoutModelInactivity(ctx, func() {
		if al.bus == nil || constants.IsInternalChannel(ts.channel) {
			return
		}
		pubCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		msg := bus.OutboundMessage{
			Channel:          ts.channel,
			ChatID:           ts.chatID,
			ReplyToMessageID: ts.opts.ReplyToMessageID,
			Content:          content,
			Progress:         progress,
		}
		if al.cfg.IsChannelType(ts.channel, "telegram") && progress.Kind == bus.ProgressTool &&
			al.channelManager != nil {
			// Complete activity delivery before the next answer starts streaming.
			_ = al.channelManager.SendMessage(pubCtx, msg)
			return
		}
		_ = al.bus.PublishOutbound(pubCtx, msg)
	})
}

func (al *AgentLoop) publishToolProgress(
	ctx context.Context,
	ts *turnState,
	actionID, tool string,
	args map[string]any,
	result *tools.ToolResult,
) {
	if tool == "message" {
		return
	}
	progress := &bus.ProgressMessage{
		RequestID: ts.turnID,
		ActionID:  actionID,
		Kind:      bus.ProgressTool,
		State:     bus.ProgressStarted,
		Tool:      tool,
		Arguments: cloneEventArguments(args),
	}
	// Filter explicitly public payloads before transport. LLM-only output stays private.
	encoded, _ := json.Marshal(progress.Arguments)
	_ = json.Unmarshal([]byte(al.cfg.FilterSensitiveData(string(encoded))), &progress.Arguments)
	content := fmt.Sprintf("%s: %s", tool, al.cfg.FilterSensitiveData(string(encoded)))
	if result != nil {
		progress.State = bus.ProgressCompleted
		progress.Result = result.ForActivity
		if progress.Result == "" && !result.Silent {
			progress.Result = result.ForUser
		}
		if progress.Result == "" {
			progress.Result = "Done."
		}
		if result.IsError || result.Err != nil {
			progress.State = bus.ProgressFailed
			if progress.Result == "Done." {
				progress.Result = "Tool failed."
			}
			progress.Error = progress.Result
		}
		progress.Result = al.cfg.FilterSensitiveData(progress.Result)
		progress.Error = al.cfg.FilterSensitiveData(progress.Error)
		content = fmt.Sprintf("%s: %s", tool, progress.Result)
		if progress.Error != "" {
			content = fmt.Sprintf("%s failed: %s", tool, progress.Error)
		}
	}
	al.publishProgress(ctx, ts, progress, content)
}

func (al *AgentLoop) normalizeToolResult(result *tools.ToolResult) *tools.ToolResult {
	if result == nil {
		return nil
	}
	clone := *result
	clone.ForLLM = al.cfg.FilterSensitiveData(clone.ForLLM)
	clone.ForUser = al.cfg.FilterSensitiveData(clone.ForUser)
	clone.ForActivity = al.cfg.FilterSensitiveData(clone.ForActivity)
	return &clone
}
