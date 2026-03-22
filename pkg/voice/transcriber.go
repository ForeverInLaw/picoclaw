package voice

import (
	"context"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

type Transcriber interface {
	Name() string
	Transcribe(ctx context.Context, audioFilePath string) (*TranscriptionResponse, error)
}

type TranscriptionResponse struct {
	Text     string  `json:"text"`
	Language string  `json:"language,omitempty"`
	Duration float64 `json:"duration,omitempty"`
}

// DetectTranscriber inspects cfg and returns the appropriate Transcriber, or
// nil if no supported transcription provider is configured.
func DetectTranscriber(cfg *config.Config) Transcriber {
	if cfg.Voice.Riva.Enabled && cfg.Providers.Nvidia.APIKey != "" {
		return NewRivaTranscriber(cfg.Providers.Nvidia.APIKey, cfg.Voice.Riva)
	}

	if modelName := strings.TrimSpace(cfg.Voice.ModelName); modelName != "" {
		modelCfg, err := cfg.GetModelConfig(modelName)
		if err != nil {
			return nil
		}
		return NewAudioModelTranscriber(modelCfg)
	}

	// Direct Groq provider config takes priority.
	if key := cfg.Providers.Groq.APIKey; key != "" {
		return NewGroqTranscriber(key)
	}

	// Fall back to any model-list entry that uses the groq/ protocol.
	for _, mc := range cfg.ModelList {
		if strings.HasPrefix(mc.Model, "groq/") && mc.APIKey != "" {
			return NewGroqTranscriber(mc.APIKey)
		}
	}

	return nil
}
