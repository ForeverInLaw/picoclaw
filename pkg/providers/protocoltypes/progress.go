package protocoltypes

import "context"

type modelProgressKey struct{}

// WithModelProgress carries generation progress independently of chat previews.
func WithModelProgress(ctx context.Context, notify func()) context.Context {
	return context.WithValue(ctx, modelProgressKey{}, notify)
}

// ReportModelProgress records decoded text, exposed reasoning, or tool formation.
// Transport keepalives and response bookkeeping must not call it.
func ReportModelProgress(ctx context.Context) {
	if notify, ok := ctx.Value(modelProgressKey{}).(func()); ok {
		notify()
	}
}
