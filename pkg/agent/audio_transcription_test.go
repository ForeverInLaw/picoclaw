package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type stubTranscriber struct {
	results map[string]string
	errors  map[string]error
}

func (s *stubTranscriber) Name() string { return "stub" }

func (s *stubTranscriber) Transcribe(_ context.Context, audioFilePath string) (*asr.TranscriptionResponse, error) {
	if err := s.errors[audioFilePath]; err != nil {
		return nil, err
	}
	if text, ok := s.results[audioFilePath]; ok {
		return &asr.TranscriptionResponse{Text: text}, nil
	}
	return nil, errors.New("missing stub result")
}

type recordingTranscriber struct {
	path string
	text string
	err  error
}

func (r *recordingTranscriber) Name() string { return "recording" }

func (r *recordingTranscriber) Transcribe(_ context.Context, audioFilePath string) (*asr.TranscriptionResponse, error) {
	r.path = audioFilePath
	if r.err != nil {
		return nil, r.err
	}
	return &asr.TranscriptionResponse{Text: r.text}, nil
}

func TestTranscribeAudioInMessage_RemovesTranscribedAudioRefs(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	oggPath := filepath.Join(dir, "voice.ogg")
	if err := os.WriteFile(oggPath, []byte("fake audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := store.Store(oggPath, media.MediaMeta{Filename: "voice.ogg", ContentType: "audio/ogg"}, "test")
	if err != nil {
		t.Fatal(err)
	}

	al := &AgentLoop{
		cfg:        &config.Config{},
		mediaStore: store,
		transcriber: &stubTranscriber{
			results: map[string]string{oggPath: "привет"},
			errors:  map[string]error{},
		},
	}

	msg := bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "1",
		Content: "[voice]",
		Media:   []string{ref},
	}

	got, hadAudio := al.transcribeAudioInMessage(context.Background(), msg)
	if !hadAudio {
		t.Fatal("expected hadAudio=true")
	}
	if got.Content != "[voice: привет]" {
		t.Fatalf("content=%q want %q", got.Content, "[voice: привет]")
	}
	if len(got.Media) != 0 {
		t.Fatalf("media=%v want empty", got.Media)
	}
}

func TestTranscribeAudioInMessage_KeepsFailedAudioRefs(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	okPath := filepath.Join(dir, "ok.ogg")
	failPath := filepath.Join(dir, "fail.ogg")
	if err := os.WriteFile(okPath, []byte("ok audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failPath, []byte("bad audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	okRef, err := store.Store(okPath, media.MediaMeta{Filename: "ok.ogg", ContentType: "audio/ogg"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	failRef, err := store.Store(failPath, media.MediaMeta{Filename: "fail.ogg", ContentType: "audio/ogg"}, "test")
	if err != nil {
		t.Fatal(err)
	}

	al := &AgentLoop{
		cfg:        &config.Config{},
		mediaStore: store,
		transcriber: &stubTranscriber{
			results: map[string]string{okPath: "ok text"},
			errors:  map[string]error{failPath: errors.New("boom")},
		},
	}

	msg := bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "1",
		Content: "[voice]\n[voice]",
		Media:   []string{okRef, failRef},
	}

	got, hadAudio := al.transcribeAudioInMessage(context.Background(), msg)
	if !hadAudio {
		t.Fatal("expected hadAudio=true")
	}
	if got.Content != "[voice: ok text]\n[voice]" {
		t.Fatalf("content=%q want %q", got.Content, "[voice: ok text]\n[voice]")
	}
	if len(got.Media) != 1 || got.Media[0] != failRef {
		t.Fatalf("media=%v want [%q]", got.Media, failRef)
	}
}

func TestTranscribeAudioInMessage_BatchedTextAndMultipleVoices(t *testing.T) {
	store := media.NewFileMediaStore()
	dir := t.TempDir()

	firstPath := filepath.Join(dir, "first.ogg")
	secondPath := filepath.Join(dir, "second.ogg")
	if err := os.WriteFile(firstPath, []byte("first audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	firstRef, err := store.Store(firstPath, media.MediaMeta{Filename: "first.ogg", ContentType: "audio/ogg"}, "batch")
	if err != nil {
		t.Fatal(err)
	}
	secondRef, err := store.Store(secondPath, media.MediaMeta{Filename: "second.ogg", ContentType: "audio/ogg"}, "batch")
	if err != nil {
		t.Fatal(err)
	}

	al := &AgentLoop{
		cfg:        &config.Config{},
		mediaStore: store,
		transcriber: &stubTranscriber{
			results: map[string]string{
				firstPath:  "первый транскрипт",
				secondPath: "второй транскрипт",
			},
			errors: map[string]error{},
		},
	}

	msg := bus.InboundMessage{
		Channel: "telegram",
		ChatID:  "1",
		Content: "текст перед голосовыми\n\n[voice]\n\n[forwarded from Bob]: [voice]",
		Media:   []string{firstRef, secondRef},
		Metadata: map[string]string{
			"batch_count":       "3",
			"batch_message_ids": "801,802,803",
		},
	}

	got, hadAudio := al.transcribeAudioInMessage(context.Background(), msg)
	if !hadAudio {
		t.Fatal("expected hadAudio=true")
	}
	want := "текст перед голосовыми\n\n[voice: первый транскрипт]\n\n[forwarded from Bob]: [voice: второй транскрипт]"
	if got.Content != want {
		t.Fatalf("content=%q want %q", got.Content, want)
	}
	if len(got.Media) != 0 {
		t.Fatalf("media=%v want empty", got.Media)
	}
	if got.Metadata["batch_count"] != "3" {
		t.Fatalf("batch metadata lost: %#v", got.Metadata)
	}
}

func TestSetTranscriber_RegistersTranscribeMediaTool(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Tools.TranscribeMedia.Enabled = true

	registry := NewAgentRegistry(cfg, nil)
	al := &AgentLoop{
		cfg:      cfg,
		registry: registry,
	}

	al.SetTranscriber(&stubTranscriber{
		results: map[string]string{},
		errors:  map[string]error{},
	})
	al.SetMediaStore(media.NewFileMediaStore())

	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		t.Fatal("expected default agent")
	}

	tool, ok := defaultAgent.Tools.Get("transcribe_media")
	if !ok {
		t.Fatal("expected transcribe_media tool to be registered")
	}
	if _, ok := tool.(*tools.TranscribeMediaTool); !ok {
		t.Fatalf("expected *tools.TranscribeMediaTool, got %T", tool)
	}
}

func TestTranscribeAudioInMessage_TranscribesVideoAnnotationWhenMediaIsAudioCompatible(t *testing.T) {
	dir := t.TempDir()
	videoPath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("audio-compatible video fixture"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	store := media.NewFileMediaStore()
	ref, err := store.Store(videoPath, media.MediaMeta{Filename: "clip.ogg", ContentType: "audio/ogg"}, "scope")
	if err != nil {
		t.Fatalf("store media: %v", err)
	}

	al := &AgentLoop{
		cfg:        &config.Config{},
		mediaStore: store,
		transcriber: &stubTranscriber{
			results: map[string]string{videoPath: "видео текст"},
			errors:  map[string]error{},
		},
	}
	msg := bus.InboundMessage{Content: "смотри\n[video]", Media: []string{ref}}

	got, hadAudio := al.transcribeAudioInMessage(context.Background(), msg)
	if !hadAudio {
		t.Fatal("expected hadAudio=true")
	}
	if got.Content != "смотри\n[video: видео текст]" {
		t.Fatalf("content=%q", got.Content)
	}
	if len(got.Media) != 0 {
		t.Fatalf("media should be consumed after transcription, got %#v", got.Media)
	}
}

func TestTranscribeAudioInMessage_DeletesConvertedVideoAudioAfterTranscriptionError(t *testing.T) {
	pathDir := t.TempDir()
	fakeFFmpeg := filepath.Join(pathDir, "ffmpeg")
	if runtime.GOOS == "windows" {
		fakeFFmpeg += ".bat"
	}

	var script string
	if runtime.GOOS == "windows" {
		script = "@echo off\r\nset last=\r\n:loop\r\nif \"%~1\"==\"\" goto done\r\nset last=%~1\r\nshift\r\ngoto loop\r\n:done\r\necho fake audio>\"%last%\"\r\nexit /b 0\r\n"
	} else {
		script = "#!/bin/sh\nfor last do :; done\nprintf 'fake audio' > \"$last\"\n"
	}
	if err := os.WriteFile(fakeFFmpeg, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+oldPath)

	dir := t.TempDir()
	videoPath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(videoPath, []byte("fake video"), 0o644); err != nil {
		t.Fatalf("write video fixture: %v", err)
	}

	store := media.NewFileMediaStore()
	ref, err := store.Store(videoPath, media.MediaMeta{Filename: "clip.mp4", ContentType: "video/mp4"}, "scope")
	if err != nil {
		t.Fatalf("store media: %v", err)
	}
	recorder := &recordingTranscriber{err: errors.New("asr boom")}
	al := &AgentLoop{
		cfg:         &config.Config{},
		mediaStore:  store,
		transcriber: recorder,
	}

	got, hadAudio := al.transcribeAudioInMessage(context.Background(), bus.InboundMessage{Content: "[video]", Media: []string{ref}})
	if hadAudio {
		t.Fatal("expected failed transcription to return hadAudio=false")
	}
	if got.Content != "[video]" {
		t.Fatalf("content=%q", got.Content)
	}
	if recorder.path == "" {
		t.Fatal("expected converted audio path to be passed to transcriber")
	}
	if _, err := os.Stat(recorder.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("converted temp audio still exists or stat failed unexpectedly: path=%q err=%v", recorder.path, err)
	}
}
