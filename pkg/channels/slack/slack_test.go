package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/slack-go/slack"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
)

func TestSendMediaUpload(t *testing.T) {
	content := "media upload test"
	filename := "report.txt"
	localPath := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(localPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			if r.FormValue("filename") != filename || r.FormValue("length") != strconv.Itoa(len(content)) {
				t.Errorf("unexpected upload metadata: %v", r.Form)
			}
			fmt.Fprintf(w, `{"ok":true,"file_id":"F123","upload_url":%q}`, server.URL+"/upload")
		case "/upload":
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil || string(body) != content {
				t.Errorf("uploaded body = %q, error = %v", body, err)
			}
		case "/files.completeUploadExternal":
			if r.FormValue("channel_id") != "C123" {
				t.Errorf("unexpected channel: %q", r.FormValue("channel_id"))
			}
			var files []slack.FileSummary
			if err := json.Unmarshal([]byte(r.FormValue("files")), &files); err != nil {
				t.Error(err)
			} else if len(files) != 1 || files[0].ID != "F123" || files[0].Title != "Report" {
				t.Errorf("unexpected completed files: %+v", files)
			}
			fmt.Fprint(w, `{"ok":true,"files":[{"id":"F123"}]}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := media.NewFileMediaStore()
	ref, err := store.Store(localPath, media.MediaMeta{Filename: filename}, "test")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := NewSlackChannel(config.SlackConfig{BotToken: "xoxb-test", AppToken: "xapp-test"}, bus.NewMessageBus())
	if err != nil {
		t.Fatal(err)
	}
	ch.api = slack.New("xoxb-test", slack.OptionAPIURL(server.URL+"/"))
	ch.SetMediaStore(store)
	ch.SetRunning(true)
	if err := ch.SendMedia(context.Background(), bus.OutboundMediaMessage{
		ChatID: "C123",
		Parts:  []bus.MediaPart{{Ref: ref, Filename: filename, Caption: "Report"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestParseSlackChatID(t *testing.T) {
	tests := []struct {
		name       string
		chatID     string
		wantChanID string
		wantThread string
	}{
		{
			name:       "channel only",
			chatID:     "C123456",
			wantChanID: "C123456",
			wantThread: "",
		},
		{
			name:       "channel with thread",
			chatID:     "C123456/1234567890.123456",
			wantChanID: "C123456",
			wantThread: "1234567890.123456",
		},
		{
			name:       "DM channel",
			chatID:     "D987654",
			wantChanID: "D987654",
			wantThread: "",
		},
		{
			name:       "empty string",
			chatID:     "",
			wantChanID: "",
			wantThread: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chanID, threadTS := parseSlackChatID(tt.chatID)
			if chanID != tt.wantChanID {
				t.Errorf("parseSlackChatID(%q) channelID = %q, want %q", tt.chatID, chanID, tt.wantChanID)
			}
			if threadTS != tt.wantThread {
				t.Errorf("parseSlackChatID(%q) threadTS = %q, want %q", tt.chatID, threadTS, tt.wantThread)
			}
		})
	}
}

func TestStripBotMention(t *testing.T) {
	ch := &SlackChannel{botUserID: "U12345BOT"}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "mention at start",
			input: "<@U12345BOT> hello there",
			want:  "hello there",
		},
		{
			name:  "mention in middle",
			input: "hey <@U12345BOT> can you help",
			want:  "hey  can you help",
		},
		{
			name:  "no mention",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "only mention",
			input: "<@U12345BOT>",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ch.stripBotMention(tt.input)
			if got != tt.want {
				t.Errorf("stripBotMention(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewSlackChannel(t *testing.T) {
	msgBus := bus.NewMessageBus()

	t.Run("missing bot token", func(t *testing.T) {
		cfg := config.SlackConfig{
			BotToken: "",
			AppToken: "xapp-test",
		}
		_, err := NewSlackChannel(cfg, msgBus)
		if err == nil {
			t.Error("expected error for missing bot_token, got nil")
		}
	})

	t.Run("missing app token", func(t *testing.T) {
		cfg := config.SlackConfig{
			BotToken: "xoxb-test",
			AppToken: "",
		}
		_, err := NewSlackChannel(cfg, msgBus)
		if err == nil {
			t.Error("expected error for missing app_token, got nil")
		}
	})

	t.Run("valid config", func(t *testing.T) {
		cfg := config.SlackConfig{
			BotToken:  "xoxb-test",
			AppToken:  "xapp-test",
			AllowFrom: []string{"U123"},
		}
		ch, err := NewSlackChannel(cfg, msgBus)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Name() != "slack" {
			t.Errorf("Name() = %q, want %q", ch.Name(), "slack")
		}
		if ch.IsRunning() {
			t.Error("new channel should not be running")
		}
	})
}

func TestSlackChannelIsAllowed(t *testing.T) {
	msgBus := bus.NewMessageBus()

	t.Run("empty allowlist allows all", func(t *testing.T) {
		cfg := config.SlackConfig{
			BotToken:  "xoxb-test",
			AppToken:  "xapp-test",
			AllowFrom: []string{},
		}
		ch, _ := NewSlackChannel(cfg, msgBus)
		if !ch.IsAllowed("U_ANYONE") {
			t.Error("empty allowlist should allow all users")
		}
	})

	t.Run("allowlist restricts users", func(t *testing.T) {
		cfg := config.SlackConfig{
			BotToken:  "xoxb-test",
			AppToken:  "xapp-test",
			AllowFrom: []string{"U_ALLOWED"},
		}
		ch, _ := NewSlackChannel(cfg, msgBus)
		if !ch.IsAllowed("U_ALLOWED") {
			t.Error("allowed user should pass allowlist check")
		}
		if ch.IsAllowed("U_BLOCKED") {
			t.Error("non-allowed user should be blocked")
		}
	})
}
