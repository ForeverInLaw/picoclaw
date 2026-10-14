package seahorse

import (
	"context"
	"fmt"
)

type CommitGuard func(operation func() error) (bool, error)
type commitGuardKey struct{}

// WithCommitGuard binds publication to the conversation generation captured by
// the caller. Computation and LLM requests always happen outside that gate.
func WithCommitGuard(ctx context.Context, guard CommitGuard) context.Context {
	return context.WithValue(ctx, commitGuardKey{}, guard)
}

func commitContext(ctx context.Context, operation func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := ctx.Value(commitGuardKey{}).(CommitGuard); ok && guard != nil {
		current, err := guard(operation)
		if err != nil {
			return err
		}
		if !current {
			return fmt.Errorf("conversation generation is no longer current")
		}
		return nil
	}
	return operation()
}

func publishContextSummary(ctx context.Context, operation func() (*string, error)) (*string, error) {
	var id *string
	err := commitContext(ctx, func() error { var err error; id, err = operation(); return err })
	return id, err
}
