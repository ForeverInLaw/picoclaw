package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var errConfigLockBusy = errors.New("configuration writer is busy")

// WithConfigMutation serializes read/validate/write transactions across dashboard
// and gateway processes. The persistent lock inode must never be removed.
func WithConfigMutation(path string, mutate func() error) error {
	return WithConfigMutationContext(context.Background(), path, mutate)
}

func WithConfigMutationContext(ctx context.Context, path string, mutate func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	for {
		err = lockConfigPlatform(file)
		if err == nil {
			break
		}
		if !errors.Is(err, errConfigLockBusy) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return mutate()
}
