//go:build !windows

package main

import "github.com/sipeed/picoclaw/pkg/fileutil"

func writeDashboardSetupToken(path string, data []byte) error {
	return fileutil.WriteFileAtomic(path, data, 0o600)
}
