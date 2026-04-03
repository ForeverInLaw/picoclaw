package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestNewAgentInstance_UsesDefaultsTemperatureAndMaxTokens(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 1.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)
	t.Cleanup(func() { _ = agent.Close() })

	if agent.MaxTokens != 1234 {
		t.Fatalf("MaxTokens = %d, want %d", agent.MaxTokens, 1234)
	}
	if agent.Temperature != 1.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 1.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenZero(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	configuredTemp := 0.0
	cfg.Agents.Defaults.Temperature = &configuredTemp

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)
	t.Cleanup(func() { _ = agent.Close() })

	if agent.Temperature != 0.0 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.0)
	}
}

func TestNewAgentInstance_DefaultsTemperatureWhenUnset(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         tmpDir,
				ModelName:         "test-model",
				MaxTokens:         1234,
				MaxToolIterations: 5,
			},
		},
	}

	provider := &mockProvider{}
	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)
	t.Cleanup(func() { _ = agent.Close() })

	if agent.Temperature != 0.7 {
		t.Fatalf("Temperature = %f, want %f", agent.Temperature, 0.7)
	}
}

func TestNewAgentInstance_ResolveCandidatesFromModelListAlias(t *testing.T) {
	tests := []struct {
		name         string
		aliasName    string
		modelName    string
		apiBase      string
		wantProvider string
		wantModel    string
	}{
		{
			name:         "alias with provider prefix",
			aliasName:    "step-3.5-flash",
			modelName:    "openrouter/stepfun/step-3.5-flash:free",
			apiBase:      "https://openrouter.ai/api/v1",
			wantProvider: "openrouter",
			wantModel:    "stepfun/step-3.5-flash:free",
		},
		{
			name:         "alias without provider prefix",
			aliasName:    "glm-5",
			modelName:    "glm-5",
			apiBase:      "https://api.z.ai/api/coding/paas/v4",
			wantProvider: "openai",
			wantModel:    "glm-5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "agent-instance-test-*")
			if err != nil {
				t.Fatalf("Failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			cfg := &config.Config{
				Agents: config.AgentsConfig{
					Defaults: config.AgentDefaults{
						Workspace: tmpDir,
						ModelName: tt.aliasName,
					},
				},
				ModelList: config.SecureModelList{
					{
						ModelName: tt.aliasName,
						Model:     tt.modelName,
						APIBase:   tt.apiBase,
					},
				},
			}

			provider := &mockProvider{}
			agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, provider)
			t.Cleanup(func() { _ = agent.Close() })

			if len(agent.Candidates) != 1 {
				t.Fatalf("len(Candidates) = %d, want 1", len(agent.Candidates))
			}
			if agent.Candidates[0].Provider != tt.wantProvider {
				t.Fatalf("candidate provider = %q, want %q", agent.Candidates[0].Provider, tt.wantProvider)
			}
			if agent.Candidates[0].Model != tt.wantModel {
				t.Fatalf("candidate model = %q, want %q", agent.Candidates[0].Model, tt.wantModel)
			}
		})
	}
}

func TestNewAgentInstance_ConfiguresImageProviderFromModelList(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:   tmpDir,
				ModelName:   "text-model",
				ImageModel:  "vision-model",
				MaxTokens:   1024,
				Temperature: nil,
			},
		},
		ModelList: config.SecureModelList{
			{
				ModelName: "text-model",
				Model:     "openai/plain-text-model",
				APIKeys:   config.SimpleSecureStrings("text-key"),
				APIBase:   "https://text.example/v1",
			},
			{
				ModelName: "vision-model",
				Model:     "openai/vision-model",
				APIKeys:   config.SimpleSecureStrings("vision-key"),
				APIBase:   "https://vision.example/v1",
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	t.Cleanup(func() { _ = agent.Close() })

	if agent.ImageModel != "vision-model" {
		t.Fatalf("ImageModel = %q, want %q", agent.ImageModel, "vision-model")
	}
	if agent.ImageProvider == nil {
		t.Fatal("ImageProvider = nil, want configured provider")
	}
	if len(agent.ImageCandidates) != 1 {
		t.Fatalf("len(ImageCandidates) = %d, want 1", len(agent.ImageCandidates))
	}
	if agent.ImageCandidates[0] != (providers.FallbackCandidate{Provider: "openai", Model: "vision-model"}) {
		t.Fatalf("ImageCandidates[0] = %#v, want openai/vision-model", agent.ImageCandidates[0])
	}
}

func TestNewAgentInstance_AllowsMediaTempDirForReadListAndExec(t *testing.T) {
	workspace := t.TempDir()
	mediaDir := media.TempDir()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(mediaDir) error = %v", err)
	}

	mediaFile, err := os.CreateTemp(mediaDir, "instance-tool-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp(mediaDir) error = %v", err)
	}
	mediaPath := mediaFile.Name()
	if _, err := mediaFile.WriteString("attachment content"); err != nil {
		mediaFile.Close()
		t.Fatalf("WriteString(mediaFile) error = %v", err)
	}
	if err := mediaFile.Close(); err != nil {
		t.Fatalf("Close(mediaFile) error = %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(mediaPath) })

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:           workspace,
				ModelName:           "test-model",
				RestrictToWorkspace: true,
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
			ListDir:  config.ToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				AllowRemote:        true,
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	t.Cleanup(func() { _ = agent.Close() })

	readTool, ok := agent.Tools.Get("read_file")
	if !ok {
		t.Fatal("read_file tool not registered")
	}
	readResult := readTool.Execute(context.Background(), map[string]any{"path": mediaPath})
	if readResult.IsError {
		t.Fatalf("read_file should allow media temp dir, got: %s", readResult.ForLLM)
	}
	if !strings.Contains(readResult.ForLLM, "attachment content") {
		t.Fatalf("read_file output missing media content: %s", readResult.ForLLM)
	}

	listTool, ok := agent.Tools.Get("list_dir")
	if !ok {
		t.Fatal("list_dir tool not registered")
	}
	listResult := listTool.Execute(context.Background(), map[string]any{"path": mediaDir})
	if listResult.IsError {
		t.Fatalf("list_dir should allow media temp dir, got: %s", listResult.ForLLM)
	}
	if !strings.Contains(listResult.ForLLM, filepath.Base(mediaPath)) {
		t.Fatalf("list_dir output missing media file: %s", listResult.ForLLM)
	}

	execTool, ok := agent.Tools.Get("exec")
	if !ok {
		t.Fatal("exec tool not registered")
	}
	execResult := execTool.Execute(context.Background(), map[string]any{
		"command":     "cat " + filepath.Base(mediaPath),
		"working_dir": mediaDir,
	})
	if execResult.IsError {
		t.Fatalf("exec should allow media temp dir, got: %s", execResult.ForLLM)
	}
	if !strings.Contains(execResult.ForLLM, "attachment content") {
		t.Fatalf("exec output missing media content: %s", execResult.ForLLM)
	}
}

func TestNewAgentInstance_InvalidExecConfigDoesNotExit(t *testing.T) {
	workspace := t.TempDir()

	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: workspace,
				ModelName: "test-model",
			},
		},
		Tools: config.ToolsConfig{
			ReadFile: config.ReadFileToolConfig{Enabled: true},
			Exec: config.ExecConfig{
				ToolConfig:         config.ToolConfig{Enabled: true},
				EnableDenyPatterns: true,
				CustomDenyPatterns: []string{"[invalid-regex"},
			},
		},
	}

	agent := NewAgentInstance(nil, &cfg.Agents.Defaults, cfg, &mockProvider{})
	t.Cleanup(func() { _ = agent.Close() })
	if agent == nil {
		t.Fatal("expected agent instance, got nil")
	}

	if _, ok := agent.Tools.Get("exec"); ok {
		t.Fatal("exec tool should not be registered when exec config is invalid")
	}

	if _, ok := agent.Tools.Get("read_file"); !ok {
		t.Fatal("read_file tool should still be registered")
	}
}
