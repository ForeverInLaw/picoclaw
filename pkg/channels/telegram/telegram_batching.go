package telegram

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/identity"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/utils"
)

type telegramInboundCandidate struct {
	peer             bus.Peer
	messageID        string
	senderID         string
	chatID           string
	content          string
	media            []string
	metadata         map[string]string
	sender           bus.SenderInfo
	batchKey         string
	batchEligible    bool
	observeOnly      bool
	replyToMessageID string
}

type telegramInboundBatch struct {
	peer             bus.Peer
	messageID        string
	senderID         string
	chatID           string
	metadata         map[string]string
	sender           bus.SenderInfo
	observeOnly      bool
	replyToMessageID string
	contents         []string
	media            []string
	messageIDs       []string
	startedAt        time.Time
	endedAt          time.Time
	timer            *time.Timer
}

func (c *TelegramChannel) buildInboundCandidate(
	ctx context.Context,
	message *telego.Message,
) (*telegramInboundCandidate, error) {
	if message == nil {
		return nil, fmt.Errorf("message is nil")
	}

	user := message.From
	if user == nil {
		return nil, fmt.Errorf("message sender (user) is nil")
	}

	platformID := fmt.Sprintf("%d", user.ID)
	senderLabel := c.resolveParticipantLabel(user)
	sender := bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  platformID,
		CanonicalID: identity.BuildCanonicalID("telegram", platformID),
		Username:    user.Username,
		DisplayName: senderLabel,
	}

	if !c.IsAllowedSender(sender) {
		logger.DebugCF("telegram", "Message rejected by allowlist", map[string]any{
			"user_id": platformID,
		})
		return nil, nil
	}

	chatID := message.Chat.ID
	c.chatIDs[platformID] = chatID

	content := ""
	mediaPaths := []string{}

	chatIDStr := fmt.Sprintf("%d", chatID)
	messageIDStr := fmt.Sprintf("%d", message.MessageID)
	scope := channels.BuildMediaScope("telegram", chatIDStr, messageIDStr)

	storeMedia := func(localPath, filename string) string {
		if store := c.GetMediaStore(); store != nil {
			ref, err := store.Store(localPath, media.MediaMeta{
				Filename: filename,
				Source:   "telegram",
			}, scope)
			if err == nil {
				return ref
			}
		}
		return localPath
	}

	if message.Text != "" {
		content += message.Text
	}
	if message.Caption != "" {
		if content != "" {
			content += "\n"
		}
		content += message.Caption
	}

	if len(message.Photo) > 0 {
		photo := message.Photo[len(message.Photo)-1]
		photoPath := c.downloadPhoto(ctx, photo.FileID)
		if photoPath != "" {
			mediaPaths = append(mediaPaths, storeMedia(photoPath, "photo.jpg"))
			if content != "" {
				content += "\n"
			}
			content += "[image: photo]"
		}
	}
	if message.Voice != nil {
		voicePath := c.downloadFile(ctx, message.Voice.FileID, ".ogg")
		if voicePath != "" {
			mediaPaths = append(mediaPaths, storeMedia(voicePath, "voice.ogg"))
			if content != "" {
				content += "\n"
			}
			content += "[voice]"
		}
	}
	if message.Audio != nil {
		audioPath := c.downloadFile(ctx, message.Audio.FileID, ".mp3")
		if audioPath != "" {
			mediaPaths = append(mediaPaths, storeMedia(audioPath, "audio.mp3"))
			if content != "" {
				content += "\n"
			}
			content += "[audio]"
		}
	}
	if message.Document != nil {
		docPath := c.downloadFile(ctx, message.Document.FileID, "")
		if docPath != "" {
			mediaPaths = append(mediaPaths, storeMedia(docPath, "document"))
			if content != "" {
				content += "\n"
			}
			content += "[file]"
		}
	}
	if content == "" {
		content = "[empty message]"
	}

	observeOnly := false
	if message.Chat.Type != "private" {
		isMentioned := c.isBotMentioned(message)
		isReplyToBot := c.isReplyToBot(message)
		isAddressedToBot := isMentioned || isReplyToBot
		if isMentioned {
			content = c.stripBotMention(content)
		}
		respond, cleaned := c.ShouldRespondInGroup(isAddressedToBot, content)
		if respond {
			content = cleaned
		} else {
			observeOnly = true
		}
	}
	content = prependQuotedTelegramReply(message, formatForwardedTelegramMessage(message, content))

	compositeChatID := fmt.Sprintf("%d", chatID)
	threadID := message.MessageThreadID
	if message.Chat.IsForum && threadID != 0 {
		compositeChatID = fmt.Sprintf("%d/%d", chatID, threadID)
	}

	logger.DebugCF("telegram", "Received message", map[string]any{
		"sender_id": sender.CanonicalID,
		"chat_id":   compositeChatID,
		"thread_id": threadID,
		"preview":   utils.Truncate(content, 50),
	})

	peerKind := "direct"
	peerID := fmt.Sprintf("%d", user.ID)
	if message.Chat.Type != "private" {
		peerKind = "group"
		peerID = compositeChatID
	}

	metadata := map[string]string{
		"user_id":      fmt.Sprintf("%d", user.ID),
		"username":     user.Username,
		"first_name":   user.FirstName,
		"sender_label": senderLabel,
		"chat_label":   strings.TrimSpace(message.Chat.Title),
		"is_group":     fmt.Sprintf("%t", message.Chat.Type != "private"),
	}
	if observeOnly {
		metadata["observe_only"] = "true"
	}
	if alias := c.resolveParticipantAlias(user); alias != "" {
		metadata["sender_alias"] = alias
	}
	replyToMessageID := ""
	if message.ReplyToMessage != nil {
		replyToMessageID = fmt.Sprintf("%d", message.ReplyToMessage.MessageID)
		metadata["reply_to_message_id"] = replyToMessageID
		if replyAuthor := message.ReplyToMessage.From; replyAuthor != nil {
			metadata["reply_to_user_id"] = fmt.Sprintf("%d", replyAuthor.ID)
			metadata["reply_to_username"] = replyAuthor.Username
			metadata["reply_to_first_name"] = replyAuthor.FirstName
			metadata["reply_to_sender_id"] = telegramCanonicalID(replyAuthor)
			metadata["reply_to_label"] = c.resolveParticipantLabel(replyAuthor)
			if alias := c.resolveParticipantAlias(replyAuthor); alias != "" {
				metadata["reply_to_alias"] = alias
			}
		}
	}

	if message.Chat.IsForum && threadID != 0 {
		metadata["parent_peer_kind"] = "topic"
		metadata["parent_peer_id"] = fmt.Sprintf("%d", threadID)
	}

	if forwardLabel := telegramForwardOriginLabel(message.ForwardOrigin); forwardLabel != "" {
		metadata["forwarded_from"] = forwardLabel
	}

	messageText, _ := telegramEntityTextAndList(message)
	batchEligible := c.batchingEnabled() &&
		len(mediaPaths) == 0 &&
		!commands.HasCommandPrefix(strings.TrimSpace(messageText))

	return &telegramInboundCandidate{
		peer:             bus.Peer{Kind: peerKind, ID: peerID},
		messageID:        messageIDStr,
		senderID:         platformID,
		chatID:           compositeChatID,
		content:          content,
		media:            mediaPaths,
		metadata:         metadata,
		sender:           sender,
		batchKey:         compositeChatID + "|" + sender.CanonicalID,
		batchEligible:    batchEligible,
		observeOnly:      observeOnly,
		replyToMessageID: replyToMessageID,
	}, nil
}

func (c *TelegramChannel) dispatchInboundCandidate(
	ctx context.Context,
	candidate telegramInboundCandidate,
) error {
	if !candidate.batchEligible {
		c.flushBatchByKey(ctx, candidate.batchKey)
		c.publishInboundCandidate(ctx, candidate)
		return nil
	}

	c.enqueueTelegramBatch(ctx, candidate)
	return nil
}

func (c *TelegramChannel) batchingEnabled() bool {
	return c.config != nil && c.config.Channels.Telegram.Batching.Enabled
}

func (c *TelegramChannel) batchingWindow() time.Duration {
	if c.config == nil {
		return 2 * time.Second
	}
	ms := c.config.Channels.Telegram.Batching.WindowMS
	if ms <= 0 {
		ms = 2000
	}
	return time.Duration(ms) * time.Millisecond
}

func (c *TelegramChannel) enqueueTelegramBatch(
	ctx context.Context,
	candidate telegramInboundCandidate,
) {
	c.batchMu.Lock()
	defer c.batchMu.Unlock()

	existing := c.batches[candidate.batchKey]
	if existing != nil && !telegramBatchCompatible(existing, candidate) {
		c.stopLocked(existing)
		delete(c.batches, candidate.batchKey)
		go c.publishAggregatedBatch(ctx, existing)
		existing = nil
	}

	if existing == nil {
		existing = &telegramInboundBatch{
			peer:             candidate.peer,
			messageID:        candidate.messageID,
			senderID:         candidate.senderID,
			chatID:           candidate.chatID,
			metadata:         cloneStringMap(candidate.metadata),
			sender:           candidate.sender,
			observeOnly:      candidate.observeOnly,
			replyToMessageID: candidate.replyToMessageID,
			startedAt:        time.Now(),
			endedAt:          time.Now(),
			contents:         []string{candidate.content},
			media:            append([]string(nil), candidate.media...),
			messageIDs:       []string{candidate.messageID},
		}
		existing.timer = time.AfterFunc(c.batchingWindow(), func() {
			c.flushBatchByKey(c.ctx, candidate.batchKey)
		})
		c.batches[candidate.batchKey] = existing
		return
	}

	existing.contents = append(existing.contents, candidate.content)
	existing.media = append(existing.media, candidate.media...)
	existing.messageIDs = append(existing.messageIDs, candidate.messageID)
	existing.endedAt = time.Now()
	if existing.timer != nil {
		existing.timer.Reset(c.batchingWindow())
	}
}

func telegramBatchCompatible(batch *telegramInboundBatch, candidate telegramInboundCandidate) bool {
	if batch == nil {
		return false
	}
	return batch.observeOnly == candidate.observeOnly &&
		batch.replyToMessageID == candidate.replyToMessageID &&
		batch.chatID == candidate.chatID &&
		batch.sender.CanonicalID == candidate.sender.CanonicalID
}

func (c *TelegramChannel) flushBatchByKey(ctx context.Context, key string) {
	c.batchMu.Lock()
	batch := c.batches[key]
	if batch != nil {
		c.stopLocked(batch)
		delete(c.batches, key)
	}
	c.batchMu.Unlock()

	if batch != nil {
		c.publishAggregatedBatch(ctx, batch)
	}
}

func (c *TelegramChannel) flushAllBatches(ctx context.Context) {
	c.batchMu.Lock()
	batches := make([]*telegramInboundBatch, 0, len(c.batches))
	for key, batch := range c.batches {
		c.stopLocked(batch)
		batches = append(batches, batch)
		delete(c.batches, key)
	}
	c.batchMu.Unlock()

	for _, batch := range batches {
		c.publishAggregatedBatch(ctx, batch)
	}
}

func (c *TelegramChannel) stopLocked(batch *telegramInboundBatch) {
	if batch != nil && batch.timer != nil {
		batch.timer.Stop()
	}
}

func (c *TelegramChannel) publishAggregatedBatch(ctx context.Context, batch *telegramInboundBatch) {
	if batch == nil {
		return
	}
	metadata := cloneStringMap(batch.metadata)
	if len(batch.messageIDs) > 0 {
		metadata["batch_message_ids"] = strings.Join(batch.messageIDs, ",")
		metadata["batch_count"] = fmt.Sprintf("%d", len(batch.messageIDs))
	}
	metadata["batch_started_at"] = batch.startedAt.UTC().Format(time.RFC3339Nano)
	metadata["batch_ended_at"] = batch.endedAt.UTC().Format(time.RFC3339Nano)

	c.publishInboundCandidate(ctx, telegramInboundCandidate{
		peer:             batch.peer,
		messageID:        batch.messageID,
		senderID:         batch.senderID,
		chatID:           batch.chatID,
		content:          strings.Join(batch.contents, "\n\n"),
		media:            append([]string(nil), batch.media...),
		metadata:         metadata,
		sender:           batch.sender,
		observeOnly:      batch.observeOnly,
		replyToMessageID: batch.replyToMessageID,
	})
}

func (c *TelegramChannel) publishInboundCandidate(ctx context.Context, candidate telegramInboundCandidate) {
	runCtx := ctx
	if runCtx == nil {
		runCtx = c.ctx
	}
	if runCtx == nil {
		runCtx = context.Background()
	}
	c.HandleMessage(
		runCtx,
		candidate.peer,
		candidate.messageID,
		candidate.senderID,
		candidate.chatID,
		candidate.content,
		candidate.media,
		candidate.metadata,
		candidate.sender,
	)
}

func formatForwardedTelegramMessage(message *telego.Message, content string) string {
	label := telegramForwardOriginLabel(nil)
	if message != nil {
		label = telegramForwardOriginLabel(message.ForwardOrigin)
	}
	content = strings.TrimSpace(content)
	if label == "" {
		return content
	}
	if content == "" {
		return fmt.Sprintf("[forwarded from %s]", label)
	}
	return fmt.Sprintf("[forwarded from %s]: %s", label, content)
}

func telegramForwardOriginLabel(origin telego.MessageOrigin) string {
	switch v := origin.(type) {
	case *telego.MessageOriginUser:
		if strings.TrimSpace(v.SenderUser.Username) != "" {
			return v.SenderUser.Username
		}
		if strings.TrimSpace(v.SenderUser.FirstName) != "" {
			return v.SenderUser.FirstName
		}
		return fmt.Sprintf("user:%d", v.SenderUser.ID)
	case *telego.MessageOriginHiddenUser:
		return strings.TrimSpace(v.SenderUserName)
	case *telego.MessageOriginChat:
		if strings.TrimSpace(v.SenderChat.Title) != "" {
			return v.SenderChat.Title
		}
		if strings.TrimSpace(v.AuthorSignature) != "" {
			return v.AuthorSignature
		}
		return "chat"
	case *telego.MessageOriginChannel:
		if strings.TrimSpace(v.Chat.Title) != "" {
			return v.Chat.Title
		}
		if strings.TrimSpace(v.AuthorSignature) != "" {
			return v.AuthorSignature
		}
		return "channel"
	default:
		return ""
	}
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
