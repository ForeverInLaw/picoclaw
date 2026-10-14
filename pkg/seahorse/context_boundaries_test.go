package seahorse_test

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/tokenizer"
)

func TestClearSessionWaitsForAdmittedIngestion(t *testing.T) {
	engine, err := seahorse.NewEngine(seahorse.Config{DBPath: filepath.Join(t.TempDir(), "context.sqlite")}, nil)
	require.NoError(t, err)
	defer engine.Close()
	require.NoError(t, engine.SyncSources(t.Context(), "conversation", nil))
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	ctx := seahorse.WithCommitGuard(t.Context(), func(commit func() error) (bool, error) {
		close(entered)
		<-release
		return true, commit()
	})
	ingested, cleared := make(chan error, 1), make(chan error, 1)
	go func() {
		ingested <- engine.SyncSources(ctx, "conversation", []seahorse.Message{{Role: "user", Content: "pending source", TokenCount: 10}})
	}()
	<-entered
	go func() { cleared <- engine.ClearSession(t.Context(), "conversation") }()
	select {
	case err := <-cleared:
		require.NoError(t, err)
		t.Fatal("clear returned while earlier ingestion could still recreate the session")
	case <-time.After(50 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	require.NoError(t, <-ingested)
	require.NoError(t, <-cleared)
	status, err := engine.GetRetrieval().Store().GetSessionStatus(t.Context(), "conversation")
	require.NoError(t, err)
	require.Zero(t, status.Messages)
}

func TestAssemblyBudgetsRenderedSummaryAndGuidance(t *testing.T) {
	for _, depth := range []int{0, 2} {
		t.Run(string(rune('0'+depth)), func(t *testing.T) {
			engine, err := seahorse.NewEngine(seahorse.Config{DBPath: filepath.Join(t.TempDir(), "context.sqlite")}, nil)
			require.NoError(t, err)
			defer engine.Close()
			store := engine.GetRetrieval().Store()
			conv, err := store.GetOrCreateConversation(t.Context(), "conversation")
			require.NoError(t, err)
			content := strings.Repeat(`<source & "evidence">`, 10)
			plainTokens := tokenizer.EstimateMessageTokens(providers.Message{Content: content})
			summary, err := store.CreateSummary(t.Context(), seahorse.CreateSummaryInput{
				ConversationID: conv.ConversationID, Kind: seahorse.SummaryKindLeaf,
				Depth: depth, Content: content, TokenCount: plainTokens,
			})
			require.NoError(t, err)
			require.NoError(t, store.AppendContextSummary(t.Context(), conv.ConversationID, summary.SummaryID))
			latest := providers.Message{Role: "user", Content: "latest question"}
			messageTokens := tokenizer.EstimateMessageTokens(latest)
			message, err := store.AddMessage(t.Context(), conv.ConversationID, latest.Role, latest.Content, messageTokens)
			require.NoError(t, err)
			require.NoError(t, store.AppendContextMessage(t.Context(), conv.ConversationID, message.ID))
			budget := plainTokens + messageTokens
			result, err := engine.Assemble(t.Context(), "conversation", seahorse.AssembleInput{Budget: budget})
			require.NoError(t, err)
			tokens := 0
			if result.Summary != "" {
				tokens += tokenizer.EstimateMessageTokens(providers.Message{Content: result.Summary})
			}
			for _, message := range result.Messages {
				tokens += message.TokenCount
			}
			require.LessOrEqual(t, tokens, budget)
			require.Equal(t, latest.Content, result.Messages[len(result.Messages)-1].Content)
			full, err := engine.Assemble(t.Context(), "conversation", seahorse.AssembleInput{Budget: 10000})
			require.NoError(t, err)
			require.Contains(t, full.Summary, "&lt;source &amp;")
			require.Contains(t, full.Summary, "short_expand")
		})
	}
}
