package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/common"
)

var (
	allowedImageSizes = map[string]struct{}{
		"1024x1024": {},
		"1024x1536": {},
		"1536x1024": {},
		"auto":      {},
	}
	allowedImageQualities = map[string]struct{}{
		"low":    {},
		"medium": {},
		"high":   {},
		"auto":   {},
	}
	allowedImageResponseFormats = map[string]struct{}{
		"url":      {},
		"b64_json": {},
	}
	allowedImageBackgrounds = map[string]struct{}{
		"transparent": {},
		"opaque":      {},
		"auto":        {},
	}
	allowedImageOutputFormats = map[string]struct{}{
		"png":  {},
		"jpeg": {},
		"webp": {},
	}
)

type GenerateImageTool struct {
	configGetter func() *config.Config
	mediaStore   media.MediaStore
}

type imageGenerationData struct {
	URL           string `json:"url"`
	B64JSON       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt"`
}

type imageGenerationResponse struct {
	Created int64                 `json:"created"`
	Data    []imageGenerationData `json:"data"`
}

func NewGenerateImageTool(configGetter func() *config.Config) *GenerateImageTool {
	return &GenerateImageTool{configGetter: configGetter}
}

func (t *GenerateImageTool) Name() string { return "generate_image" }

func (t *GenerateImageTool) Description() string {
	return "Generate image media from a text prompt using the configured default image-generation model. " +
		"Use the prompt exactly as approved by the user. If you want to improve or rewrite the prompt, ask first and wait for consent."
}

func (t *GenerateImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Final approved prompt for the image. Do not silently rewrite it.",
			},
			"size": map[string]any{
				"type":        "string",
				"description": "Optional image size: 1024x1024, 1024x1536, 1536x1024, or auto.",
			},
			"quality": map[string]any{
				"type":        "string",
				"description": "Optional quality: low, medium, high, or auto.",
			},
			"n": map[string]any{
				"type":        "integer",
				"description": "Optional number of images to generate (1-10).",
			},
			"response_format": map[string]any{
				"type":        "string",
				"description": "Optional response format: b64_json or url. Defaults to b64_json.",
			},
			"background": map[string]any{
				"type":        "string",
				"description": "Optional background mode: transparent, opaque, or auto.",
			},
			"output_format": map[string]any{
				"type":        "string",
				"description": "Optional output format: png, jpeg, or webp.",
			},
		},
		"required": []string{"prompt"},
	}
}

func (t *GenerateImageTool) SetMediaStore(store media.MediaStore) {
	t.mediaStore = store
}

func (t *GenerateImageTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	channel := strings.TrimSpace(ToolChannel(ctx))
	chatID := strings.TrimSpace(ToolChatID(ctx))
	if channel == "" || chatID == "" {
		return ErrorResult("no target channel/chat available")
	}
	if t.mediaStore == nil {
		return ErrorResult("media store not configured")
	}
	if t.configGetter == nil {
		return ErrorResult("config is not available")
	}

	cfg := t.configGetter()
	if cfg == nil {
		return ErrorResult("config is not available")
	}

	modelName := strings.TrimSpace(cfg.Agents.Defaults.ImageGenerationModel)
	if modelName == "" {
		return ErrorResult("no default image-generation model configured")
	}

	modelCfg, err := cfg.GetModelConfig(modelName)
	if err != nil {
		return ErrorResult(fmt.Sprintf("default image-generation model %q not found: %v", modelName, err)).WithError(err)
	}

	reqBody, err := buildImageGenerationRequest(args)
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}

	imagesResp, modelID, _, err := t.callImageGenerationAPI(ctx, modelCfg, reqBody)
	if err != nil {
		return ErrorResult(fmt.Sprintf("image generation failed: %v", err)).WithError(err)
	}

	refs, llmNote, err := t.persistGeneratedImages(ctx, channel, chatID, modelName, modelID, reqBody, imagesResp)
	if err != nil {
		return ErrorResult(fmt.Sprintf("failed to prepare generated images: %v", err)).WithError(err)
	}

	return MediaResult(llmNote, refs).WithResponseHandled()
}

func buildImageGenerationRequest(args map[string]any) (map[string]any, error) {
	prompt, _ := args["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	body := map[string]any{
		"prompt":          prompt,
		"n":               1,
		"response_format": "b64_json",
	}

	if raw, ok := args["size"]; ok {
		size, err := stringArg(raw, allowedImageSizes, "size")
		if err != nil {
			return nil, err
		}
		if size != "" {
			body["size"] = size
		}
	}

	if raw, ok := args["quality"]; ok {
		quality, err := stringArg(raw, allowedImageQualities, "quality")
		if err != nil {
			return nil, err
		}
		if quality != "" {
			body["quality"] = quality
		}
	}

	if raw, ok := args["response_format"]; ok {
		responseFormat, err := stringArg(raw, allowedImageResponseFormats, "response_format")
		if err != nil {
			return nil, err
		}
		if responseFormat != "" {
			body["response_format"] = responseFormat
		}
	}

	if raw, ok := args["background"]; ok {
		background, err := stringArg(raw, allowedImageBackgrounds, "background")
		if err != nil {
			return nil, err
		}
		if background != "" {
			body["background"] = background
		}
	}

	if raw, ok := args["output_format"]; ok {
		outputFormat, err := stringArg(raw, allowedImageOutputFormats, "output_format")
		if err != nil {
			return nil, err
		}
		if outputFormat != "" {
			body["output_format"] = outputFormat
		}
	}

	if raw, ok := args["n"]; ok {
		n, err := intArg(raw, "n")
		if err != nil {
			return nil, err
		}
		if n < 1 || n > 10 {
			return nil, fmt.Errorf("n must be between 1 and 10")
		}
		body["n"] = n
	}

	return body, nil
}

func (t *GenerateImageTool) callImageGenerationAPI(
	ctx context.Context,
	modelCfg *config.ModelConfig,
	reqBody map[string]any,
) (*imageGenerationResponse, string, string, error) {
	if modelCfg == nil {
		return nil, "", "", fmt.Errorf("model config is nil")
	}

	apiBase := providers.ResolveAPIBase(modelCfg)
	if apiBase == "" {
		return nil, "", "", fmt.Errorf("api_base is required for image generation model %q", modelCfg.ModelName)
	}

	_, modelID := providers.ExtractProtocol(modelCfg.Model)
	if strings.TrimSpace(modelID) == "" {
		return nil, "", "", fmt.Errorf("image generation model %q has empty model id", modelCfg.ModelName)
	}

	body := make(map[string]any, len(modelCfg.ExtraBody)+len(reqBody)+1)
	for k, v := range modelCfg.ExtraBody {
		body[k] = v
	}
	for k, v := range reqBody {
		body[k] = v
	}
	body["model"] = modelID

	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to marshal image generation request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/images/generations", bytes.NewReader(jsonData))
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	userAgent := strings.TrimSpace(modelCfg.UserAgent)
	if userAgent == "" {
		userAgent = common.DefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	if apiKey := strings.TrimSpace(modelCfg.APIKey()); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := common.NewHTTPClient(modelCfg.Proxy)
	if modelCfg.RequestTimeout > 0 {
		client.Timeout = timeDurationSeconds(modelCfg.RequestTimeout)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", "", common.HandleErrorResponse(resp, apiBase)
	}

	parsed, err := parseImageGenerationResponse(resp, apiBase)
	if err != nil {
		return nil, "", "", err
	}
	if parsed == nil || len(parsed.Data) == 0 {
		return nil, "", "", fmt.Errorf("image generation returned no images")
	}

	return parsed, modelID, apiBase, nil
}

func parseImageGenerationResponse(resp *http.Response, apiBase string) (*imageGenerationResponse, error) {
	contentType := resp.Header.Get("Content-Type")
	reader := bufio.NewReader(resp.Body)
	prefix, err := reader.Peek(256)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return nil, fmt.Errorf("failed to inspect image generation response: %w", err)
	}
	if common.LooksLikeHTML(prefix, contentType) {
		return nil, common.WrapHTMLResponseError(resp.StatusCode, prefix, contentType, apiBase)
	}

	var parsed imageGenerationResponse
	if err := json.NewDecoder(reader).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to parse image generation response: %w", err)
	}
	return &parsed, nil
}

func (t *GenerateImageTool) persistGeneratedImages(
	ctx context.Context,
	channel, chatID, modelName, modelID string,
	reqBody map[string]any,
	resp *imageGenerationResponse,
) ([]string, string, error) {
	if err := os.MkdirAll(media.TempDir(), 0o700); err != nil {
		return nil, "", fmt.Errorf("failed to create media temp dir: %w", err)
	}

	scope := fmt.Sprintf("tool:generate_image:%s:%s:%s", channel, chatID, uuid.NewString())
	prompt, _ := reqBody["prompt"].(string)
	outputFormat, _ := reqBody["output_format"].(string)
	responseFormat, _ := reqBody["response_format"].(string)

	refs := make([]string, 0, len(resp.Data))
	revisedPrompts := make([]string, 0, len(resp.Data))
	for i, item := range resp.Data {
		localPath, contentType, filename, err := t.materializeGeneratedImage(ctx, item, outputFormat, responseFormat, i)
		if err != nil {
			return nil, "", err
		}

		ref, err := t.mediaStore.Store(localPath, media.MediaMeta{
			Filename:      filename,
			ContentType:   contentType,
			Source:        "tool:generate_image",
			CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
		}, scope)
		if err != nil {
			return nil, "", fmt.Errorf("failed to register generated image: %w", err)
		}
		refs = append(refs, ref)

		if revised := strings.TrimSpace(item.RevisedPrompt); revised != "" {
			revisedPrompts = append(revisedPrompts, revised)
		}
	}

	var llmNote strings.Builder
	fmt.Fprintf(&llmNote, "Generated %d image(s) with default image-generation model %q (%s).", len(refs), modelName, modelID)
	if strings.TrimSpace(prompt) != "" {
		llmNote.WriteString("\nPrompt: ")
		llmNote.WriteString(strings.TrimSpace(prompt))
	}
	if len(revisedPrompts) > 0 {
		llmNote.WriteString("\nRevised prompt(s):")
		for _, revised := range revisedPrompts {
			llmNote.WriteString("\n- ")
			llmNote.WriteString(revised)
		}
	}

	return refs, llmNote.String(), nil
}

func (t *GenerateImageTool) materializeGeneratedImage(
	ctx context.Context,
	item imageGenerationData,
	outputFormat, responseFormat string,
	index int,
) (string, string, string, error) {
	if strings.TrimSpace(item.B64JSON) != "" {
		data, err := base64.StdEncoding.DecodeString(item.B64JSON)
		if err != nil {
			return "", "", "", fmt.Errorf("failed to decode generated image %d: %w", index+1, err)
		}
		return writeGeneratedImage(data, outputFormat, index)
	}

	if strings.TrimSpace(item.URL) == "" {
		return "", "", "", fmt.Errorf("generated image %d did not include %s data", index+1, strings.TrimSpace(responseFormat))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.URL, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to create download request for generated image %d: %w", index+1, err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to download generated image %d: %w", index+1, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("failed to download generated image %d: status %d", index+1, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(config.DefaultMaxMediaSize)))
	if err != nil {
		return "", "", "", fmt.Errorf("failed to read generated image %d: %w", index+1, err)
	}
	return writeGeneratedImage(data, outputFormat, index)
}

func writeGeneratedImage(data []byte, outputFormat string, index int) (string, string, string, error) {
	if len(data) == 0 {
		return "", "", "", fmt.Errorf("generated image %d is empty", index+1)
	}

	ext := normalizeImageOutputFormat(outputFormat)
	pattern := fmt.Sprintf("image-gen-*-%d.%s", index+1, ext)
	file, err := os.CreateTemp(media.TempDir(), pattern)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to create temp file for generated image %d: %w", index+1, err)
	}

	path := file.Name()
	if _, err := file.Write(data); err != nil {
		file.Close()
		_ = os.Remove(path)
		return "", "", "", fmt.Errorf("failed to write generated image %d: %w", index+1, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", "", "", fmt.Errorf("failed to finalize generated image %d: %w", index+1, err)
	}

	contentType := detectMediaType(path)
	if !strings.HasPrefix(contentType, "image/") {
		_ = os.Remove(path)
		return "", "", "", fmt.Errorf("generated file %d is not an image (detected %s)", index+1, contentType)
	}

	filename := filepath.Base(path)
	return path, contentType, filename, nil
}

func stringArg(raw any, allowed map[string]struct{}, field string) (string, error) {
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", field)
	}
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "", nil
	}
	if _, ok := allowed[value]; !ok {
		return "", fmt.Errorf("%s has unsupported value %q", field, value)
	}
	return value, nil
}

func intArg(raw any, field string) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int32:
		return int(v), nil
	case int64:
		return int(v), nil
	case float64:
		if float64(int(v)) != v {
			return 0, fmt.Errorf("%s must be an integer", field)
		}
		return int(v), nil
	default:
		return 0, fmt.Errorf("%s must be an integer", field)
	}
}

func normalizeImageOutputFormat(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "jpeg":
		return "jpg"
	case "png", "webp":
		return value
	default:
		return "png"
	}
}

func timeDurationSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		return common.DefaultRequestTimeout
	}
	return time.Duration(seconds) * time.Second
}
