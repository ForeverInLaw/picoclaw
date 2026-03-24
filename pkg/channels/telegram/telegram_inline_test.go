package telegram

import (
	"testing"

	"github.com/mymmrac/telego"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestTelegramInlineChatIDRoundTrip(t *testing.T) {
	chatID := telegramInlineChatID("abc123")
	if !isTelegramInlineChatID(chatID) {
		t.Fatal("expected inline chat target to be recognized")
	}

	inlineMessageID, ok := telegramInlineMessageID(chatID)
	if !ok {
		t.Fatal("expected inline message id to parse")
	}
	if inlineMessageID != "abc123" {
		t.Fatalf("inlineMessageID = %q, want %q", inlineMessageID, "abc123")
	}
}

func TestBuildTelegramInlineQueryResult_UsesConfiguredPlaceholderAndButton(t *testing.T) {
	result := buildTelegramInlineQueryResult(config.TelegramInlineConfig{
		ResultTitle:     "Генерировать",
		PlaceholderText: "Думаю...",
		ButtonLabel:     ".",
	}, "inline query text", false)

	article, ok := result.(*telego.InlineQueryResultArticle)
	if !ok {
		t.Fatalf("result type = %T, want *telego.InlineQueryResultArticle", result)
	}
	if article.Title != "Генерировать" {
		t.Fatalf("article.Title = %q", article.Title)
	}
	content, ok := article.InputMessageContent.(*telego.InputTextMessageContent)
	if !ok {
		t.Fatalf("content type = %T, want *telego.InputTextMessageContent", article.InputMessageContent)
	}
	if content.MessageText != "<blockquote>inline query text</blockquote>\n\nДумаю..." {
		t.Fatalf("content.MessageText = %q", content.MessageText)
	}
	if content.ParseMode != telego.ModeHTML {
		t.Fatalf("content.ParseMode = %q", content.ParseMode)
	}
	if article.ReplyMarkup == nil || len(article.ReplyMarkup.InlineKeyboard) != 1 || len(article.ReplyMarkup.InlineKeyboard[0]) != 1 {
		t.Fatal("expected single technical inline button")
	}
	if article.ReplyMarkup.InlineKeyboard[0][0].CallbackData != telegramInlineCallbackData {
		t.Fatalf("callback data = %q", article.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
	}
}

func TestRenderTelegramInlineInitialContent_UsesNativeBlockquoteInHTMLMode(t *testing.T) {
	got := renderTelegramInlineInitialContent("hello\nworld", "Думаю...", false)
	want := "<blockquote>hello<br>world</blockquote>\n\nДумаю..."
	if got != want {
		t.Fatalf("renderTelegramInlineInitialContent() = %q, want %q", got, want)
	}
}

func TestMarkdownToTelegramHTML_PreservesNativeBlockquote(t *testing.T) {
	got := markdownToTelegramHTML("> quoted line\n\nplain text")
	want := "<blockquote>quoted line</blockquote>\n\nplain text"
	if got != want {
		t.Fatalf("markdownToTelegramHTML() = %q, want %q", got, want)
	}
}

func TestBuildTelegramInlineInboundMessage_MarksInlineMetadata(t *testing.T) {
	msg := buildTelegramInlineInboundMessage(telego.ChosenInlineResult{
		ResultID:        "res-1",
		InlineMessageID: "inline-msg-1",
		Query:           "сделай summary",
		From: telego.User{
			ID:        42,
			FirstName: "Alice",
			Username:  "alice",
		},
	}, bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  "42",
		CanonicalID: "telegram:42",
		Username:    "alice",
		DisplayName: "Alice",
	})

	if msg.ChatID != "inline:inline-msg-1" {
		t.Fatalf("msg.ChatID = %q", msg.ChatID)
	}
	if msg.Metadata[telegramInlineMetadataKey] != "true" {
		t.Fatalf("inline metadata marker missing: %#v", msg.Metadata)
	}
	if msg.Metadata[telegramInlineQueryKey] != "сделай summary" {
		t.Fatalf("inline query metadata = %q", msg.Metadata[telegramInlineQueryKey])
	}
	if msg.Content != "сделай summary" {
		t.Fatalf("msg.Content = %q", msg.Content)
	}
}
