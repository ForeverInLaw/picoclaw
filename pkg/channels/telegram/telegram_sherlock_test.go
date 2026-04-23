package telegram

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"

	"github.com/sipeed/picoclaw/pkg/sherlock"
)

func TestHandleMessage_SherlockCommandSendsInlineMenu(t *testing.T) {
	caller := &stubCaller{callFn: func(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
		return successResponse(t), nil
	}}
	ch := newTestChannel(t, caller)
	store, err := sherlock.Open(filepath.Join(t.TempDir(), "state", "sherlock.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	ch.sherlock = store
	if err := ch.sherlock.RecordMessage(context.Background(), "42", "alice", "Alice", "hello"); err != nil {
		t.Fatalf("RecordMessage() error = %v", err)
	}

	err = ch.handleMessage(context.Background(), &telego.Message{
		MessageID: 7,
		Text:      "/sherlock",
		Chat:      telego.Chat{ID: 666, Type: "private"},
		From:      &telego.User{ID: 666, Username: "owner", FirstName: "Owner"},
	})
	if err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(caller.calls))
	}
	if !strings.Contains(caller.calls[0].URL, "sendMessage") {
		t.Fatalf("unexpected URL: %s", caller.calls[0].URL)
	}

	var params map[string]any
	if err := json.Unmarshal(caller.calls[0].Data.BodyRaw, &params); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	text, _ := params["text"].(string)
	if !strings.Contains(text, "Sherlock") {
		t.Fatalf("text = %q", text)
	}
	markup, ok := params["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("reply_markup missing: %#v", params)
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok || len(rows) == 0 {
		t.Fatalf("inline keyboard missing: %#v", markup)
	}
}

func TestObserveSherlockMessage_StoresOnlyPrivateNonCommands(t *testing.T) {
	caller := &stubCaller{callFn: func(ctx context.Context, url string, data *ta.RequestData) (*ta.Response, error) {
		return successResponse(t), nil
	}}
	ch := newTestChannel(t, caller)
	store, err := sherlock.Open(filepath.Join(t.TempDir(), "state", "sherlock.sqlite"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	ch.sherlock = store

	ch.observeSherlockMessage(context.Background(), &telego.Message{
		Text: "/help",
		Chat: telego.Chat{ID: 42, Type: "private"},
		From: &telego.User{ID: 42, Username: "alice", FirstName: "Alice"},
	})
	ch.observeSherlockMessage(context.Background(), &telego.Message{
		Text: "hello there",
		Chat: telego.Chat{ID: 42, Type: "private"},
		From: &telego.User{ID: 42, Username: "alice", FirstName: "Alice"},
	})
	ch.observeSherlockMessage(context.Background(), &telego.Message{
		Text: "group text",
		Chat: telego.Chat{ID: -100, Type: "group"},
		From: &telego.User{ID: 42, Username: "alice", FirstName: "Alice"},
	})

	messages, err := ch.sherlock.GetMessages(context.Background(), "42", 20)
	if err != nil {
		t.Fatalf("GetMessages() error = %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("len(messages) = %d, want 1", len(messages))
	}
	if messages[0].Text != "hello there" {
		t.Fatalf("message text = %q", messages[0].Text)
	}
}
