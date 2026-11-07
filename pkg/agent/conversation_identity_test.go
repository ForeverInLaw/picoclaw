package agent

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// identityRecorder answers every request and records the conversation identity
// each one carried, which Key pool providers bind to a key.
type identityRecorder struct {
	mu         sync.Mutex
	identities []string
}

func (r *identityRecorder) Chat(
	_ context.Context, _ []providers.Message, _ []providers.ToolDefinition, _ string, opts map[string]any,
) (*providers.LLMResponse, error) {
	identity, _ := opts[providers.ConversationIdentityOption].(string)
	r.mu.Lock()
	r.identities = append(r.identities, identity)
	r.mu.Unlock()
	return &providers.LLMResponse{Content: "Mock response"}, nil
}

func (r *identityRecorder) GetDefaultModel() string { return "mock-model" }

func (r *identityRecorder) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.identities)
}

func newIdentityLoop(t *testing.T) (*AgentLoop, *identityRecorder) {
	t.Helper()
	base, cfg, msgBus, _, cleanup := newTestAgentLoop(t)
	base.Close()
	msgBus.Close()
	t.Cleanup(cleanup)
	recorder := &identityRecorder{}
	messageBus := bus.NewMessageBus()
	al := NewAgentLoop(cfg, messageBus, recorder)
	t.Cleanup(func() { al.Close(); messageBus.Close() })
	return al, recorder
}

func assertIdentities(t *testing.T, got []string, want string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatal("no model request was made")
	}
	for i, identity := range got {
		if identity != want {
			t.Errorf("request %d carried conversation identity %q, want %q", i, identity, want)
		}
	}
}

func TestTurnsAndSubTurnsCarryTheirConversationsIdentity(t *testing.T) {
	al, recorder := newIdentityLoop(t)
	const conversation = "agent:main:telegram:direct:42"
	if _, err := al.ProcessDirectWithChannel(t.Context(), "hello", conversation, "telegram", "42"); err != nil {
		t.Fatalf("ProcessDirectWithChannel() error = %v", err)
	}
	agent := al.registry.GetDefaultAgent()
	parent := newTurnState(agent, processOptions{SessionKey: conversation, Channel: "telegram", ChatID: "42"},
		al.newTurnEventScope(agent.ID, conversation))

	if _, err := spawnSubTurn(t.Context(), al, parent, SubTurnConfig{Model: "test-model", Tools: []tools.Tool{}}); err != nil {
		t.Fatalf("spawnSubTurn() error = %v", err)
	}

	assertIdentities(t, recorder.recorded(), conversation)
}

func TestConversationSummaryCarriesItsConversationsIdentity(t *testing.T) {
	al, recorder := newIdentityLoop(t)
	const conversation = "agent:main:telegram:group:-100"
	agent := al.registry.GetDefaultAgent()
	for i := range 8 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		agent.Sessions.AddMessage(conversation, role, "a message long enough to be worth summarizing")
	}

	al.summarizeSession(t.Context(), agent, conversation, al.newTurnEventScope(agent.ID, conversation))

	assertIdentities(t, recorder.recorded(), conversation)
}
