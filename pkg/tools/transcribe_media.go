package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/media"
)

type TranscribeMediaTool struct {
	workspace      string
	restrict       bool
	mediaStore     media.MediaStore
	transcriber    asr.Transcriber
	allowReadPaths []*regexp.Regexp
}

func NewTranscribeMediaTool(
	workspace string,
	restrict bool,
	store media.MediaStore,
	transcriber asr.Transcriber,
	allowReadPaths ...[]*regexp.Regexp,
) *TranscribeMediaTool {
	var patterns []*regexp.Regexp
	if len(allowReadPaths) > 0 {
		patterns = allowReadPaths[0]
	}
	return &TranscribeMediaTool{
		workspace:      workspace,
		restrict:       restrict,
		mediaStore:     store,
		transcriber:    transcriber,
		allowReadPaths: patterns,
	}
}

func (t *TranscribeMediaTool) Name() string {
	return "transcribe_media"
}

func (t *TranscribeMediaTool) Description() string {
	return "Transcribe audio media or voice files to text. Supports media:// refs and local file paths."
}

func (t *TranscribeMediaTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source": map[string]any{
				"type":        "string",
				"description": "A media:// ref or local file path pointing to an audio file.",
			},
		},
		"required": []string{"source"},
	}
}

func (t *TranscribeMediaTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t.transcriber == nil {
		return ErrorResult("transcription is not configured")
	}

	source, ok := args["source"].(string)
	if !ok || strings.TrimSpace(source) == "" {
		return ErrorResult("source is required")
	}

	audioPath, err := t.resolveSource(strings.TrimSpace(source))
	if err != nil {
		return ErrorResult(err.Error()).WithError(err)
	}

	resp, err := t.transcriber.Transcribe(ctx, audioPath)
	if err != nil {
		return ErrorResult(fmt.Sprintf("transcription failed: %v", err)).WithError(err)
	}

	if resp == nil || strings.TrimSpace(resp.Text) == "" {
		return ErrorResult("transcription returned empty text")
	}

	var b strings.Builder
	b.WriteString("Transcription:\n")
	b.WriteString(strings.TrimSpace(resp.Text))
	if resp.Language != "" {
		b.WriteString("\n\nLanguage: ")
		b.WriteString(resp.Language)
	}
	if resp.Duration > 0 {
		b.WriteString(fmt.Sprintf("\nDuration: %.2fs", resp.Duration))
	}
	b.WriteString("\nSource: ")
	b.WriteString(filepath.Base(audioPath))

	return SilentResult(b.String())
}

func (t *TranscribeMediaTool) resolveSource(source string) (string, error) {
	if strings.HasPrefix(source, "media://") {
		if t.mediaStore == nil {
			return "", fmt.Errorf("media store is not available")
		}
		localPath, _, err := t.mediaStore.ResolveWithMeta(source)
		if err != nil {
			return "", fmt.Errorf("failed to resolve media ref: %w", err)
		}
		return localPath, nil
	}

	return validatePathWithAllowPaths(source, t.workspace, t.restrict, t.allowReadPaths)
}
