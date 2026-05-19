package extract

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

// Embedder is the minimal Embedder contract the worker depends on.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, float64, error)
}

// ExtractionLLM is the minimal LLM contract — one string in, one string out.
// Implementations route through pkg/providers using the configured slug.
type ExtractionLLM interface {
	ExtractFacts(ctx context.Context, system, user string) (string, error)
}

// Job is a unit of work for the extractor.
type Job struct {
	SessionKey string
	Window     []protocoltypes.Message
	StartIdx   int
	EndIdx     int
}

// Worker turns Jobs into persisted facts. One Worker per agent instance.
type Worker struct {
	store     facts.Store
	embed     Embedder
	llm       ExtractionLLM
	log       *Logger
	namespace string // primary namespace for inserted facts
	botRef    string // tg:bot:<botname> for source_msg_ref provenance
}

func NewWorker(store facts.Store, e Embedder, llm ExtractionLLM, log *Logger, namespace, botRef string) *Worker {
	return &Worker{store: store, embed: e, llm: llm, log: log, namespace: namespace, botRef: botRef}
}

// Run processes a single job synchronously. The caller drives async via a
// goroutine + buffered channel.
func (w *Worker) Run(ctx context.Context, j Job) {
	last, err := w.log.LastProcessed(ctx, j.SessionKey)
	if err == nil && j.EndIdx <= last {
		return
	}

	resp, err := w.callWithRetry(ctx, j.Window)
	if err != nil {
		_ = w.log.RecordFailure(ctx, j.SessionKey,
			fmt.Sprintf("[%d,%d)", j.StartIdx, j.EndIdx), err.Error())
		return
	}

	parsed, err := ParseFacts(resp)
	if err != nil {
		_ = w.log.RecordFailure(ctx, j.SessionKey,
			fmt.Sprintf("[%d,%d)", j.StartIdx, j.EndIdx), err.Error())
		return
	}

	for _, ef := range parsed {
		if err := w.persist(ctx, ef, j); err != nil {
			_ = w.log.RecordFailure(ctx, j.SessionKey,
				fmt.Sprintf("[%d,%d)", j.StartIdx, j.EndIdx), err.Error())
		}
	}

	_ = w.log.Mark(ctx, j.SessionKey, j.EndIdx)
}

func (w *Worker) callWithRetry(ctx context.Context, window []protocoltypes.Message) (string, error) {
	system, user := BuildPrompt(window)
	delays := []time.Duration{0, 1 * time.Second, 3 * time.Second, 9 * time.Second}
	var lastErr error
	for _, d := range delays {
		if d > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(d):
			}
		}
		out, err := w.llm.ExtractFacts(ctx, system, user)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("extract: empty after retries")
	}
	return "", lastErr
}
