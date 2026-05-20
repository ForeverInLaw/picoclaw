package facts

import (
	"errors"
	"testing"
)

func TestConfig_DisabledIsValid(t *testing.T) {
	if err := (Config{Enabled: false}).Validate(); err != nil {
		t.Fatalf("disabled config should validate: %v", err)
	}
}

func TestConfig_RequiresFields(t *testing.T) {
	err := (Config{Enabled: true}).Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig, got %v", err)
	}
}

func TestConfig_HappyPath(t *testing.T) {
	c := Config{
		Enabled:         true,
		ExtractionModel: "x",
		EmbeddingModel:  "y",
		EmbeddingDim:    1536,
		TopK:            8,
		RecallMinScore:  0.55,
		SQLitePath:      "/tmp/facts.db",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("happy: %v", err)
	}
}
