package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/media"
)

type fakeTranscriber struct {
	resp *asr.TranscriptionResponse
	err  error
	path string
}

func (f *fakeTranscriber) Name() string {
	return "fake"
}

func (f *fakeTranscriber) Transcribe(_ context.Context, audioFilePath string) (*asr.TranscriptionResponse, error) {
	f.path = audioFilePath
	return f.resp, f.err
}

func TestTranscribeMediaTool_PathSource(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "clip.ogg")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o644); err != nil {
		t.Fatalf("failed to write audio file: %v", err)
	}

	tr := &fakeTranscriber{
		resp: &asr.TranscriptionResponse{
			Text:     "hello world",
			Language: "en-US",
			Duration: 1.5,
		},
	}
	tool := NewTranscribeMediaTool(dir, true, nil, tr)

	result := tool.Execute(context.Background(), map[string]any{"source": audioPath})
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if !result.Silent {
		t.Fatal("expected silent result")
	}
	if tr.path != audioPath {
		t.Fatalf("transcriber path = %q, want %q", tr.path, audioPath)
	}
	if !strings.Contains(result.ForLLM, "hello world") {
		t.Fatalf("expected transcript in result, got: %s", result.ForLLM)
	}
}

func TestTranscribeMediaTool_MediaRefSource(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "voice.ogg")
	if err := os.WriteFile(audioPath, []byte("audio"), 0o644); err != nil {
		t.Fatalf("failed to write audio file: %v", err)
	}

	store := media.NewFileMediaStore()
	ref, err := store.Store(audioPath, media.MediaMeta{Filename: "voice.ogg", ContentType: "audio/ogg"}, "scope")
	if err != nil {
		t.Fatalf("store.Store() error: %v", err)
	}

	tr := &fakeTranscriber{
		resp: &asr.TranscriptionResponse{Text: "media transcript"},
	}
	tool := NewTranscribeMediaTool(dir, true, store, tr)

	result := tool.Execute(context.Background(), map[string]any{"source": ref})
	if result.IsError {
		t.Fatalf("expected success, got error: %s", result.ForLLM)
	}
	if tr.path != audioPath {
		t.Fatalf("transcriber path = %q, want %q", tr.path, audioPath)
	}
}

func TestTranscribeMediaTool_NoTranscriber(t *testing.T) {
	tool := NewTranscribeMediaTool(t.TempDir(), true, nil, nil)

	result := tool.Execute(context.Background(), map[string]any{"source": "clip.ogg"})
	if !result.IsError {
		t.Fatal("expected error when transcriber is missing")
	}
}
