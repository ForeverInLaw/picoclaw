package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type inlineModelTest struct {
	config.ModelConfig
	ModelIndex *int `json:"model_index"`
}

// Validation checks configuration only. It never constructs a provider or
// contacts the configured endpoint, so it cannot establish model availability.
func (h *Handler) handleValidateModel(w http.ResponseWriter, r *http.Request) {
	mc, ok := h.readInlineModelTest(w, r)
	if !ok {
		return
	}
	if err := validateModelTestConfiguration(mc); err != nil {
		modelTestError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).
		Encode(map[string]string{"status": "valid", "message": "Configuration is valid. No model request was sent."})
}

func (h *Handler) handleTestInlineModel(w http.ResponseWriter, r *http.Request) {
	mc, ok := h.readInlineModelTest(w, r)
	if !ok {
		return
	}
	h.runModelInference(w, r, mc)
}

func (h *Handler) readInlineModelTest(w http.ResponseWriter, r *http.Request) (config.ModelConfig, bool) {
	var request inlineModelTest
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil {
		modelTestError(w, http.StatusBadRequest, "Invalid model configuration JSON.")
		return config.ModelConfig{}, false
	}
	mc := request.ModelConfig
	if request.ModelIndex != nil {
		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			modelTestError(w, http.StatusInternalServerError, "Could not load the selected model configuration.")
			return mc, false
		}
		index := *request.ModelIndex
		if index < 0 || index >= len(cfg.ModelList) {
			modelTestError(w, http.StatusNotFound, "The selected model no longer exists. Refresh the model list.")
			return mc, false
		}
		saved := cfg.ModelList[index]
		if err := retainModelCredential(&mc, saved); err != nil {
			modelTestError(w, http.StatusBadRequest, err.Error())
			return mc, false
		}
		if mc.APIKey == "" && saved.APIKey != "" && !compatibleModelCredentials(mc, saved) {
			modelTestError(w, http.StatusBadRequest, "Enter a new API key for the edited provider, endpoint or proxy.")
			return mc, false
		}
	}
	if modelCredentialPlaceholder(mc.APIKey) || len(mc.APIKeys) != 0 {
		modelTestError(
			w,
			http.StatusBadRequest,
			"Provide one explicit API key or select a compatible saved model. Save file/encrypted credentials before testing them.",
		)
		return mc, false
	}
	return mc, true
}

// Saved probes select the indexed configuration directly, without alias
// resolution, key rotation, retries or fallback to another configured model.
func (h *Handler) handleTestSavedModel(w http.ResponseWriter, r *http.Request) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		modelTestError(w, http.StatusBadRequest, "Invalid model index.")
		return
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		modelTestError(w, http.StatusInternalServerError, "Could not load the selected model configuration.")
		return
	}
	if index < 0 || index >= len(cfg.ModelList) {
		modelTestError(w, http.StatusNotFound, "The selected model no longer exists. Refresh the model list.")
		return
	}
	h.runModelInference(w, r, cfg.ModelList[index])
}

func (h *Handler) runModelInference(w http.ResponseWriter, r *http.Request, mc config.ModelConfig) {
	if err := validateModelTestConfiguration(mc); err != nil {
		modelTestError(w, http.StatusBadRequest, err.Error())
		return
	}
	// These protocols can open subprocesses/connections or use global OAuth
	// credentials. This action only supports configured OpenAI-compatible HTTP.
	protocol, _ := providers.ExtractProtocol(mc.Model)
	switch protocol {
	case "azure", "azure-openai", "anthropic-messages", "coding-plan-anthropic", "alibaba-coding-anthropic",
		"antigravity", "claude-cli", "claudecli", "codex-cli", "codexcli", "github-copilot", "copilot":
		modelTestError(
			w,
			http.StatusBadRequest,
			"This test supports OpenAI-compatible HTTP models with an API key or local endpoint.",
		)
		return
	}
	if (mc.AuthMethod == "oauth" || mc.AuthMethod == "token") && protocol != "openai" {
		modelTestError(
			w,
			http.StatusBadRequest,
			"This test supports OpenAI-compatible HTTP models with an API key or local endpoint.",
		)
		return
	}
	provider, modelID, err := providers.CreateProviderFromConfig(&mc)
	if err != nil {
		modelTestError(w, http.StatusBadRequest, "Check the selected provider, model identifier, API base and API key.")
		return
	}
	if _, ok := provider.(*providers.HTTPProvider); !ok && protocol != "openai" {
		modelTestError(w, http.StatusBadRequest, "This test requires an OpenAI-compatible HTTP provider.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	started := time.Now()
	response, err := provider.Chat(ctx, []providers.Message{{Role: "user", Content: "Reply with OK."}}, nil, modelID,
		map[string]any{"max_tokens": 32})
	if err != nil {
		message := providers.ErrorGuidance(err)
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
			message = "The model test timed out. Check the endpoint's availability or try a faster model."
		}
		if message == "" {
			message = "The selected model could not answer. Check its provider and request settings."
		}
		modelTestError(w, http.StatusBadGateway, message)
		return
	}
	if response == nil || strings.TrimSpace(response.Content) == "" {
		modelTestError(
			w,
			http.StatusBadGateway,
			"The selected model returned no answer. Check its model identifier and request settings.",
		)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "success",
		"message":    "The selected model replied successfully.",
		"latency_ms": time.Since(started).Milliseconds(),
	})
}

func validateModelTestConfiguration(mc config.ModelConfig) error {
	if mc.Validate() != nil || strings.TrimSpace(mc.ModelName) == "" {
		return errors.New("A model name and model identifier are required.")
	}
	_, modelID := providers.ExtractProtocol(mc.Model)
	if strings.TrimSpace(modelID) == "" {
		return errors.New("A model identifier is required after the provider prefix.")
	}
	if mc.APIBase != "" {
		u, err := url.Parse(mc.APIBase)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New(
				"Use an absolute HTTP or HTTPS API base without URL credentials, query parameters or fragments.",
			)
		}
	}
	if mc.RequestTimeout < 0 {
		return errors.New("Request timeout cannot be negative.")
	}
	reserved := map[string]bool{
		"model": true, "messages": true, "tools": true, "tool_choice": true, "stream": true,
		"max_tokens": true, "max_completion_tokens": true, "max_output_tokens": true, "n": true,
	}
	if mc.MaxTokensField != "" && reserved[mc.MaxTokensField] && mc.MaxTokensField != "max_tokens" &&
		mc.MaxTokensField != "max_completion_tokens" &&
		mc.MaxTokensField != "max_output_tokens" {
		return errors.New("The max tokens field must not override the model, messages, tools or streaming settings.")
	}
	for field := range mc.ExtraBody {
		if reserved[field] || field == mc.MaxTokensField {
			return errors.New(
				"Extra body must not override the selected model, prompt, tools, streaming or token limit for this minimal test.",
			)
		}
	}
	return nil
}

func modelTestError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "error", "message": message})
}
