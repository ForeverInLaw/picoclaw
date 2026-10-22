package fileutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

var errFileLockBusy = errors.New("file lock is busy")

// LockFile acquires an exclusive process lock until the returned file is closed.
// The protected lock inode must remain in place; removing it can split ownership.
func LockFile(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := lockFilePlatform(file)
		if err == nil {
			if err = ctx.Err(); err != nil {
				_ = file.Close()
				return nil, err
			}
			return file, nil
		}
		if !errors.Is(err, errFileLockBusy) {
			_ = file.Close()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
