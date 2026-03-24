package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SendStickerTool struct {
	token   string
	baseURL string
	client  *http.Client
}

type telegramStickerSetResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		Name     string `json:"name"`
		Title    string `json:"title"`
		Stickers []struct {
			FileID string `json:"file_id"`
			Emoji  string `json:"emoji"`
		} `json:"stickers"`
	} `json:"result"`
}

type telegramSendStickerResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

func NewSendStickerTool(token, baseURL string) *SendStickerTool {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	return &SendStickerTool{
		token:   strings.TrimSpace(token),
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (t *SendStickerTool) Name() string { return "send_sticker" }

func (t *SendStickerTool) Description() string {
	return "Send a Telegram sticker to the current chat. Use this instead of exec/curl for Telegram stickers. Supports direct sticker file_id, sticker_set plus emoji, or listing stickers in a set."
}

func (t *SendStickerTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"sticker": map[string]any{
				"type":        "string",
				"description": "Optional Telegram sticker file_id to send directly.",
			},
			"sticker_set": map[string]any{
				"type":        "string",
				"description": "Optional Telegram sticker set name, e.g. supermegahype.",
			},
			"emoji": map[string]any{
				"type":        "string",
				"description": "Optional emoji used to choose a matching sticker from the set. If empty, a random sticker from the set is used.",
			},
			"list_set": map[string]any{
				"type":        "string",
				"description": "Optional Telegram sticker set name to inspect instead of sending.",
			},
		},
	}
}

func (t *SendStickerTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if strings.TrimSpace(ToolChannel(ctx)) != "telegram" {
		return ErrorResult("send_sticker is only available in Telegram chats")
	}
	if t.token == "" {
		return ErrorResult("telegram token is not configured for send_sticker")
	}

	listSet, _ := args["list_set"].(string)
	if listSet = strings.TrimSpace(listSet); listSet != "" {
		resp, err := t.getStickerSet(ctx, listSet)
		if err != nil {
			return ErrorResult(fmt.Sprintf("listing sticker set: %v", err))
		}
		lines := []string{
			fmt.Sprintf("Sticker set %q (%s)", resp.Result.Name, resp.Result.Title),
		}
		limit := minInt(len(resp.Result.Stickers), 12)
		for i := 0; i < limit; i++ {
			s := resp.Result.Stickers[i]
			lines = append(lines, fmt.Sprintf("- %s %s", s.Emoji, s.FileID))
		}
		if len(resp.Result.Stickers) > limit {
			lines = append(lines, fmt.Sprintf("... and %d more", len(resp.Result.Stickers)-limit))
		}
		return SilentResult(strings.Join(lines, "\n"))
	}

	chatID := strings.TrimSpace(ToolChatID(ctx))
	if chatID == "" {
		return ErrorResult("no telegram chat context available for send_sticker")
	}

	sticker, _ := args["sticker"].(string)
	sticker = strings.TrimSpace(sticker)
	if sticker == "" {
		setName, _ := args["sticker_set"].(string)
		emoji, _ := args["emoji"].(string)
		chosen, err := t.resolveStickerFromSet(ctx, strings.TrimSpace(setName), strings.TrimSpace(emoji))
		if err != nil {
			return ErrorResult(fmt.Sprintf("resolving sticker from set: %v", err))
		}
		sticker = chosen
	}

	if sticker == "" {
		return ErrorResult("send_sticker requires sticker, or sticker_set, or list_set")
	}

	if err := t.sendSticker(ctx, chatID, sticker); err != nil {
		return ErrorResult(fmt.Sprintf("sending sticker: %v", err))
	}

	return SilentResult("Sticker sent to current Telegram chat").WithTerminal()
}

func (t *SendStickerTool) resolveStickerFromSet(ctx context.Context, setName, emoji string) (string, error) {
	if setName == "" {
		return "", fmt.Errorf("sticker_set is required when sticker is omitted")
	}
	resp, err := t.getStickerSet(ctx, setName)
	if err != nil {
		return "", err
	}
	if len(resp.Result.Stickers) == 0 {
		return "", fmt.Errorf("sticker set %q is empty", setName)
	}

	if emoji != "" {
		for _, s := range resp.Result.Stickers {
			if s.Emoji == emoji && strings.TrimSpace(s.FileID) != "" {
				return s.FileID, nil
			}
		}
	}

	idx := rand.New(rand.NewSource(time.Now().UnixNano())).Intn(len(resp.Result.Stickers))
	return resp.Result.Stickers[idx].FileID, nil
}

func (t *SendStickerTool) getStickerSet(ctx context.Context, setName string) (*telegramStickerSetResponse, error) {
	form := url.Values{}
	form.Set("name", setName)

	var resp telegramStickerSetResponse
	if err := t.postForm(ctx, "getStickerSet", form, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("%s", resp.Description)
	}
	return &resp, nil
}

func (t *SendStickerTool) sendSticker(ctx context.Context, chatID, sticker string) error {
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("sticker", sticker)

	var resp telegramSendStickerResponse
	if err := t.postForm(ctx, "sendSticker", form, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Description)
	}
	return nil
}

func (t *SendStickerTool) postForm(ctx context.Context, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		fmt.Sprintf("%s/bot%s/%s", t.baseURL, t.token, method),
		bytes.NewBufferString(form.Encode()),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("telegram api status %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding telegram response: %w", err)
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
