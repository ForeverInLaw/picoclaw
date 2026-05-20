package facts

import (
	"fmt"
	"time"
)

// Config configures the facts memory subsystem. It is consumed by
// Bootstrap and lives inside the agent config under
// agents.memory.facts.
type Config struct {
	Enabled               bool     `json:"enabled" yaml:"enabled"`
	ChannelScope          []string `json:"channel_scope" yaml:"channel_scope"`
	ExtractionModel       string   `json:"extraction_model" yaml:"extraction_model"`
	EmbeddingModel        string   `json:"embedding_model" yaml:"embedding_model"`
	EmbeddingDim          int      `json:"embedding_dim" yaml:"embedding_dim"`
	TopK                  int      `json:"top_k" yaml:"top_k"`
	RecallMinScore        float64  `json:"recall_min_score" yaml:"recall_min_score"`
	ExtractionWindowMsgs  int      `json:"extraction_window_msgs" yaml:"extraction_window_msgs"`
	ExtractionIdleSeconds int      `json:"extraction_idle_seconds" yaml:"extraction_idle_seconds"`
	TTLDefaultDays        int      `json:"ttl_default_days" yaml:"ttl_default_days"`
	DecayWindowDays       int      `json:"decay_window_days" yaml:"decay_window_days"`
	MinConfidence         float64  `json:"min_confidence" yaml:"min_confidence"`
	SQLitePath            string   `json:"sqlite_path" yaml:"sqlite_path"`
}

// Validate checks that the config is internally consistent. Caller should
// also verify embedding_dim against the resolved embedding model after
// provider routing.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.ExtractionModel == "" || c.EmbeddingModel == "" {
		return fmt.Errorf("%w: extraction_model and embedding_model must be set", ErrInvalidConfig)
	}
	if c.EmbeddingDim <= 0 {
		return fmt.Errorf("%w: embedding_dim must be > 0", ErrInvalidConfig)
	}
	if c.TopK <= 0 {
		return fmt.Errorf("%w: top_k must be > 0", ErrInvalidConfig)
	}
	if c.RecallMinScore < 0 || c.RecallMinScore > 1 {
		return fmt.Errorf("%w: recall_min_score must be in [0,1]", ErrInvalidConfig)
	}
	if c.SQLitePath == "" {
		return fmt.Errorf("%w: sqlite_path must be set", ErrInvalidConfig)
	}
	return nil
}

// DecayWindow returns DecayWindowDays as a time.Duration.
func (c Config) DecayWindow() time.Duration {
	return time.Duration(c.DecayWindowDays) * 24 * time.Hour
}
