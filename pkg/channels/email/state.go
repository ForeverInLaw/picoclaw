package email

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func (c *EmailChannel) queueIncomingMessage(msg incomingMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := msg.MessageID
	entry := trackedEmail{
		UID:              msg.UID,
		ChatID:           msg.ChatID,
		SenderName:       msg.SenderName,
		Subject:          msg.Subject,
		Content:          msg.Content,
		References:       slices.Clone(msg.References),
		Status:           "queued",
		LastQueuedAt:     time.Now(),
		LastAttemptAt:    time.Now(),
		OriginalDate:     msg.Date,
		OriginalEnvelope: msg.RawHeader,
	}
	c.state.Messages[key] = entry
	c.pruneStateLocked()
	return c.saveStateLocked()
}

func (c *EmailChannel) lookupTrackedMessage(messageID string) (trackedEmail, bool) {
	if strings.TrimSpace(messageID) == "" {
		return trackedEmail{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.state.Messages[messageID]
	return entry, ok
}

func (c *EmailChannel) markAnswered(messageID string, entry trackedEmail) error {
	if messageID == "" {
		return nil
	}
	if entry.UID > 0 {
		if err := c.markIMAPAnswered(entry.UID); err != nil {
			return err
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	tracked, ok := c.state.Messages[messageID]
	if !ok {
		return nil
	}
	tracked.Status = "answered"
	tracked.AnsweredAt = time.Now()
	tracked.LastAttemptAt = tracked.AnsweredAt
	c.state.Messages[messageID] = tracked
	c.pruneStateLocked()
	return c.saveStateLocked()
}

func (c *EmailChannel) updateLastSeenUID(uid uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if uid > c.state.LastSeenUID {
		c.state.LastSeenUID = uid
		_ = c.saveStateLocked()
	}
}

func (c *EmailChannel) loadState() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	data, err := os.ReadFile(c.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.state = channelState{Messages: map[string]trackedEmail{}}
			return nil
		}
		return err
	}
	if err := json.Unmarshal(data, &c.state); err != nil {
		return err
	}
	if c.state.Messages == nil {
		c.state.Messages = map[string]trackedEmail{}
	}
	return nil
}

func (c *EmailChannel) saveStateLocked() error {
	data, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(c.statePath, data, 0o600)
}

func (c *EmailChannel) pruneStateLocked() {
	cutoff := time.Now().Add(-retentionPeriod)
	for key, entry := range c.state.Messages {
		if entry.Status == "answered" && !entry.AnsweredAt.IsZero() && entry.AnsweredAt.Before(cutoff) {
			delete(c.state.Messages, key)
		}
	}
}
