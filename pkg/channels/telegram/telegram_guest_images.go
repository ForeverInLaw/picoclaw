package telegram

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mymmrac/telego"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/logger"
)

// Guest replies have no writable chat ID. Upload privately to their recorded
// owner, then reuse the Telegram file ID in the already published guest reply.
func (c *TelegramChannel) sendGuestImage(ctx context.Context, inlineID string, msg bus.OutboundMediaMessage) error {
	if len(msg.Parts) != 1 || msg.Parts[0].Type != "image" {
		return fmt.Errorf("guest replies support one generated image: %w", channels.ErrSendFailed)
	}
	part := msg.Parts[0]
	c.inlineMu.Lock()
	pages, err := c.loadInlinePages(inlineID)
	c.inlineMu.Unlock()
	if err != nil || !pages.Guest || pages.OwnerID <= 0 {
		return fmt.Errorf("guest image destination unavailable: %w", channels.ErrSendFailed)
	}
	if pages.PhotoFileID != "" && pages.PhotoRef != part.Ref {
		return fmt.Errorf("guest reply already has an image: %w", channels.ErrSendFailed)
	}
	ownerID := pages.OwnerID
	fileID := pages.PhotoFileID
	if fileID == "" {
		// Upload outside the shared inline lock so other guest text replies
		// and streaming previews continue while the file is transferred.
		fileID, err = c.uploadGuestImage(ctx, ownerID, part.Ref)
	}
	c.inlineMu.Lock()
	defer c.inlineMu.Unlock()
	current, readErr := c.loadInlinePages(inlineID)
	if readErr != nil || !current.Guest || current.OwnerID != ownerID {
		return fmt.Errorf("guest image destination changed: %w", channels.ErrSendFailed)
	}
	pages = current
	if err != nil {
		if errors.Is(err, channels.ErrSendFailed) && pages.PhotoFileID == "" {
			pages.Content = "Изображение создано, но загрузить его в Telegram не удалось. Откройте личный чат с ботом и нажмите /start. Повторно генерировать изображение не нужно."
			pages.Published = false
			if saveErr := c.saveInlinePages(inlineID, pages); saveErr == nil {
				_ = c.publishInlinePages(ctx, inlineID, pages, true)
			}
		}
		return err
	}
	if pages.PhotoFileID != "" && pages.PhotoRef != part.Ref {
		return fmt.Errorf("guest reply already has an image: %w", channels.ErrSendFailed)
	}
	if pages.PhotoFileID == "" {
		pages.PhotoFileID = fileID
		pages.PhotoRef = part.Ref
		pages.Content = strings.TrimSpace(part.Caption)
		if pages.Content == "" {
			pages.Content = "Изображение готово."
		}
		pages.Plain = false
		pages.Published = false
		// Retain the upload before editing, so delivery retries reuse it without
		// uploading again or rerunning image generation.
		if err = c.saveInlinePages(inlineID, pages); err != nil {
			return fmt.Errorf("retain guest image upload: %w: %w", channels.ErrTemporary, err)
		}
	}
	return c.publishInlinePages(ctx, inlineID, pages, true)
}

func (c *TelegramChannel) uploadGuestImage(ctx context.Context, ownerID int64, ref string) (string, error) {
	store := c.GetMediaStore()
	if store == nil {
		return "", fmt.Errorf("guest image media store unavailable: %w", channels.ErrSendFailed)
	}
	path, err := store.Resolve(ref)
	if err != nil {
		return "", fmt.Errorf("guest image file unavailable: %w", channels.ErrSendFailed)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("guest image file unavailable: %w", channels.ErrSendFailed)
	}
	defer file.Close()
	uploaded, err := c.bot.SendPhoto(ctx, &telego.SendPhotoParams{
		ChatID: tu.ID(ownerID), Photo: telego.InputFile{File: file},
		DisableNotification: true,
	})
	if err != nil {
		return "", telegramDeliveryError(err)
	}
	if uploaded == nil {
		return "", fmt.Errorf("Telegram returned no uploaded photo: %w", channels.ErrTemporary)
	}
	// Cleanup must still run when the sending request is canceled.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.bot.DeleteMessage(cleanupCtx, &telego.DeleteMessageParams{
			ChatID: tu.ID(ownerID), MessageID: uploaded.MessageID,
		}); err != nil {
			logger.WarnCF("telegram", "Temporary guest image upload could not be removed",
				map[string]any{"message_id": uploaded.MessageID})
		}
	}()
	if len(uploaded.Photo) == 0 {
		return "", fmt.Errorf("Telegram returned no uploaded photo ID: %w", channels.ErrTemporary)
	}
	id := uploaded.Photo[len(uploaded.Photo)-1].FileID
	if id == "" {
		return "", fmt.Errorf("Telegram returned an empty photo ID: %w", channels.ErrTemporary)
	}
	return id, nil
}
