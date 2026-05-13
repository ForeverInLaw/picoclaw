package telegram

import (
	"testing"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestTelegramGuestChatIDRoundTrip(t *testing.T) {
	chatID := telegramGuestChatID("abc123")
	if !isTelegramGuestChatID(chatID) {
		t.Fatal("expected guest chat target to be recognized")
	}

	inlineMessageID, ok := telegramGuestInlineMessageID(chatID)
	if !ok {
		t.Fatal("expected inline message id to parse")
	}
	if inlineMessageID != "abc123" {
		t.Fatalf("inlineMessageID = %q, want %q", inlineMessageID, "abc123")
	}
}

func TestBuildTelegramGuestPlaceholderResult_UsesConfiguredPlaceholder(t *testing.T) {
	result := buildTelegramGuestPlaceholderResult(config.TelegramGuestConfig{
		ResultTitle:     "Generate",
		PlaceholderText: "Thinking...",
	}, "guest query text", true)

	article, ok := result.(*telego.InlineQueryResultArticle)
	if !ok {
		t.Fatalf("result type = %T, want *telego.InlineQueryResultArticle", result)
	}
	if article.Title != "Generate" {
		t.Fatalf("article.Title = %q", article.Title)
	}
	content, ok := article.InputMessageContent.(*telego.InputTextMessageContent)
	if !ok {
		t.Fatalf("content type = %T, want *telego.InputTextMessageContent", article.InputMessageContent)
	}
	if content.MessageText != "Thinking\\.\\.\\." {
		t.Fatalf("content.MessageText = %q", content.MessageText)
	}
	if content.ParseMode != telego.ModeMarkdownV2 {
		t.Fatalf("content.ParseMode = %q", content.ParseMode)
	}
	if article.ReplyMarkup != nil {
		t.Fatal("guest mode answer must not carry an inline keyboard")
	}
}

func TestNormalizeTelegramGuestPlaceholder_AddsCloudForLegacyDefault(t *testing.T) {
	got := normalizeTelegramGuestPlaceholder("Думаю...")
	want := "Думаю... 💭"
	if got != want {
		t.Fatalf("normalizeTelegramGuestPlaceholder() = %q, want %q", got, want)
	}
}

func TestRenderTelegramGuestInitialContent_EscapesPlaceholderForMarkdownV2(t *testing.T) {
	got := renderTelegramGuestInitialContent("Thinking...")
	want := "Thinking\\.\\.\\."
	if got != want {
		t.Fatalf("renderTelegramGuestInitialContent() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_PreservesNativeBlockquote(t *testing.T) {
	got := markdownToTelegramHTML("> quoted line\n\nplain text")
	want := "<blockquote>quoted line</blockquote>\n\nplain text"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestBuildTelegramGuestInboundMessage_MarksGuestMetadata(t *testing.T) {
	tgMsg := &telego.Message{
		MessageID: 1,
		Text:      "сделай summary",
		Chat:      telego.Chat{ID: -100, Type: telego.ChatTypeGroup, Title: "Test"},
		From: &telego.User{
			ID:        42,
			FirstName: "Alice",
			Username:  "alice",
		},
		GuestQueryID: "gq-1",
	}

	msg := buildTelegramGuestInboundMessage(tgMsg, "inline-msg-1", bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "42",
		CanonicalID: "telegram:42",
		Username:    "alice",
		DisplayName: "Alice",
	})

	if msg.ChatID != "guest:inline-msg-1" {
		t.Fatalf("msg.ChatID = %q", msg.ChatID)
	}
	if msg.MessageID != "inline-msg-1" {
		t.Fatalf("msg.MessageID = %q", msg.MessageID)
	}
	if msg.Metadata[telegramGuestMetadataKey] != "true" {
		t.Fatalf("guest metadata marker missing: %#v", msg.Metadata)
	}
	if msg.Metadata[telegramGuestQueryKey] != "сделай summary" {
		t.Fatalf("guest query metadata = %q", msg.Metadata[telegramGuestQueryKey])
	}
	if msg.Content != "сделай summary" {
		t.Fatalf("msg.Content = %q", msg.Content)
	}
}
