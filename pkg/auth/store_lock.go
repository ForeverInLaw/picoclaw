package auth

import (
	"context"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func lockStore() (func(), error) { return lockStoreContext(context.Background()) }

func lockStoreContext(ctx context.Context) (func(), error) {
	if err := acquireAuthMutex(ctx, &storeMu); err != nil {
		return nil, err
	}
	file, err := fileutil.LockFile(ctx, authFilePath()+".lock")
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
