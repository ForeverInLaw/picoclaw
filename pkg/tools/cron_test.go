package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
)

type stubJobExecutor struct {
	response string
	err      error
	lastReq  ScheduledRequest
}

func (s *stubJobExecutor) ProcessScheduled(ctx context.Context, req ScheduledRequest) (string, error) {
	s.lastReq = req
	return s.response, s.err
}

func newTestCronToolWithConfig(t *testing.T, cfg *config.Config) *CronTool {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "cron.json")
	cronService := cron.NewCronService(storePath, nil)
	msgBus := bus.NewMessageBus()
	tool, err := NewCronTool(cronService, nil, msgBus, t.TempDir(), true, 0, cfg)
	if err != nil {
		t.Fatalf("NewCronTool() error: %v", err)
	}
	return tool
}

func newTestCronTool(t *testing.T) *CronTool {
	t.Helper()
	return newTestCronToolWithConfig(t, config.DefaultConfig())
}

// TestCronTool_CommandBlockedFromRemoteChannel verifies command scheduling is restricted to internal channels
func TestCronTool_CommandBlockedFromRemoteChannel(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "telegram", "chat-1")
	result := tool.Execute(ctx, map[string]any{
		"action":          "add",
		"message":         "check disk",
		"command":         "df -h",
		"command_confirm": true,
		"at_seconds":      float64(60),
	})

	if !result.IsError {
		t.Fatal("expected command scheduling to be blocked from remote channel")
	}
	if !strings.Contains(result.ForLLM, "restricted to internal channels") {
		t.Errorf("expected 'restricted to internal channels', got: %s", result.ForLLM)
	}
}

func TestCronTool_CommandDoesNotRequireConfirmByDefault(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	result := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "check disk",
		"command":    "df -h",
		"at_seconds": float64(60),
	})

	if result.IsError {
		t.Fatalf("expected command scheduling without confirm to succeed by default, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Cron job added") {
		t.Errorf("expected 'Cron job added', got: %s", result.ForLLM)
	}
}

func TestCronTool_CommandRequiresConfirmWhenAllowCommandDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.AllowCommand = false

	tool := newTestCronToolWithConfig(t, cfg)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	result := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "check disk",
		"command":    "df -h",
		"at_seconds": float64(60),
	})

	if !result.IsError {
		t.Fatal("expected command scheduling to require confirm when allow_command is disabled")
	}
	if !strings.Contains(result.ForLLM, "command_confirm=true") {
		t.Errorf("expected command_confirm requirement message, got: %s", result.ForLLM)
	}
}

func TestCronTool_CommandAllowedWithConfirmWhenAllowCommandDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Cron.AllowCommand = false

	tool := newTestCronToolWithConfig(t, cfg)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	result := tool.Execute(ctx, map[string]any{
		"action":          "add",
		"message":         "check disk",
		"command":         "df -h",
		"command_confirm": true,
		"at_seconds":      float64(60),
	})

	if result.IsError {
		t.Fatalf(
			"expected command scheduling with confirm to succeed when allow_command is disabled, got: %s",
			result.ForLLM,
		)
	}
	if !strings.Contains(result.ForLLM, "Cron job added") {
		t.Errorf("expected 'Cron job added', got: %s", result.ForLLM)
	}
}

func TestCronTool_CommandBlockedWhenExecDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Enabled = false

	tool := newTestCronToolWithConfig(t, cfg)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	result := tool.Execute(ctx, map[string]any{
		"action":          "add",
		"message":         "check disk",
		"command":         "df -h",
		"command_confirm": true,
		"at_seconds":      float64(60),
	})

	if !result.IsError {
		t.Fatal("expected command scheduling to be blocked when exec is disabled")
	}
	if !strings.Contains(result.ForLLM, "command execution is disabled") {
		t.Errorf("expected exec disabled message, got: %s", result.ForLLM)
	}
}

// TestCronTool_CommandAllowedFromInternalChannel verifies command scheduling works from internal channels
func TestCronTool_CommandAllowedFromInternalChannel(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "cli", "direct")
	result := tool.Execute(ctx, map[string]any{
		"action":          "add",
		"message":         "check disk",
		"command":         "df -h",
		"command_confirm": true,
		"at_seconds":      float64(60),
	})

	if result.IsError {
		t.Fatalf("expected command scheduling to succeed from internal channel, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Cron job added") {
		t.Errorf("expected 'Cron job added', got: %s", result.ForLLM)
	}
}

// TestCronTool_AddJobRequiresSessionContext verifies fail-closed when channel/chatID missing
func TestCronTool_AddJobRequiresSessionContext(t *testing.T) {
	tool := newTestCronTool(t)
	result := tool.Execute(context.Background(), map[string]any{
		"action":     "add",
		"message":    "reminder",
		"at_seconds": float64(60),
	})

	if !result.IsError {
		t.Fatal("expected error when session context is missing")
	}
	if !strings.Contains(result.ForLLM, "no session context") {
		t.Errorf("expected 'no session context' message, got: %s", result.ForLLM)
	}
}

// TestCronTool_NonCommandJobAllowedFromRemoteChannel verifies regular reminders work from any channel
func TestCronTool_NonCommandJobAllowedFromRemoteChannel(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "telegram", "chat-1")
	result := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "time to stretch",
		"at_seconds": float64(600),
	})

	if result.IsError {
		t.Fatalf("expected non-command reminder to succeed from remote channel, got: %s", result.ForLLM)
	}
}

func TestCronTool_NonCommandJobDefaultsDeliverToTrue(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolContext(context.Background(), "telegram", "chat-1")
	result := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "send me a poem",
		"at_seconds": float64(600),
	})

	if result.IsError {
		t.Fatalf("expected non-command reminder to succeed, got: %s", result.ForLLM)
	}

	jobs := tool.cronService.ListJobs(false)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if !jobs[0].Payload.Deliver {
		t.Fatal("expected deliver=true by default for non-command jobs")
	}
}

func TestCronTool_AddJobPersistsRequesterIdentity(t *testing.T) {
	tool := newTestCronTool(t)
	ctx := WithToolSender(
		WithToolContext(context.Background(), "telegram", "group-1"),
		bus.SenderInfo{
			Platform:    "telegram",
			PlatformID:  "6669548787",
			CanonicalID: "telegram:6669548787",
			Username:    "nevermore",
			DisplayName: "Сер",
		},
	)

	result := tool.Execute(ctx, map[string]any{
		"action":     "add",
		"message":    "⏰ Напоминание!",
		"at_seconds": float64(60),
	})
	if result.IsError {
		t.Fatalf("expected reminder scheduling to succeed, got: %s", result.ForLLM)
	}

	jobs := tool.cronService.ListJobs(false)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}

	payload := jobs[0].Payload
	if payload.RequesterCanonicalID != "telegram:6669548787" {
		t.Fatalf("requester canonical id = %q", payload.RequesterCanonicalID)
	}
	if payload.RequesterPlatformID != "6669548787" {
		t.Fatalf("requester platform id = %q", payload.RequesterPlatformID)
	}
	if payload.RequesterUsername != "nevermore" {
		t.Fatalf("requester username = %q", payload.RequesterUsername)
	}
	if payload.RequesterDisplayName != "Сер" {
		t.Fatalf("requester display name = %q", payload.RequesterDisplayName)
	}
}

func TestCronTool_ExecuteJobPublishesErrorWhenExecDisabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.Exec.Enabled = false

	tool := newTestCronToolWithConfig(t, cfg)
	job := &cron.CronJob{}
	job.Payload.Channel = "cli"
	job.Payload.To = "direct"
	job.Payload.Command = "df -h"

	if got := tool.ExecuteJob(context.Background(), job); got != "ok" {
		t.Fatalf("ExecuteJob() = %q, want ok", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var (
		msg bus.OutboundMessage
		ok  bool
	)
	select {
	case msg, ok = <-tool.msgBus.OutboundChan():
	case <-ctx.Done():
		t.Fatal("expected outbound message")
	}
	if !ok {
		t.Fatal("expected outbound channel to remain open")
	}
	if !strings.Contains(msg.Content, "command execution is disabled") {
		t.Fatalf("expected exec disabled message, got: %s", msg.Content)
	}
}

func TestCronTool_ExecuteJobPublishesAgentResponseWhenDeliverDisabled(t *testing.T) {
	tool := newTestCronTool(t)
	executor := &stubJobExecutor{response: "reminder fired"}
	tool.executor = executor

	job := &cron.CronJob{}
	job.Payload.Channel = "telegram"
	job.Payload.To = "chat-1"
	job.Payload.Message = "⏰ reminder"
	job.Payload.Deliver = false
	job.Payload.RequesterCanonicalID = "telegram:6669548787"
	job.Payload.RequesterPlatformID = "6669548787"
	job.Payload.RequesterDisplayName = "Сер"
	job.Payload.RequesterUsername = "nevermore"

	if got := tool.ExecuteJob(context.Background(), job); got != "ok" {
		t.Fatalf("ExecuteJob() = %q, want ok", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var (
		msg bus.OutboundMessage
		ok  bool
	)
	select {
	case msg, ok = <-tool.msgBus.OutboundChan():
	case <-ctx.Done():
		t.Fatal("expected outbound message")
	}
	if !ok {
		t.Fatal("expected outbound channel to remain open")
	}
	if msg.Channel != "telegram" || msg.ChatID != "chat-1" {
		t.Fatalf("unexpected outbound target: %s:%s", msg.Channel, msg.ChatID)
	}
	if msg.Content != "reminder fired" {
		t.Fatalf("unexpected outbound content: %s", msg.Content)
	}
	if executor.lastReq.Sender.CanonicalID != "telegram:6669548787" {
		t.Fatalf("unexpected requester sender id: %s", executor.lastReq.Sender.CanonicalID)
	}
	if !strings.Contains(executor.lastReq.Content, "requested_by: Сер") {
		t.Fatalf("expected requester label in scheduled content, got: %s", executor.lastReq.Content)
	}
}

func TestCronTool_ExecuteJobPublishesMentionForTelegramReminder(t *testing.T) {
	tool := newTestCronTool(t)

	job := &cron.CronJob{}
	job.Payload.Channel = "telegram"
	job.Payload.To = "group-1"
	job.Payload.Message = "⏰ Напоминание!"
	job.Payload.Deliver = true
	job.Payload.RequesterPlatformID = "6669548787"
	job.Payload.RequesterDisplayName = "Сер"

	if got := tool.ExecuteJob(context.Background(), job); got != "ok" {
		t.Fatalf("ExecuteJob() = %q, want ok", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	select {
	case msg := <-tool.msgBus.OutboundChan():
		want := "[Сер](tg://user?id=6669548787), ⏰ Напоминание!"
		if msg.Content != want {
			t.Fatalf("content=%q want=%q", msg.Content, want)
		}
	case <-ctx.Done():
		t.Fatal("expected outbound reminder message")
	}
}
