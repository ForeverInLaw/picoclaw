package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendStickerTool_RejectsNonTelegram(t *testing.T) {
	tool := NewSendStickerTool("token", "https://api.telegram.org")

	result := tool.Execute(WithToolContext(context.Background(), "discord", "chat-1"), map[string]any{
		"sticker_set": "supermegahype",
	})

	if !result.IsError {
		t.Fatal("expected telegram-only rejection")
	}
}

func TestSendStickerTool_SendsEmojiMatchedStickerFromSet(t *testing.T) {
	seen := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "/getStickerSet"):
			if got := r.Form.Get("name"); got != "supermegahype" {
				t.Fatalf("expected sticker set supermegahype, got %q", got)
			}
			writeJSON(t, w, map[string]any{
				"ok": true,
				"result": map[string]any{
					"name":  "supermegahype",
					"title": "Super Mega Hype",
					"stickers": []map[string]any{
						{"file_id": "file-1", "emoji": "😂"},
						{"file_id": "file-2", "emoji": "😎"},
					},
				},
			})
		case strings.HasSuffix(r.URL.Path, "/sendSticker"):
			if got := r.Form.Get("chat_id"); got != "chat-42" {
				t.Fatalf("expected chat_id chat-42, got %q", got)
			}
			if got := r.Form.Get("sticker"); got != "file-1" {
				t.Fatalf("expected sticker file-1, got %q", got)
			}
			writeJSON(t, w, map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 99,
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	tool := NewSendStickerTool("token", server.URL)
	result := tool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), map[string]any{
		"sticker_set": "supermegahype",
		"emoji":       "😂",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !result.Silent {
		t.Fatal("expected silent result for direct sticker send")
	}
	if !result.Terminal {
		t.Fatal("expected terminal result for direct sticker send")
	}
	if got := len(seen); got != 2 {
		t.Fatalf("expected 2 telegram api calls, got %d", got)
	}
}

func TestSendStickerTool_ListSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if !strings.HasSuffix(r.URL.Path, "/getStickerSet") {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Form.Get("name"); got != "rostikbalbes" {
			t.Fatalf("expected sticker set rostikbalbes, got %q", got)
		}
		writeJSON(t, w, map[string]any{
			"ok": true,
			"result": map[string]any{
				"name":  "rostikbalbes",
				"title": "Rostik Balbes",
				"stickers": []map[string]any{
					{"file_id": "file-a", "emoji": "🤔"},
				},
			},
		})
	}))
	defer server.Close()

	tool := NewSendStickerTool("token", server.URL)
	result := tool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), map[string]any{
		"list_set": "rostikbalbes",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %s", result.ForLLM)
	}
	if !result.Silent {
		t.Fatal("expected silent list result")
	}
	if !strings.Contains(result.ForLLM, `Sticker set "rostikbalbes"`) {
		t.Fatalf("unexpected list output: %q", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "🤔 file-a") {
		t.Fatalf("expected file listing in output, got %q", result.ForLLM)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode: %v", err)
	}
}
