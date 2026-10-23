package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/auth"
)

const (
	MaxCodexImageBytes  = 32 << 20
	MaxCodexImageInputs = 5
)

// CodexImageClient uses the native Codex route, independently of text routing.
// Its account is explicit; failed requests never borrow another account or retry.
type CodexImageClient struct {
	accountID string
	maxBytes  int
	http      *http.Client
}

func NewCodexImageClient(accountID string, maxBytes int) *CodexImageClient {
	if maxBytes <= 0 || maxBytes > MaxCodexImageBytes {
		maxBytes = MaxCodexImageBytes
	}
	return &CodexImageClient{
		accountID: accountID, maxBytes: maxBytes,
		http: &http.Client{
			Timeout:       5 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *CodexImageClient) Generate(ctx context.Context, prompt string, images [][]byte, transparent bool) ([]byte, error) {
	if strings.TrimSpace(prompt) == "" || len(images) > MaxCodexImageInputs {
		return nil, fmt.Errorf("provide a prompt and at most %d reference images", MaxCodexImageInputs)
	}
	if c.accountID == "" {
		return nil, fmt.Errorf("configure tools.generate_image.account_id with a native Codex connection")
	}
	account, err := auth.GetAccountContext(ctx, c.accountID)
	if err != nil || account.Provider != "openai" {
		return nil, fmt.Errorf("configured Codex image account is unavailable")
	}
	if account.Resource != "" {
		return nil, fmt.Errorf("this connection uses the public ChatGPT API; native images require a CLI Codex connection: picoclaw auth login --provider openai --device-code")
	}
	credential, err := auth.AccountCredentialContext(ctx, c.accountID)
	if err != nil || credential == nil || credential.AccessToken == "" {
		return nil, fmt.Errorf("Codex image account is disabled or needs sign-in")
	}

	type imageReference struct {
		ImageURL string `json:"image_url"`
	}
	request := struct {
		Model      string           `json:"model"`
		Prompt     string           `json:"prompt"`
		N          int              `json:"n"`
		Background string           `json:"background,omitempty"`
		Images     []imageReference `json:"images,omitempty"`
	}{Model: "gpt-image-2", Prompt: prompt, N: 1}
	if transparent {
		request.Background = "transparent"
	}
	total := 0
	for _, data := range images {
		total += len(data)
		mime := http.DetectContentType(data)
		if total > MaxCodexImageBytes || len(data) == 0 || (mime != "image/png" && mime != "image/jpeg" && mime != "image/webp") {
			return nil, fmt.Errorf("reference images must be PNG, JPEG or WebP and total at most 32 MiB")
		}
		request.Images = append(request.Images, imageReference{
			ImageURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
		})
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode Codex image request: %w", err)
	}
	path := "generations"
	if len(images) > 0 {
		path = "edits"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://chatgpt.com/backend-api/codex/images/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("x-codex-image-turn-id", uuid.NewString())
	if credential.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", credential.AccountID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Codex image request failed; no automatic retry: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not expose response bodies or revoke a connection that may still serve text.
		reason := "native image endpoint rejected the request"
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			reason = "native Codex sign-in is required"
		case http.StatusForbidden:
			reason = "this account is not allowed to use native image generation"
		case http.StatusTooManyRequests:
			reason = "image generation limit reached; try later"
		}
		return nil, fmt.Errorf("Codex images HTTP %d: %s", resp.StatusCode, reason)
	}
	limit := int64(base64.StdEncoding.EncodedLen(c.maxBytes) + 64*1024)
	encoded, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read Codex image response: %w", err)
	}
	if int64(len(encoded)) > limit {
		return nil, fmt.Errorf("generated image exceeds the configured media size limit")
	}
	var result struct {
		Data []struct {
			Base64 string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(encoded, &result) != nil || len(result.Data) != 1 ||
		result.Data[0].Base64 == "" || len(result.Data[0].Base64) > base64.StdEncoding.EncodedLen(c.maxBytes) {
		return nil, fmt.Errorf("Codex returned no usable image within the configured media size limit")
	}
	data, err := base64.StdEncoding.DecodeString(result.Data[0].Base64)
	if err != nil || len(data) > c.maxBytes {
		return nil, fmt.Errorf("Codex returned invalid or oversized image data")
	}
	switch http.DetectContentType(data) {
	case "image/png", "image/jpeg", "image/webp":
		return data, nil
	default:
		return nil, fmt.Errorf("Codex returned an unsupported image format")
	}
}
