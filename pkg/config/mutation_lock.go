package config

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// WithConfigMutation serializes read/validate/write transactions across dashboard
// and gateway processes. The persistent lock inode must never be removed.
func WithConfigMutation(path string, mutate func() error) error {
	return WithConfigMutationContext(context.Background(), path, mutate)
}

func WithConfigMutationContext(ctx context.Context, path string, mutate func() error) error {
	file, err := fileutil.LockFile(ctx, path+".lock")
	if err != nil {
		return err
	}
	defer file.Close()
	return mutate()
}
