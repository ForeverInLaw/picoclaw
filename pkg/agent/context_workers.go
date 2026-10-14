package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/sipeed/picoclaw/pkg/providers"
)

// contextWorkers owns admission, cancellation and joining for either active
// context engine. Storage and captured providers close only after workers exit.
type contextWorkers struct {
	mu                sync.Mutex
	lifetime          context.Context
	cancel            context.CancelFunc
	closing           bool
	closed            chan struct{}
	closeErr          error
	nextOperation     uint64
	operations        map[uint64]contextOperation
	workers           sync.WaitGroup
	retainedProviders []providers.StatefulProvider
}

type contextOperation struct {
	key    string
	cancel context.CancelFunc
}

type contextProviderOwner interface {
	retainProvider(provider providers.LLMProvider)
}

func newContextWorkers() *contextWorkers {
	lifetime, cancel := context.WithCancel(context.Background())
	return &contextWorkers{
		lifetime:   lifetime,
		cancel:     cancel,
		closed:     make(chan struct{}),
		operations: map[uint64]contextOperation{},
	}
}

func (w *contextWorkers) begin(ctx context.Context, key string) (context.Context, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closing {
		return nil, nil, fmt.Errorf("context manager is closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.lifetime, cancel)
	w.nextOperation++
	id := w.nextOperation
	w.operations[id] = contextOperation{key: key, cancel: cancel}
	w.workers.Add(1)
	done := func() {
		stop()
		cancel()
		w.mu.Lock()
		delete(w.operations, id)
		w.mu.Unlock()
		w.workers.Done()
	}
	return ctx, done, nil
}

func (w *contextWorkers) clear(key string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, operation := range w.operations {
		if operation.key == key {
			operation.cancel()
		}
	}
	return nil
}

func (w *contextWorkers) close(closeStorage func() error) error {
	w.mu.Lock()
	if w.closing {
		w.mu.Unlock()
		<-w.closed
		return w.closeErr
	}
	w.closing = true
	w.cancel()
	w.mu.Unlock()
	w.workers.Wait()
	if closeStorage != nil {
		w.closeErr = closeStorage()
	}
	for _, provider := range w.retainedProviders {
		provider.Close()
	}
	close(w.closed)
	return w.closeErr
}

// Keep clients captured by earlier maintenance operations alive until join,
// including when the conversation's selected model changes.
func (w *contextWorkers) retainProvider(provider providers.LLMProvider) {
	stateful, ok := provider.(providers.StatefulProvider)
	if !ok {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, existing := range w.retainedProviders {
		if existing == stateful {
			return
		}
	}
	if w.closing {
		w.mu.Unlock()
		<-w.closed
		stateful.Close()
		w.mu.Lock()
		return
	}
	w.retainedProviders = append(w.retainedProviders, stateful)
}
