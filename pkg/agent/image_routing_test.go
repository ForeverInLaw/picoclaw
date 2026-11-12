package agent

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func imageRoutingConfig(models map[string]bool) *config.Config {
	cfg := &config.Config{}
	for name, images := range models {
		cfg.ModelList = append(cfg.ModelList, config.ModelConfig{
			ModelName:    name,
			Model:        "openai/" + name,
			Capabilities: &config.ModelCapabilities{Images: &images},
		})
	}
	return cfg
}

func candidate(name string) providers.FallbackCandidate {
	return providers.FallbackCandidate{Provider: "openai", Model: name, ConfigName: name}
}

func imageRoutingAgent() *AgentInstance {
	return &AgentInstance{
		ID:              "main",
		Model:           "text",
		Provider:        &mockProvider{},
		Candidates:      []providers.FallbackCandidate{candidate("text"), candidate("vision")},
		ImageModel:      "image",
		ImageProvider:   &recordingProvider{},
		ImageCandidates: []providers.FallbackCandidate{candidate("image")},
	}
}

var imageTurn = []providers.Message{
	{Role: "user", Content: "what is this?", Media: []string{"data:image/png;base64,abc"}},
}

func TestImageTurnKeepsOnlyImageCapableModelsFromPrimaryChain(t *testing.T) {
	cfg := imageRoutingConfig(map[string]bool{"text": false, "vision": true, "image": true})
	agent := imageRoutingAgent()

	provider, candidates, model := (&AgentLoop{}).selectCandidates(cfg, agent, "what is this?", imageTurn)

	// The primary provider served "text", so "vision" resolves its own provider.
	if provider != nil || model != "vision" || len(candidates) != 1 || candidates[0].Model != "vision" {
		t.Fatalf("got provider %T, model %q, candidates %#v; want vision alone with its own provider", provider, model, candidates)
	}
}

func TestImageTurnUsesImageModelWhenPrimaryChainIsTextOnly(t *testing.T) {
	cfg := imageRoutingConfig(map[string]bool{"text": false, "vision": false, "image": true})
	agent := imageRoutingAgent()

	provider, candidates, model := (&AgentLoop{}).selectCandidates(cfg, agent, "what is this?", imageTurn)

	if provider != agent.ImageProvider || model != "image" || len(candidates) != 1 {
		t.Fatalf("got provider %T, model %q, candidates %#v; want image chain", provider, model, candidates)
	}
}

func TestImageTurnWithoutImageCapableModelKeepsPrimaryChain(t *testing.T) {
	cfg := imageRoutingConfig(map[string]bool{"text": false, "vision": false, "image": false})
	agent := imageRoutingAgent()

	provider, candidates, _ := (&AgentLoop{}).selectCandidates(cfg, agent, "what is this?", imageTurn)

	if provider != agent.Provider || len(candidates) != 2 {
		t.Fatalf("got provider %T, candidates %#v; want unchanged primary chain", provider, candidates)
	}
}

func TestFileTurnKeepsPrimaryChain(t *testing.T) {
	cfg := imageRoutingConfig(map[string]bool{"text": false, "vision": true, "image": true})
	agent := imageRoutingAgent()
	messages := []providers.Message{{Role: "user", Content: "please read this [file:/tmp/report.pdf]"}}

	provider, candidates, model := (&AgentLoop{}).selectCandidates(cfg, agent, "please read this", messages)

	if provider != agent.Provider || model != "text" || len(candidates) != 2 {
		t.Fatalf("got provider %T, model %q, candidates %#v; want full primary chain", provider, model, candidates)
	}
}

func TestCandidateWithoutImagesFlagRejectsImageRequest(t *testing.T) {
	cfg := &config.Config{ModelList: []config.ModelConfig{{ModelName: "plain", Model: "openai/plain"}}}

	err := checkCandidateContext(cfg, candidate("plain"), &mockProvider{}, imageTurn, nil, nil, 0, 0)

	if err == nil {
		t.Fatal("model without capabilities.images accepted an image request")
	}
}
