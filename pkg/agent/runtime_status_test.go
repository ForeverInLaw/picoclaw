package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/modelruntime"
)

type failingRuntimeContext struct {
	ContextManager
	phase string
	err   error
}

func (m *failingRuntimeContext) Assemble(ctx context.Context, snapshot ContextSnapshot) (ContextAssembly, error) {
	if m.phase == "assemble" {
		return ContextAssembly{}, m.err
	}
	return m.ContextManager.Assemble(ctx, snapshot)
}

func (m *failingRuntimeContext) Ingest(ctx context.Context, snapshot ContextSnapshot) error {
	if m.phase == "ingest" {
		return m.err
	}
	return m.ContextManager.Ingest(ctx, snapshot)
}

func TestRuntimeStatusReportsContextFailuresBeforeAndAfterInference(t *testing.T) {
	for _, phase := range []string{"assemble", "ingest"} {
		t.Run(phase, func(t *testing.T) {
			loop, _, messages, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			defer messages.Close()
			defer loop.Close()
			agent := loop.registry.GetDefaultAgent()
			failure := errors.New("context failure")
			agent.ContextManager = &failingRuntimeContext{
				ContextManager: agent.ContextManager,
				phase:          phase,
				err:            failure,
			}
			path := filepath.Join(t.TempDir(), "config.json")
			collector, err := loop.EnableRuntimeStatus(path)
			require.NoError(t, err)
			_, err = loop.runAgentLoop(t.Context(), agent, processOptions{
				SessionKey: "runtime-failure", Channel: "cli", ChatID: "runtime", UserMessage: "hello",
			})
			require.ErrorIs(t, err, failure)
			require.NoError(t, collector.Flush())
			snapshot, err := modelruntime.Read(path)
			require.NoError(t, err)
			require.Len(t, snapshot.Records, 1)
			require.Equal(t, "failed", snapshot.Records[0].State)
			require.False(t, snapshot.Records[0].FinishedAt.IsZero())
		})
	}
}
