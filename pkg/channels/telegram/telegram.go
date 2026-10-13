package telegram

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/utils"
)

var (
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+([^\n]+)`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldStar   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder  = regexp.MustCompile(`__(.+?)__`)
	reItalic     = regexp.MustCompile(`_([^_]+)_`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reListItem   = regexp.MustCompile(`^[-*]\s+`)
	reCodeBlock  = regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
)

type TelegramChannel struct {
	*channels.BaseChannel
	bot            *telego.Bot
	bh             *th.BotHandler
	config         *config.Config
	chatIDs        map[string]int64
	ctx            context.Context
	cancel         context.CancelFunc
	batchMu        sync.Mutex
	batches        map[string]*telegramInboundBatch
	progressMu     sync.Mutex
	activities     map[string]*telegramActivity
	inlineMu       sync.Mutex
	inlineReplies  map[string]*telegramInlineReplyState
	deliveryMu     sync.Mutex
	deliveryNow    func() time.Time
	pendingReplies map[telegramReplyKey]*telegramReplyDelivery

	registerFunc     func(context.Context, []commands.Definition) error
	commandRegCancel context.CancelFunc
}

// HandlesMessageSplitting keeps rich/plain fallback and inline pagination inside
// the Telegram adapter, where their different API limits are known.
func (c *TelegramChannel) HandlesMessageSplitting() bool { return true }

func NewTelegramChannel(cfg *config.Config, bus *bus.MessageBus) (*TelegramChannel, error) {
	var opts []telego.BotOption
	telegramCfg := cfg.Channels.Telegram

	if telegramCfg.Proxy != "" {
		proxyURL, parseErr := url.Parse(telegramCfg.Proxy)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", telegramCfg.Proxy, parseErr)
		}
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}))
	} else if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		// Use environment proxy if configured
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		}))
	}

	if baseURL := strings.TrimRight(strings.TrimSpace(telegramCfg.BaseURL), "/"); baseURL != "" {
		opts = append(opts, telego.WithAPIServer(baseURL))
	}
	opts = append(opts, telego.WithLogger(logger.NewLogger("telego")))

	bot, err := telego.NewBot(telegramCfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	base := channels.NewBaseChannel(
		"telegram",
		telegramCfg,
		bus,
		telegramCfg.AllowFrom,
		channels.WithMaxMessageLength(telegramRichMessageLimit),
		channels.WithGroupTrigger(telegramCfg.GroupTrigger),
		channels.WithReasoningChannelID(telegramCfg.ReasoningChannelID),
	)

	return &TelegramChannel{
		BaseChannel: base,
		bot:         bot,
		config:      cfg,
		chatIDs:     make(map[string]int64),
		batches:     make(map[string]*telegramInboundBatch),
	}, nil
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot (polling mode)...")

	c.ctx, c.cancel = context.WithCancel(ctx)

	updates, err := c.bot.UpdatesViaLongPolling(c.ctx, &telego.GetUpdatesParams{
		Timeout: 30,
		// Guest mentions require an explicit subscription, unlike ordinary messages.
		AllowedUpdates: []string{
			"message", "edited_message", "channel_post", "edited_channel_post",
			"business_connection", "business_message", "edited_business_message", "deleted_business_messages",
			"guest_message", "callback_query", "inline_query", "chosen_inline_result",
			"shipping_query", "pre_checkout_query", "my_chat_member",
		},
	})
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to start long polling: %w", err)
	}

	bh, err := th.NewBotHandler(c.bot, updates)
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to create bot handler: %w", err)
	}
	c.bh = bh

	bh.HandleMessage(func(ctx *th.Context, message telego.Message) error {
		return c.handleMessage(ctx, &message)
	}, th.AnyMessage())
	bh.HandleGuestMessage(func(ctx *th.Context, message telego.Message) error {
		return c.handleGuestMessage(ctx, message)
	})
	bh.HandleInlineQuery(func(ctx *th.Context, query telego.InlineQuery) error {
		return c.handleInlineQuery(ctx, query)
	})
	bh.HandleChosenInlineResult(func(ctx *th.Context, result telego.ChosenInlineResult) error {
		return c.handleChosenInlineResult(ctx, result)
	})
	bh.HandleCallbackQuery(func(ctx *th.Context, query telego.CallbackQuery) error {
		return c.handleInlineCallbackQuery(ctx, query)
	})

	c.SetRunning(true)
	logger.InfoCF("telegram", "Telegram bot connected", map[string]any{
		"username": c.bot.Username(),
	})

	c.startCommandRegistration(c.ctx, commands.BuiltinDefinitions())

	go func() {
		if err = bh.Start(); err != nil {
			logger.ErrorCF("telegram", "Bot handler failed", map[string]any{
				"error": err.Error(),
			})
		}
	}()

	return nil
}

func (c *TelegramChannel) Stop(ctx context.Context) error {
	logger.InfoC("telegram", "Stopping Telegram bot...")
	c.SetRunning(false)
	c.flushAllBatches(ctx)

	// Stop the bot handler
	if c.bh != nil {
		_ = c.bh.StopWithContext(ctx)
	}

	// Cancel our context (stops long polling)
	if c.cancel != nil {
		c.cancel()
	}
	if c.commandRegCancel != nil {
		c.commandRegCancel()
	}

	return nil
}

func (c *TelegramChannel) isReplyToBot(message *telego.Message) bool {
	if message == nil || message.ReplyToMessage == nil || message.ReplyToMessage.From == nil {
		return false
	}

	replyAuthor := message.ReplyToMessage.From
	if !replyAuthor.IsBot {
		return false
	}

	if c.bot == nil {
		return false
	}

	if botID := c.bot.ID(); botID != 0 && replyAuthor.ID != 0 {
		return replyAuthor.ID == botID
	}

	botUsername := c.bot.Username()
	if botUsername != "" && replyAuthor.Username != "" {
		return strings.EqualFold(replyAuthor.Username, botUsername)
	}

	return false
}

func prependQuotedTelegramReply(message *telego.Message, quotedBody, content string) string {
	if message == nil || message.ReplyToMessage == nil {
		return content
	}

	quotedBody = strings.TrimSpace(quotedBody)
	if quotedBody == "" {
		return content
	}

	author := telegramMessageAuthor(message.ReplyToMessage)
	return fmt.Sprintf("[quoted message from %s]: %s\n\n%s", author, quotedBody, content)
}

func telegramMessageAuthor(message *telego.Message) string {
	if message == nil || message.From == nil {
		return "unknown"
	}
	if strings.TrimSpace(message.From.Username) != "" {
		return message.From.Username
	}
	if strings.TrimSpace(message.From.FirstName) != "" {
		return message.From.FirstName
	}
	return "unknown"
}

func (c *TelegramChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if msg.DeliveryID != "" {
		ctx = bus.WithDeliveryID(ctx, msg.DeliveryID)
	}
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	if inlineMessageID, ok := telegramInlineMessageID(msg.ChatID); ok {
		if err := c.editInlineMessageText(ctx, inlineMessageID, msg.Content); err != nil {
			return fmt.Errorf("telegram inline send: %w", err)
		}
		return nil
	}

	chatID, threadID, err := parseTelegramChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
	}
	_, err = c.sendRichReply(ctx, chatID, threadID, msg.ReplyToMessageID, msg.Content)
	return err
}

// maxTypingDuration limits how long the typing indicator can run.
// Prevents endless typing when the LLM fails/hangs and preSend never invokes cancel.
// Matches channels.Manager's typingStopTTL (5 min) so behavior is consistent.
const maxTypingDuration = 5 * time.Minute

// StartTyping implements channels.TypingCapable.
// It sends ChatAction(typing) immediately and then repeats every 4 seconds
// (Telegram's typing indicator expires after ~5s) in a background goroutine.
// The returned stop function is idempotent and cancels the goroutine.
// The goroutine also exits automatically after maxTypingDuration if cancel is
// never called (e.g. when the LLM fails or times out without publishing).
func (c *TelegramChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return func() {}, err
	}

	action := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
	action.MessageThreadID = threadID

	// Send the first typing action immediately
	_ = c.bot.SendChatAction(ctx, action)

	typingCtx, cancel := context.WithCancel(ctx)
	// Cap lifetime so the goroutine cannot run indefinitely if cancel is never called
	maxCtx, maxCancel := context.WithTimeout(typingCtx, maxTypingDuration)
	go func() {
		defer maxCancel()
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-maxCtx.Done():
				return
			case <-ticker.C:
				a := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
				a.MessageThreadID = threadID
				_ = c.bot.SendChatAction(typingCtx, a)
			}
		}
	}()

	return cancel, nil
}

// EditMessage implements channels.MessageEditor.
func (c *TelegramChannel) EditMessage(ctx context.Context, chatID, messageID, content string) error {
	return c.EditReply(
		ctx,
		bus.OutboundMessage{ChatID: chatID, ReplyToMessageID: "edit:" + messageID, Content: content},
		messageID,
	)
}

// EditReply retains request identity when a partial placeholder delivery later
// falls through to normal manager delivery.
func (c *TelegramChannel) EditReply(ctx context.Context, msg bus.OutboundMessage, messageID string) error {
	if msg.DeliveryID != "" {
		ctx = bus.WithDeliveryID(ctx, msg.DeliveryID)
	}
	if inlineMessageID, ok := telegramInlineMessageID(msg.ChatID); ok {
		return c.editInlineMessageText(ctx, inlineMessageID, msg.Content)
	}
	cid, threadID, err := parseTelegramChatID(msg.ChatID)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	_, _, err = c.deliverReply(ctx, cid, threadID, msg.ReplyToMessageID, msg.Content, mid)
	return err
}

func isTelegramMessageNotModified(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

// DeleteMessage implements channels.MessageDeleter.
func (c *TelegramChannel) DeleteMessage(ctx context.Context, chatID string, messageID string) error {
	cid, _, err := parseTelegramChatID(chatID)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	return c.bot.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    tu.ID(cid),
		MessageID: mid,
	})
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message (e.g. "Thinking... 💭") that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *TelegramChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	phCfg := c.config.Channels.Telegram.Placeholder
	if !phCfg.Enabled {
		return "", nil
	}

	text := phCfg.Text
	if text == "" {
		text = "Thinking... 💭"
	}

	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return "", err
	}

	phMsg := tu.Message(tu.ID(cid), text)
	phMsg.MessageThreadID = threadID
	phMsg.ReplyParameters = replyParameters(bus.StreamReplyTo(ctx))
	pMsg, err := c.bot.SendMessage(ctx, phMsg)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%d", pMsg.MessageID), nil
}

// SendMedia implements the channels.MediaSender interface.
func (c *TelegramChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	chatID, threadID, err := parseTelegramChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
	}

	store := c.GetMediaStore()
	if store == nil {
		return fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	for _, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		file, err := os.Open(localPath)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to open media file", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			continue
		}

		switch part.Type {
		case "image":
			params := &telego.SendPhotoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Photo:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendPhoto(ctx, params)
			if err != nil && strings.Contains(err.Error(), "PHOTO_INVALID_DIMENSIONS") {
				if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
					file.Close()
					return fmt.Errorf("telegram rewind media after photo failure: %w", channels.ErrTemporary)
				}

				docParams := &telego.SendDocumentParams{
					ChatID:          tu.ID(chatID),
					MessageThreadID: threadID,
					Document:        telego.InputFile{File: file},
					Caption:         part.Caption,
				}
				_, err = c.bot.SendDocument(ctx, docParams)
			}
		case "audio":
			params := &telego.SendAudioParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Audio:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendAudio(ctx, params)
		case "video":
			params := &telego.SendVideoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Video:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendVideo(ctx, params)
		default: // "file" or unknown types
			params := &telego.SendDocumentParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Document:        telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendDocument(ctx, params)
		}

		file.Close()

		if err != nil {
			logger.ErrorCF("telegram", "Failed to send media", map[string]any{
				"type":  part.Type,
				"error": err.Error(),
			})
			return fmt.Errorf("telegram send media: %w", channels.ErrTemporary)
		}
	}

	return nil
}

func (c *TelegramChannel) handleMessage(ctx context.Context, message *telego.Message) error {
	candidate, err := c.buildInboundCandidate(ctx, message)
	if err != nil {
		return err
	}
	if candidate == nil {
		return nil
	}
	return c.dispatchInboundCandidate(ctx, *candidate)
}

func (c *TelegramChannel) downloadPhoto(ctx context.Context, fileID string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get photo file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ".jpg")
}

func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file.FilePath == "" {
		return ""
	}

	url := c.bot.FileDownloadURL(file.FilePath)
	logger.DebugCF("telegram", "File URL", map[string]any{"url": url})

	// Use FilePath as filename for better identification
	filename := file.FilePath + ext
	return utils.DownloadFile(url, filename, utils.DownloadOptions{
		LoggerPrefix: "telegram",
	})
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID, ext string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ext)
}

// parseTelegramChatID splits "chatID/threadID" into its components.
// Returns threadID=0 when no "/" is present (non-forum messages).
func parseTelegramChatID(chatID string) (int64, int, error) {
	idx := strings.Index(chatID, "/")
	if idx == -1 {
		cid, err := strconv.ParseInt(chatID, 10, 64)
		return cid, 0, err
	}
	cid, err := strconv.ParseInt(chatID[:idx], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tid, err := strconv.Atoi(chatID[idx+1:])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid thread ID in chat ID %q: %w", chatID, err)
	}
	return cid, tid, nil
}

// isBotMentioned checks if the bot is mentioned in the message via entities.
func (c *TelegramChannel) isBotMentioned(message *telego.Message) bool {
	text, entities := telegramEntityTextAndList(message)
	if text == "" || len(entities) == 0 {
		return false
	}

	botUsername := ""
	if c.bot != nil {
		botUsername = c.bot.Username()
	}
	runes := []rune(text)

	for _, entity := range entities {
		entityText, ok := telegramEntityText(runes, entity)
		if !ok {
			continue
		}

		switch entity.Type {
		case telego.EntityTypeMention:
			if botUsername != "" && strings.EqualFold(entityText, "@"+botUsername) {
				return true
			}
		case telego.EntityTypeTextMention:
			if botUsername != "" && entity.User != nil && strings.EqualFold(entity.User.Username, botUsername) {
				return true
			}
		case telego.EntityTypeBotCommand:
			if isBotCommandEntityForThisBot(entityText, botUsername) {
				return true
			}
		}
	}
	return false
}

func telegramEntityTextAndList(message *telego.Message) (string, []telego.MessageEntity) {
	if message.Text != "" {
		return message.Text, message.Entities
	}
	return message.Caption, message.CaptionEntities
}

func telegramEntityText(runes []rune, entity telego.MessageEntity) (string, bool) {
	if entity.Offset < 0 || entity.Length <= 0 {
		return "", false
	}
	end := entity.Offset + entity.Length
	if entity.Offset >= len(runes) || end > len(runes) {
		return "", false
	}
	return string(runes[entity.Offset:end]), true
}

func isBotCommandEntityForThisBot(entityText, botUsername string) bool {
	if !strings.HasPrefix(entityText, "/") {
		return false
	}
	command := strings.TrimPrefix(entityText, "/")
	if command == "" {
		return false
	}

	at := strings.IndexRune(command, '@')
	if at == -1 {
		// A bare /command delivered to this bot is intended for this bot.
		return true
	}

	mentionUsername := command[at+1:]
	if mentionUsername == "" || botUsername == "" {
		return false
	}
	return strings.EqualFold(mentionUsername, botUsername)
}

// stripBotMention removes the @bot mention from the content.
func (c *TelegramChannel) stripBotMention(content string) string {
	botUsername := c.bot.Username()
	if botUsername == "" {
		return content
	}
	// Case-insensitive replacement
	re := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(botUsername))
	content = re.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}

// cryptoRandInt returns a non-zero random int using crypto/rand.
func cryptoRandInt() int {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint32(b[:])) | 1 // ensure non-zero
}
