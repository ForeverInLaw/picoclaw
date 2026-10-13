package telegram

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/channels"
)

type telegramReplyKey struct {
	chatID   int64
	threadID int
	replyTo  string
	source   [32]byte
}

type telegramReplyPart struct {
	content string
	plain   bool
}

type telegramReplyDelivery struct {
	parts          []telegramReplyPart
	next           int
	editID         int
	firstID        int
	deletedPreview int
	terminalErr    error
	createdAt      time.Time
	sourceBytes    int
}

// Finalization and ordinary manager delivery share the same acknowledged cursor.
// Only edits are retried here. A rejected new send can resume at its current
// part; an unacknowledged transport failure has an unknown outcome and fails
// closed instead of risking a duplicate message.
func (c *TelegramChannel) deliverReply(
	ctx context.Context,
	chatID int64,
	threadID int,
	replyTo, content string,
	editID int,
) (int, *telegramReplyDelivery, error) {
	content = normalizeRichMarkdown(content)
	if strings.TrimSpace(content) == "" {
		return 0, nil, nil
	}
	key := telegramReplyKey{
		chatID:   chatID,
		threadID: threadID,
		replyTo:  replyTo,
		source:   sha256.Sum256([]byte(content)),
	}
	c.deliveryMu.Lock()
	defer c.deliveryMu.Unlock()
	if c.pendingReplies == nil {
		c.pendingReplies = make(map[telegramReplyKey]*telegramReplyDelivery)
	}
	delivery := c.pendingReplies[key]
	// A failed placeholder edit reaches Send through the manager without its
	// placeholder ID. Preserve its progress when that same answer falls through.
	if delivery == nil && editID == 0 {
		for candidate, pending := range c.pendingReplies {
			if candidate.chatID == chatID && candidate.threadID == threadID && candidate.source == key.source &&
				strings.HasPrefix(candidate.replyTo, "edit:") {
				key = candidate
				delivery = pending
				break
			}
		}
	}
	if delivery == nil {
		var retainedBytes int
		for candidate, pending := range c.pendingReplies {
			// Manager recovery lasts seconds; retain failures longer than its complete
			// retry/typing lifecycle, with bounded count and source bytes.
			if time.Since(pending.createdAt) > 10*time.Minute {
				delete(c.pendingReplies, candidate)
				continue
			}
			retainedBytes += pending.sourceBytes
		}
		if len(c.pendingReplies) >= 64 || retainedBytes+len(content) > 64<<20 {
			return 0, nil, fmt.Errorf("pending Telegram delivery capacity exceeded: %w", channels.ErrSendFailed)
		}
		delivery = &telegramReplyDelivery{editID: editID, createdAt: time.Now(), sourceBytes: len(content)}
		for _, chunk := range channels.SplitMessage(content, telegramRichMessageLimit) {
			delivery.parts = append(delivery.parts, telegramReplyPart{content: chunk})
		}
		c.pendingReplies[key] = delivery
	}
	if delivery.terminalErr != nil {
		return delivery.firstID, delivery, delivery.terminalErr
	}
	err := delivery.resume(ctx, c, chatID, threadID, replyTo)
	if err == nil {
		delete(c.pendingReplies, key)
		if delivery.deletedPreview != 0 {
			_ = c.bot.DeleteMessage(
				ctx,
				&telego.DeleteMessageParams{ChatID: tu.ID(chatID), MessageID: delivery.deletedPreview},
			)
		}
	}
	return delivery.firstID, delivery, err
}

func (d *telegramReplyDelivery) usePlainParts() {
	chunks := channels.SplitMessage(d.parts[d.next].content, telegramInlineMessageLimit)
	replacements := make([]telegramReplyPart, 0, len(chunks))
	for _, chunk := range chunks {
		replacements = append(replacements, telegramReplyPart{content: chunk, plain: true})
	}
	parts := append([]telegramReplyPart(nil), d.parts[:d.next]...)
	parts = append(parts, replacements...)
	d.parts = append(parts, d.parts[d.next+1:]...)
}

func (d *telegramReplyDelivery) resume(
	ctx context.Context,
	c *TelegramChannel,
	chatID int64,
	threadID int,
	replyTo string,
) error {
	for d.next < len(d.parts) {
		if err := ctx.Err(); err != nil {
			return err
		}
		part := d.parts[d.next]
		editing := d.next == 0 && d.editID != 0
		var message *telego.Message
		var err error
		if editing {
			err = safeFinalEdit(ctx, func() error {
				params := &telego.EditMessageTextParams{ChatID: tu.ID(chatID), MessageID: d.editID}
				if part.plain {
					params.Text = part.content
				} else {
					rich := tu.RichMessageMarkdown(part.content)
					params.RichMessage = &rich
				}
				_, editErr := c.bot.EditMessageText(ctx, params)
				if isTelegramMessageNotModified(editErr) {
					return nil
				}
				return telegramDeliveryError(editErr)
			})
			if unavailablePreview(err) {
				d.deletedPreview = d.editID
				d.editID = 0
				continue
			}
		} else {
			var reply *telego.ReplyParameters
			if d.next == 0 {
				reply = replyParameters(replyTo)
			}
			if part.plain {
				params := tu.Message(tu.ID(chatID), part.content)
				params.MessageThreadID = threadID
				params.ReplyParameters = reply
				message, err = c.bot.SendMessage(ctx, params)
			} else {
				message, err = c.bot.SendRichMessage(
					ctx,
					&telego.SendRichMessageParams{
						ChatID:          tu.ID(chatID),
						MessageThreadID: threadID,
						ReplyParameters: reply,
						RichMessage:     tu.RichMessageMarkdown(part.content),
					},
				)
			}
		}
		if err != nil {
			if !part.plain && isTelegramFormattingError(err) {
				d.usePlainParts()
				continue
			}
			if !editing {
				var apiError *ta.Error
				if !errors.As(err, &apiError) {
					d.terminalErr = fmt.Errorf(
						"Telegram new-message delivery outcome is unknown; partial reply retained: %w: %w",
						channels.ErrSendFailed,
						err,
					)
					return d.terminalErr
				}
			}
			classified := telegramDeliveryError(err)
			if errors.Is(classified, channels.ErrSendFailed) {
				d.terminalErr = classified
			}
			return classified
		}
		if d.firstID == 0 {
			if editing {
				d.firstID = d.editID
			} else if message != nil {
				d.firstID = message.MessageID
			}
		}
		d.next++
	}
	return nil
}

func unavailablePreview(err error) bool {
	var apiError *ta.Error
	if !errors.As(err, &apiError) || apiError.ErrorCode != 400 {
		return false
	}
	description := strings.ToLower(apiError.Description)
	return strings.Contains(description, "message to edit not found") ||
		strings.Contains(description, "message can't be edited") ||
		strings.Contains(description, "message cannot be edited")
}
