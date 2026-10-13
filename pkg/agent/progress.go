package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/constants"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func (al *AgentLoop) toolProgressEnabled(ts *turnState) bool {
	if ts.opts.DisableToolFeedback || ts.channel == "" || constants.IsInternalChannel(ts.channel) {
		return false
	}
	if ts.channel == "telegram" {
		return !strings.HasPrefix(strings.TrimSpace(ts.chatID), "inline:")
	}
	return al.cfg.Agents.Defaults.ShouldSendToolFeedback(ts.opts.PeerKind, ts.opts.SenderID)
}

func (al *AgentLoop) publishProgress(ctx context.Context, ts *turnState, progress *bus.ProgressMessage, content string) {
	if al.bus == nil || constants.IsInternalChannel(ts.channel) {
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{Channel: ts.channel, ChatID: ts.chatID, ReplyToMessageID: ts.opts.ReplyToMessageID, Content: content, Progress: progress})
}

func (al *AgentLoop) publishToolProgress(ctx context.Context, ts *turnState, actionID, tool string, args map[string]any, result *tools.ToolResult) {
	progress := &bus.ProgressMessage{RequestID: ts.turnID, ActionID: actionID, Kind: bus.ProgressTool, State: bus.ProgressStarted, Tool: tool, Arguments: cloneEventArguments(args)}
	// Filter payloads before transport, including arguments and silent tool output.
	encoded, _ := json.Marshal(progress.Arguments)
	_ = json.Unmarshal([]byte(al.cfg.FilterSensitiveData(string(encoded))), &progress.Arguments)
	content := fmt.Sprintf("%s: %s", tool, al.cfg.FilterSensitiveData(string(encoded)))
	if result != nil {
		progress.State = bus.ProgressCompleted
		progress.Result = result.ForUser
		if progress.Result == "" {
			progress.Result = result.ForLLM
		}
		if result.IsError || result.Err != nil {
			progress.State = bus.ProgressFailed
			progress.Error = progress.Result
			if result.Err != nil {
				progress.Error = result.Err.Error()
			}
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
