package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type locationRecordingProvider struct {
	requests chan []providers.Message
}

func (p *locationRecordingProvider) GetDefaultModel() string { return "location-test" }

func (p *locationRecordingProvider) Chat(
	ctx context.Context,
	messages []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	select {
	case p.requests <- append([]providers.Message(nil), messages...):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &providers.LLMResponse{Content: "Location received."}, nil
}

func TestSharedLocationReachesAgentAndRetainsReplyTarget(t *testing.T) {
	scenarios := []struct {
		name        string
		message     telego.Message
		wantContent string
		wantChatID  string
	}{
		{
			name: "location only",
			message: telego.Message{
				MessageID: 19,
				Chat:      telego.Chat{ID: 42, Type: "private"},
				From:      &telego.User{ID: 42, FirstName: "Alice"},
				Location:  &telego.Location{Latitude: 51.50735123, Longitude: -0.12775887},
			},
			wantContent: "[User location: lat=51.507351, lng=-0.127759]",
			wantChatID:  "42",
		},
		{
			name: "location with text caption and topic reply",
			message: telego.Message{
				MessageID:       19,
				MessageThreadID: 9,
				Chat:            telego.Chat{ID: -123, Type: "supergroup", IsForum: true},
				From:            &telego.User{ID: 42, FirstName: "Alice"},
				Text:            "Meet me here",
				Caption:         "Near the entrance",
				Location:        &telego.Location{Latitude: -33.86881973, Longitude: 151.20929551},
				ReplyToMessage: &telego.Message{
					MessageID: 7,
					Text:      "Where should we meet?",
					From:      &telego.User{ID: 77, FirstName: "Bob"},
				},
			},
			wantContent: "[quoted message from Bob]: Where should we meet?\n\n" +
				"Meet me here\nNear the entrance\n[User location: lat=-33.868820, lng=151.209296]",
			wantChatID: "-123/9",
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			testLocationTurn(t, &scenario.message, scenario.wantContent, scenario.wantChatID)
		})
	}
}

func testLocationTurn(t *testing.T, msg *telego.Message, wantContent, wantChatID string) {
	t.Helper()
	ch, messageBus := newBatchingTestChannel(t, 30)
	defer messageBus.Close()
	provider := &locationRecordingProvider{requests: make(chan []providers.Message, 1)}
	cfg := &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace:         t.TempDir(),
				Model:             "location-test",
				MaxTokens:         4096,
				MaxToolIterations: 1,
			},
		},
	}
	loop := agent.NewAgentLoop(cfg, messageBus, provider)
	defer loop.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Error("agent did not stop")
		}
	}()

	require.NoError(t, ch.handleMessage(t.Context(), msg))

	select {
	case messages := <-provider.requests:
		require.NotEmpty(t, messages)
		require.Equal(t, "user", messages[len(messages)-1].Role)
		require.Contains(t, messages[len(messages)-1].Content, wantContent)
		require.NotContains(t, messages[len(messages)-1].Content, "[empty message]")
	case <-time.After(2 * time.Second):
		t.Fatal("shared location did not reach the provider")
	}
	select {
	case out := <-messageBus.OutboundChan():
		require.Equal(t, "telegram", out.Channel)
		require.Equal(t, wantChatID, out.ChatID)
		require.Equal(t, "19", out.ReplyToMessageID)
		require.Equal(t, "Location received.", out.Content)
	case <-time.After(2 * time.Second):
		t.Fatal("location response was not delivered")
	}
}
