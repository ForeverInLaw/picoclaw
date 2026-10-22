package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Lock files remain beside auth.json: removing them could split ownership between
// processes holding an old inode and processes opening its replacement.
var errAuthLockBusy = errors.New("credential lock is busy")

func lockAuthFile(path string) (*os.File, error) {
	return lockAuthFileContext(context.Background(), path)
}

func lockAuthFileContext(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err := lockAuthPlatform(file)
		if err == nil {
			break
		}
		if !errors.Is(err, errAuthLockBusy) {
			_ = file.Close()
			return nil, err
		}
		if err := waitAuthLock(ctx); err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	return file, nil
}

func lockStore() (func(), error) { return lockStoreContext(context.Background()) }

func lockStoreContext(ctx context.Context) (func(), error) {
	if err := acquireAuthMutex(ctx, &storeMu); err != nil {
		return nil, err
	}
	file, err := lockAuthFileContext(ctx, authFilePath()+".lock")
	if err != nil {
		storeMu.Unlock()
		return nil, err
	}
	return func() { _ = file.Close(); storeMu.Unlock() }, nil
}

func waitAuthLock(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func acquireAuthMutex(ctx context.Context, mu *sync.Mutex) error {
	for !mu.TryLock() {
		if err := waitAuthLock(ctx); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		mu.Unlock()
		return err
	}
	return nil
}
