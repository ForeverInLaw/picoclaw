//go:build windows

package auth

import (
	"golang.org/x/sys/windows"
	"os"
)

func lockAuthPlatform(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}
