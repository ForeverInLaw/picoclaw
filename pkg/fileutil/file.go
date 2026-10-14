// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

// Package fileutil provides file manipulation utilities.
package fileutil

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteFileAtomic atomically writes data to a file using a temp file + rename pattern.
//
// This guarantees that the target file is either:
// - Completely written with the new data
// - Unchanged (if any step fails before rename)
//
// The function:
// 1. Creates a temp file in the same directory (original untouched)
// 2. Writes data to temp file
// 3. Syncs data to disk (critical for SD cards/flash storage)
// 4. Sets file permissions
// 5. Syncs directory metadata (ensures rename is durable)
// 6. Atomically renames temp file to target path
//
// Safety guarantees:
// - Original file is NEVER modified until successful rename
// - Temp file is always cleaned up on error
// - Data is flushed to physical storage before rename
// - Directory entry is synced to prevent orphaned inodes
//
// Parameters:
//   - path: Target file path
//   - data: Data to write
//   - perm: File permission mode (e.g., 0o600 for secure, 0o644 for readable)
//
// Returns:
//   - Error if any step fails, nil on success
//
// Example:
//
//	// Secure config file (owner read/write only)
//	err := utils.WriteFileAtomic("config.json", data, 0o600)
//
//	// Public readable file
//	err := utils.WriteFileAtomic("public.txt", data, 0o644)
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return writeFileAtomic(path, data, perm, nil)
}

// WriteFileAtomicInRoot uses the same durable write protocol with root-relative
// operations, so directory replacement or symlinks cannot escape the owner root.
func WriteFileAtomicInRoot(root *os.Root, path string, data []byte, perm os.FileMode) error {
	return writeFileAtomic(path, data, perm, root)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode, root *os.Root) error {
	mkdirAll, openFile := os.MkdirAll, os.OpenFile
	remove, rename, open := os.Remove, os.Rename, os.Open
	if root != nil {
		mkdirAll, openFile = root.MkdirAll, root.OpenFile
		remove, rename, open = root.Remove, root.Rename, root.Open
	}
	dir := filepath.Dir(path)
	if err := mkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create temp file in the same directory (ensures atomic rename works)
	// Using a hidden prefix (.tmp-) to avoid issues with some tools
	tmpPath := filepath.Join(dir, fmt.Sprintf(".tmp-%d-%d", os.Getpid(), time.Now().UnixNano()))
	tmpFile, err := openFile(
		tmpPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		perm,
	)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	cleanup := true

	defer func() {
		if cleanup {
			tmpFile.Close()
			_ = remove(tmpPath)
		}
	}()

	// Write data to temp file
	// Note: Original file is untouched at this point
	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// CRITICAL: Force sync to storage medium before any other operations.
	// This ensures data is physically written to disk, not just cached.
	// Essential for SD cards, eMMC, and other flash storage on edge devices.
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Set file permissions before closing
	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("failed to set permissions: %w", err)
	}

	// Close file before rename (required on Windows)
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename: temp file becomes the target
	// On POSIX: rename() is atomic
	// On Windows: Rename() is atomic for files
	if err := rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	// Sync directory to ensure rename is durable
	// This prevents the renamed file from disappearing after a crash
	if dirFile, err := open(dir); err == nil {
		_ = dirFile.Sync()
		dirFile.Close()
	}

	// Success: skip cleanup (file was renamed, no temp to remove)
	cleanup = false
	return nil
}
