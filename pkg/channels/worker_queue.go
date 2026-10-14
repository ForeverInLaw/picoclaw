package channels

import (
	"context"
	"time"
)

// Admission and closure share the worker's lifetime gate. A full queue waits
// outside the gate so shutdown never waits for a blocked producer to finish.
func enqueueWorker[T any](ctx context.Context, w *channelWorker, queue chan T, closed *bool, message T) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		w.queueMu.RLock()
		if *closed {
			w.queueMu.RUnlock()
			return false
		}
		select {
		case queue <- message:
			w.queueMu.RUnlock()
			return true
		default:
			w.queueMu.RUnlock()
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func closeWorkerQueue[T any](w *channelWorker, queue chan T, closed *bool) {
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	if !*closed {
		*closed = true
		if queue != nil {
			close(queue)
		}
	}
}
