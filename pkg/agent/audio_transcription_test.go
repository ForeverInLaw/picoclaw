package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/voice"
)

type stubTranscriber struct {
	results map[string]string
	errors  map[string]error
}

func (s *stubTranscriber) Name() string { return "stub" }

func (s *stubTranscriber) Transcribe(_ context.Context, audioFilePath string) (*voice.TranscriptionResponse, error) {
	if err := s.errors[audioFilePath]; err != nil {
		return nil, err
	}
	if text, ok := s.results[audioFilePath]; ok {
		return &voice.TranscriptionResponse{Text: text}, nil
	}
	return nil, errors.New("missing stub result")
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
