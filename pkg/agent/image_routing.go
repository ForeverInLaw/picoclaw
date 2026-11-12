package agent

import (
	"net/url"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// requestHasImageInput reports whether any message carries an image the
// provider must transmit: an inline data URL or a remote http(s) URL.
func requestHasImageInput(messages []providers.Message) bool {
	for _, message := range messages {
		for _, media := range message.Media {
			if strings.HasPrefix(media, "data:image/") {
				return true
			}
			parsed, err := url.Parse(media)
			if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" {
				return true
			}
		}
	}
	return false
}

// candidateAcceptsImages requires both the model's configured flag and an
// adapter that can carry image payloads over its transport.
func candidateAcceptsImages(mc *config.ModelConfig, client providers.LLMProvider) bool {
	if capable, ok := client.(providers.ImageInputCapable); ok && !capable.SupportsImageInput() {
		return false
	}
	return mc.AcceptsImages()
}

// imageCapableCandidates keeps, in fallback order, the candidates whose
// configured model accepts images.
func imageCapableCandidates(
	cfg *config.Config,
	workspace string,
	candidates []providers.FallbackCandidate,
) []providers.FallbackCandidate {
	var capable []providers.FallbackCandidate
	for _, candidate := range candidates {
		mc, err := modelConfigForCandidate(cfg, candidate, workspace)
		if err != nil {
			logger.DebugCF("agent", "Image routing: candidate has no model configuration",
				map[string]any{"model": candidate.Model, "error": err.Error()})
			continue
		}
		if mc.AcceptsImages() {
			capable = append(capable, candidate)
		}
	}
	return capable
}

// selectImageCandidates serves an image turn from the agent's own chain when
// any of its models reads images, and from the image_model chain otherwise.
// With no image-capable model anywhere it keeps the primary chain, whose
// request check then reports the missing capability to the user.
func selectImageCandidates(
	cfg *config.Config,
	agent *AgentInstance,
) (providers.LLMProvider, []providers.FallbackCandidate, string) {
	if capable := imageCapableCandidates(cfg, agent.Workspace, agent.Candidates); len(capable) > 0 {
		return agent.Provider, capable, resolvedCandidateModel(capable, agent.Model)
	}
	if agent.ImageProvider != nil {
		if capable := imageCapableCandidates(cfg, agent.Workspace, agent.ImageCandidates); len(capable) > 0 {
			logger.InfoCF("agent", "Model routing: image model selected",
				map[string]any{"agent_id": agent.ID, "image_model": agent.ImageModel})
			return agent.ImageProvider, capable, resolvedCandidateModel(capable, agent.ImageModel)
		}
	}
	return agent.Provider, agent.Candidates, resolvedCandidateModel(agent.Candidates, agent.Model)
}
