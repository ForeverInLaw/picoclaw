package email

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	minPollIntervalMinutes = 1
	defaultPollInterval    = 30 * time.Minute
	retryFloor             = 15 * time.Minute
	retentionPeriod        = 30 * 24 * time.Hour
)

type trackedEmail struct {
	UID              uint32    `json:"uid"`
	ChatID           string    `json:"chat_id"`
	SenderName       string    `json:"sender_name,omitempty"`
	Subject          string    `json:"subject,omitempty"`
	Content          string    `json:"content,omitempty"`
	References       []string  `json:"references,omitempty"`
	Status           string    `json:"status"`
	LastQueuedAt     time.Time `json:"last_queued_at"`
	LastAttemptAt    time.Time `json:"last_attempt_at"`
	AnsweredAt       time.Time `json:"answered_at,omitempty"`
	OriginalDate     time.Time `json:"original_date,omitempty"`
	OriginalEnvelope string    `json:"original_envelope,omitempty"`
}

type channelState struct {
	Initialized bool                    `json:"initialized"`
	LastSeenUID uint32                  `json:"last_seen_uid"`
	Messages    map[string]trackedEmail `json:"messages"`
}

type incomingMessage struct {
	UID        uint32
	MessageID  string
	ChatID     string
	SenderName string
	Subject    string
	Content    string
	Date       time.Time
	References []string
	RawHeader  string
}

type EmailChannel struct {
	*channels.BaseChannel
	config        config.EmailConfig
	workspace     string
	pollInterval  time.Duration
	retryInterval time.Duration
	statePath     string

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	state channelState
}

func NewEmailChannel(cfg *config.Config, messageBus *bus.MessageBus) (*EmailChannel, error) {
	emailCfg := cfg.Channels.Email
	if emailCfg.Address == "" {
		return nil, fmt.Errorf("email address is required")
	}
	if emailCfg.IMAPHost == "" {
		return nil, fmt.Errorf("email IMAP host is required")
	}
	if emailCfg.SMTPHost == "" {
		return nil, fmt.Errorf("email SMTP host is required")
	}
	if emailCfg.Username == "" {
		emailCfg.Username = emailCfg.Address
	}
	if emailCfg.Folder == "" {
		emailCfg.Folder = "INBOX"
	}
	if emailCfg.IMAPPort == 0 {
		emailCfg.IMAPPort = 993
	}
	if emailCfg.SMTPPort == 0 {
		emailCfg.SMTPPort = 465
	}

	pollInterval := resolvePollInterval(emailCfg.PollInterval, cfg.Heartbeat.Interval)
	base := channels.NewBaseChannel("email", emailCfg, messageBus, emailCfg.AllowFrom,
		channels.WithReasoningChannelID(emailCfg.ReasoningChannelID),
	)

	return &EmailChannel{
		BaseChannel:   base,
		config:        emailCfg,
		workspace:     cfg.WorkspacePath(),
		pollInterval:  pollInterval,
		retryInterval: maxDuration(pollInterval, retryFloor),
		statePath:     filepath.Join(cfg.WorkspacePath(), "state", "email.json"),
		state:         channelState{Messages: map[string]trackedEmail{}},
	}, nil
}

func (c *EmailChannel) Start(ctx context.Context) error {
	if c.IsRunning() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0o700); err != nil {
		return err
	}
	if err := c.loadState(); err != nil {
		return err
	}

	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)

	go c.runLoop()
	logger.InfoCF("email", "Email channel started", map[string]any{
		"address":               c.config.Address,
		"folder":                c.config.Folder,
		"poll_interval_minutes": c.pollInterval.Minutes(),
	})
	return nil
}

func (c *EmailChannel) Stop(ctx context.Context) error {
	c.SetRunning(false)
	if c.cancel != nil {
		c.cancel()
	}
	logger.InfoC("email", "Email channel stopped")
	return nil
}

func (c *EmailChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	if strings.TrimSpace(msg.Content) == "" {
		return nil, nil
	}
	if strings.TrimSpace(msg.ChatID) == "" {
		return nil, fmt.Errorf("email recipient is empty: %w", channels.ErrSendFailed)
	}

	entry, ok := c.lookupTrackedMessage(msg.ReplyToMessageID)
	if !ok {
		logger.WarnCF("email", "Tracked email metadata not found for reply", map[string]any{
			"reply_to_message_id": msg.ReplyToMessageID,
			"recipient":           msg.ChatID,
		})
	}

	messageID, err := c.sendSMTPMessage(msg.ChatID, entry, msg)
	if err != nil {
		return nil, err
	}
	if ok {
		if err := c.markAnswered(msg.ReplyToMessageID, entry); err != nil {
			logger.WarnCF("email", "Failed to mark email answered", map[string]any{
				"message_id": msg.ReplyToMessageID,
				"error":      err.Error(),
			})
		}
	}
	if messageID == "" {
		return nil, nil
	}
	return []string{messageID}, nil
}

func (c *EmailChannel) runLoop() {
	c.pollOnce()

	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.pollOnce()
		}
	}
}

func (c *EmailChannel) pollOnce() {
	if !c.IsRunning() {
		return
	}

	if err := c.retryQueuedMessages(); err != nil {
		logger.WarnCF("email", "Queued email retry failed", map[string]any{"error": err.Error()})
	}

	messages, maxMessageUID, err := c.fetchNewMessages()
	if err != nil {
		logger.WarnCF("email", "Email poll failed", map[string]any{"error": err.Error()})
		return
	}
	if maxMessageUID > 0 {
		c.updateLastSeenUID(maxMessageUID)
	}
	if len(messages) == 0 {
		return
	}

	for _, msg := range messages {
		if err := c.queueIncomingMessage(msg); err != nil {
			logger.WarnCF("email", "Failed to queue incoming email", map[string]any{
				"message_id": msg.MessageID,
				"error":      err.Error(),
			})
			continue
		}
		if err := c.notifyInboundMessage(msg); err != nil {
			logger.WarnCF("email", "Failed to notify Telegram about inbound email", map[string]any{
				"message_id": msg.MessageID,
				"error":      err.Error(),
			})
		}
		c.publishIncoming(msg)
	}
}

func (c *EmailChannel) retryQueuedMessages() error {
	c.mu.Lock()
	var retryKeys []string
	now := time.Now()
	for key, entry := range c.state.Messages {
		if entry.Status != "queued" {
			continue
		}
		if now.Sub(entry.LastAttemptAt) >= c.retryInterval {
			retryKeys = append(retryKeys, key)
			entry.LastAttemptAt = now
			entry.LastQueuedAt = now
			c.state.Messages[key] = entry
		}
	}
	if len(retryKeys) == 0 {
		c.mu.Unlock()
		return nil
	}
	if err := c.saveStateLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	var entries []incomingMessage
	for _, key := range retryKeys {
		entry := c.state.Messages[key]
		entries = append(entries, incomingMessage{
			UID:        entry.UID,
			MessageID:  key,
			ChatID:     entry.ChatID,
			SenderName: entry.SenderName,
			Subject:    entry.Subject,
			Content:    entry.Content,
			Date:       entry.OriginalDate,
			References: slices.Clone(entry.References),
			RawHeader:  entry.OriginalEnvelope,
		})
	}
	c.mu.Unlock()

	for _, entry := range entries {
		c.publishIncoming(entry)
	}
	return nil
}

func (c *EmailChannel) publishIncoming(msg incomingMessage) {
	sender := bus.SenderInfo{
		Platform:    "email",
		PlatformID:  msg.ChatID,
		CanonicalID: "email:" + strings.ToLower(msg.ChatID),
		Username:    msg.ChatID,
		DisplayName: msg.SenderName,
	}
	c.HandleMessage(
		c.ctx,
		bus.Peer{Kind: "direct", ID: msg.ChatID},
		msg.MessageID,
		msg.ChatID,
		msg.ChatID,
		msg.Content,
		nil,
		map[string]string{
			"subject": msg.Subject,
			"from":    msg.ChatID,
		},
		sender,
	)
}
