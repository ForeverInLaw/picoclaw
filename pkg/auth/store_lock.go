package auth

import (
	"os"
	"path/filepath"
)

// Lock files remain beside auth.json: removing them could split ownership between
// processes holding an old inode and processes opening its replacement.
func lockAuthFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockAuthPlatform(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func lockStore() (func(), error) {
	storeMu.Lock()
	file, err := lockAuthFile(authFilePath() + ".lock")
	if err != nil {
		storeMu.Unlock()
		return nil, err
	}
	return func() { _ = file.Close(); storeMu.Unlock() }, nil
}
