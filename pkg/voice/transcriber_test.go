package voice

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/sipeed/picoclaw/pkg/config"
	rivapb "github.com/sipeed/picoclaw/pkg/voice/rivapb"
)

var _ Transcriber = (*RivaTranscriber)(nil)

func TestDetectTranscriber(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		wantNil  bool
		wantName string
	}{
		{
			name:    "no config",
			cfg:     &config.Config{},
			wantNil: true,
		},
		{
			name: "riva provider key",
			cfg: &config.Config{
				Providers: config.ProvidersConfig{
					Nvidia: config.ProviderConfig{APIKey: "nvapi-test"},
				},
				Voice: config.VoiceConfig{
					Riva: config.RivaVoiceConfig{
						Enabled:    true,
						Server:     "localhost:50051",
						FunctionID: "fn-123",
					},
				},
			},
			wantName: "riva",
		},
		{
			name: "groq provider key",
			cfg: &config.Config{
				Providers: config.ProvidersConfig{
					Groq: config.ProviderConfig{APIKey: "sk-groq-direct"},
				},
			},
			wantName: "groq",
		},
		{
			name: "voice model name selects audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-gemini"},
				ModelList: []config.ModelConfig{
					{ModelName: "voice-gemini", Model: "gemini/gemini-2.5-flash", APIKey: "sk-gemini-model"},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "groq via model list",
			cfg: &config.Config{
				ModelList: []config.ModelConfig{
					{Model: "openai/gpt-4o", APIKey: "sk-openai"},
					{Model: "groq/llama-3.3-70b", APIKey: "sk-groq-model"},
				},
			},
			wantName: "groq",
		},
		{
			name: "voice model name selects non-gemini audio model transcriber",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "voice-openai-audio"},
				ModelList: []config.ModelConfig{
					{ModelName: "voice-openai-audio", Model: "openai/gpt-4o-audio-preview", APIKey: "sk-openai"},
				},
			},
			wantName: "audio-model",
		},
		{
			name: "groq model list entry without key is skipped",
			cfg: &config.Config{
				ModelList: []config.ModelConfig{
					{Model: "groq/llama-3.3-70b", APIKey: ""},
				},
			},
			wantNil: true,
		},
		{
			name: "provider key takes priority over model list",
			cfg: &config.Config{
				Providers: config.ProvidersConfig{
					Groq: config.ProviderConfig{APIKey: "sk-groq-direct"},
				},
				ModelList: []config.ModelConfig{
					{Model: "groq/llama-3.3-70b", APIKey: "sk-groq-model"},
				},
			},
			wantName: "groq",
		},
		{
			name: "missing voice model name config returns nil",
			cfg: &config.Config{
				Voice: config.VoiceConfig{ModelName: "missing"},
				ModelList: []config.ModelConfig{
					{ModelName: "other", Model: "gemini/gemini-2.5-flash", APIKey: "sk-gemini-model"},
				},
			},
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := DetectTranscriber(tc.cfg)
			if tc.wantNil {
				if tr != nil {
					t.Errorf("DetectTranscriber() = %v, want nil", tr)
				}
				return
			}
			if tr == nil {
				t.Fatal("DetectTranscriber() = nil, want non-nil")
			}
			if got := tr.Name(); got != tc.wantName {
				t.Errorf("Name() = %q, want %q", got, tc.wantName)
			}
		})
	}
}

type testRivaSpeechRecognitionServer struct {
	rivapb.UnimplementedRivaSpeechRecognitionServer
	t *testing.T
}

func (s *testRivaSpeechRecognitionServer) Recognize(
	ctx context.Context,
	req *rivapb.RecognizeRequest,
) (*rivapb.RecognizeResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		s.t.Fatal("expected metadata")
	}
	if got := md.Get("function-id"); len(got) != 1 || got[0] != "fn-123" {
		s.t.Fatalf("unexpected function-id metadata: %#v", got)
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer nvapi-test" {
		s.t.Fatalf("unexpected authorization metadata: %#v", got)
	}
	if req.GetConfig().GetLanguageCode() != "multi" {
		s.t.Fatalf("unexpected language code: %s", req.GetConfig().GetLanguageCode())
	}
	if req.GetConfig().GetCustomConfiguration()["task"] != "translate" {
		s.t.Fatalf("unexpected custom configuration: %#v", req.GetConfig().GetCustomConfiguration())
	}
	if req.GetConfig().GetSampleRateHertz() != 16000 {
		s.t.Fatalf("unexpected sample rate: %d", req.GetConfig().GetSampleRateHertz())
	}
	if req.GetConfig().GetAudioChannelCount() != 1 {
		s.t.Fatalf("unexpected channel count: %d", req.GetConfig().GetAudioChannelCount())
	}
	if len(req.GetAudio()) == 0 {
		s.t.Fatal("expected audio bytes")
	}

	return &rivapb.RecognizeResponse{
		Results: []*rivapb.SpeechRecognitionResult{
			{
				AudioProcessed: 1.25,
				Alternatives: []*rivapb.SpeechRecognitionAlternative{
					{
						Transcript:   "hello from riva",
						LanguageCode: []string{"en-US"},
					},
				},
			},
		},
	}, nil
}

func TestRivaTranscriberTranscribe(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error: %v", err)
	}
	defer lis.Close()

	server := grpc.NewServer()
	rivapb.RegisterRivaSpeechRecognitionServer(server, &testRivaSpeechRecognitionServer{t: t})
	defer server.Stop()

	go func() {
		_ = server.Serve(lis)
	}()

	audioPath := filepath.Join(t.TempDir(), "sample.wav")
	if err := os.WriteFile(audioPath, buildTestWAV(), 0o644); err != nil {
		t.Fatalf("failed to write test wav: %v", err)
	}

	tr := NewRivaTranscriber("nvapi-test", config.RivaVoiceConfig{
		Enabled:             true,
		Server:              lis.Addr().String(),
		UseSSL:              false,
		FunctionID:          "fn-123",
		LanguageCode:        "multi",
		CustomConfiguration: "task:translate",
	})

	resp, err := tr.Transcribe(context.Background(), audioPath)
	if err != nil {
		t.Fatalf("Transcribe() error: %v", err)
	}
	if resp.Text != "hello from riva" {
		t.Fatalf("Text = %q, want %q", resp.Text, "hello from riva")
	}
	if resp.Language != "en-US" {
		t.Fatalf("Language = %q, want %q", resp.Language, "en-US")
	}
	if resp.Duration != 1.25 {
		t.Fatalf("Duration = %v, want 1.25", resp.Duration)
	}
}

func buildTestWAV() []byte {
	return []byte{
		'R', 'I', 'F', 'F', 40, 0, 0, 0,
		'W', 'A', 'V', 'E',
		'f', 'm', 't', ' ', 16, 0, 0, 0,
		1, 0,
		1, 0,
		0x80, 0x3e, 0, 0,
		0x00, 0x7d, 0, 0,
		2, 0,
		16, 0,
		'd', 'a', 't', 'a', 4, 0, 0, 0,
		0, 0, 0, 0,
	}
}
