//go:build windows

package fileutil

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockFilePlatform(file *os.File) error {
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&windows.Overlapped{},
	)
	if err == windows.ERROR_LOCK_VIOLATION {
		return errFileLockBusy
	}
	return err
}
