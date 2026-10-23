package tools

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// GenerateImageTool creates or edits one image and returns it through normal
// outbound media delivery. References use the same file boundaries as read_file.
type GenerateImageTool struct {
	workspace   string
	fs          fileSystem
	client      *providers.CodexImageClient
	sender      *SendFileTool
	attachments *AttachmentStore
	artifacts   *ArtifactStore
}

func NewGenerateImageTool(workspace string, restrict bool, maxBytes int, accountID string,
	allowPaths []*regexp.Regexp, artifacts *ArtifactStore, attachments *AttachmentStore,
) *GenerateImageTool {
	return &GenerateImageTool{
		workspace: workspace, fs: buildFs(workspace, restrict, allowPaths),
		client:    providers.NewCodexImageClient(accountID, maxBytes),
		sender:    NewSendFileTool(workspace, restrict, maxBytes, nil),
		artifacts: artifacts, attachments: attachments,
	}
}

func (t *GenerateImageTool) Name() string { return "generate_image" }

func (t *GenerateImageTool) Description() string {
	return "Generate or edit one image with native Codex image generation. Sends the result to the current chat. Use only when the user requests an image; may consume the configured Codex account's image allowance."
}

func (t *GenerateImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{"type": "string", "description": "Detailed description of the desired image or edits."},
			"images": map[string]any{
				"type": "array", "maxItems": providers.MaxCodexImageInputs,
				"items":       map[string]any{"type": "string"},
				"description": "Optional PNG/JPEG/WebP references for editing: attachment:// references from this conversation or allowed local file paths. Omit to generate a new image.",
			},
			"transparent_background": map[string]any{"type": "boolean", "description": "Request transparency, for example for a cutout or background removal."},
		},
		"required": []string{"prompt"},
	}
}

func (t *GenerateImageTool) SetMediaStore(store media.MediaStore) {
	t.sender.SetMediaStore(store)
}

func (t *GenerateImageTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	scope := ToolRequestScope(ctx)
	if scope.Inline || scope.NoHistory {
		return ErrorResult("image generation is unavailable in inline or no-history requests; use a regular chat")
	}
	if ToolChannel(ctx) == "" || ToolChatID(ctx) == "" || t.sender.mediaStore == nil {
		return ErrorResult("image delivery requires a current chat and media store")
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return ErrorResult("prompt is required")
	}
	transparent := false
	if value, exists := args["transparent_background"]; exists {
		var ok bool
		transparent, ok = value.(bool)
		if !ok {
			return ErrorResult("transparent_background must be a boolean")
		}
	}
	var images [][]byte
	if value, exists := args["images"]; exists {
		references, ok := value.([]any)
		if !ok || len(references) > providers.MaxCodexImageInputs {
			return ErrorResult("images must be an array with at most five references")
		}
		total := 0
		for _, reference := range references {
			path, ok := reference.(string)
			if !ok || strings.TrimSpace(path) == "" {
				return ErrorResult("each image reference must be a nonempty string")
			}
			data, err := t.readImage(ctx, path, providers.MaxCodexImageBytes-total)
			if err != nil {
				return ErrorResult(err.Error())
			}
			total += len(data)
			images = append(images, data)
		}
	}

	// Reserve a private, unique output before spending an image request.
	file, err := os.CreateTemp(t.workspace, "generated-image-*")
	if err != nil {
		return ErrorResult("cannot create an image file in the workspace")
	}
	path := file.Name()
	keep := false
	defer func() {
		file.Close()
		if !keep {
			os.Remove(path)
		}
	}()
	data, err := t.client.Generate(ctx, prompt, images, transparent)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if _, err = file.Write(data); err != nil {
		return ErrorResult("cannot save generated image")
	}
	if err = file.Close(); err != nil {
		return ErrorResult("cannot finish saving generated image")
	}
	extension := ".png"
	switch http.DetectContentType(data) {
	case "image/jpeg":
		extension = ".jpg"
	case "image/webp":
		extension = ".webp"
	}
	result := t.sender.Execute(ctx, map[string]any{"path": path, "filename": filepath.Base(path) + extension})
	if result.IsError {
		return result
	}
	keep = true
	result.ForLLM = fmt.Sprintf("Image generated and queued for delivery to the current chat. Saved at %s. Do not send it again.", path)
	return result
}

func (t *GenerateImageTool) readImage(ctx context.Context, path string, maxBytes int) ([]byte, error) {
	var file fs.File
	var err error
	if strings.HasPrefix(path, AttachmentPrefix) {
		file, err = t.attachments.OpenOriginal(ToolRequestScope(ctx), path)
	} else if t.artifacts.ProtectPath(path) || t.artifacts.ProtectPath(filepath.Join(t.workspace, path)) ||
		t.attachments.ProtectPath(path) || t.attachments.ProtectPath(filepath.Join(t.workspace, path)) {
		return nil, fmt.Errorf("image access denied: use an authorized attachment reference")
	} else {
		// hostFs uses absolute paths; sandboxFs resolves paths against the workspace.
		if !filepath.IsAbs(path) {
			path = filepath.Join(t.workspace, path)
		}
		file, err = t.fs.Open(path)
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || maxBytes <= 0 || info.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("reference image is unavailable or exceeds the 32 MiB total limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxBytes {
		return nil, fmt.Errorf("reference image is empty or exceeds the 32 MiB total limit")
	}
	switch http.DetectContentType(data) {
	case "image/png", "image/jpeg", "image/webp":
		return data, nil
	default:
		return nil, fmt.Errorf("reference images must be PNG, JPEG or WebP")
	}
}
