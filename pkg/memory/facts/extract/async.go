package extract

import (
	"context"
)

// AsyncWorker wraps Worker.Run with a buffered channel and a single
// drainer goroutine, providing fire-and-forget enqueueing for the agent
// hook.
type AsyncWorker struct {
	worker *Worker
	queue  chan Job
	done   chan struct{}
}

// NewAsyncWorker constructs the wrapper. Start must be called to spin up
// the drainer; Stop closes the queue and waits for in-flight work.
func NewAsyncWorker(w *Worker, bufferSize int) *AsyncWorker {
	if bufferSize <= 0 {
		bufferSize = 32
	}
	return &AsyncWorker{
		worker: w,
		queue:  make(chan Job, bufferSize),
		done:   make(chan struct{}),
	}
}

// Start begins draining the queue. Caller passes the long-lived context;
// cancelling it stops the drainer.
func (a *AsyncWorker) Start(ctx context.Context) {
	go func() {
		defer close(a.done)
		for {
			select {
			case <-ctx.Done():
				return
			case j, ok := <-a.queue:
				if !ok {
					return
				}
				a.worker.Run(ctx, j)
			}
		}
	}()
}

// Enqueue is non-blocking. If the queue is full the job is dropped and
// the call is recorded in failed_extractions for visibility.
func (a *AsyncWorker) Enqueue(j Job) {
	select {
	case a.queue <- j:
	default:
		// Best-effort failure log; drop the job rather than block the turn.
		ctx := context.Background()
		_ = a.worker.log.RecordFailure(ctx, j.SessionKey,
			"queue_full", "extractor queue full; job dropped")
	}
}

// Stop closes the queue and waits for the drainer to exit.
func (a *AsyncWorker) Stop() {
	close(a.queue)
	<-a.done
}
