package utils

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

var audioExtensions = []string{".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma"}

func AudioFormat(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	for _, supportedExt := range audioExtensions {
		if ext == supportedExt {
			return strings.TrimPrefix(ext, "."), nil
		}
	}

	return "", fmt.Errorf("unsupported audio format for %q", path)
}

// IsAudioFile checks if a file is an audio file based on its filename extension and content type.
func IsAudioFile(filename, contentType string) bool {
	audioTypes := []string{"audio/", "application/ogg", "application/x-ogg"}

	for _, ext := range audioExtensions {
		if strings.HasSuffix(strings.ToLower(filename), ext) {
			return true
		}
	}

	for _, audioType := range audioTypes {
		if strings.HasPrefix(strings.ToLower(contentType), audioType) {
			return true
		}
	}

	return false
}

// SanitizeFilename removes potentially dangerous characters from a filename
// and returns a safe version for local filesystem storage.
func SanitizeFilename(filename string) string {
	// Get the base filename without path
	base := filepath.Base(filename)

	// Remove any directory traversal attempts
	base = strings.ReplaceAll(base, "..", "")
	base = strings.ReplaceAll(base, "/", "_")
	base = strings.ReplaceAll(base, "\\", "_")

	return base
}

// DownloadOptions holds optional parameters for downloading files
type DownloadOptions struct {
	Timeout      time.Duration
	ExtraHeaders map[string]string
	LoggerPrefix string
	ProxyURL     string
}

// DownloadFile downloads a file from URL to a local temp directory.
// Returns the local file path or empty string on error.
func DownloadFile(urlStr, filename string, opts DownloadOptions) string {
	// Set defaults
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.LoggerPrefix == "" {
		opts.LoggerPrefix = "utils"
	}

	mediaDir := media.TempDir()
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		logger.ErrorCF(opts.LoggerPrefix, "Failed to create media directory", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	// Generate unique filename with UUID prefix to prevent conflicts
	safeName := SanitizeFilename(filename)
	localPath := filepath.Join(mediaDir, uuid.New().String()[:8]+"_"+safeName)

	// Create HTTP request
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		logger.ErrorCF(opts.LoggerPrefix, "Failed to create download request", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	// Add extra headers (e.g., Authorization for Slack)
	for key, value := range opts.ExtraHeaders {
		req.Header.Set(key, value)
	}

	client := &http.Client{Timeout: opts.Timeout}
	if opts.ProxyURL != "" {
		proxyURL, parseErr := url.Parse(opts.ProxyURL)
		if parseErr != nil {
			logger.ErrorCF(opts.LoggerPrefix, "Invalid proxy URL for download", map[string]any{
				"error": parseErr.Error(),
				"proxy": opts.ProxyURL,
			})
			return ""
		}
		client.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		}
	}
	// Chat file servers drop connections now and then, mid-body included; a lost
	// attachment costs more than a short retry of the whole download.
	for attempt := range maxRetries {
		if attempt > 0 {
			time.Sleep(retryDelayUnit * time.Duration(attempt))
		}
		var retryable bool
		if retryable, err = downloadOnce(client, req, localPath); err == nil || !retryable {
			break
		}
	}
	if err != nil {
		logger.ErrorCF(opts.LoggerPrefix, "Failed to download file", map[string]any{
			"error": err.Error(),
			"url":   urlStr,
		})
		return ""
	}

	logger.DebugCF(opts.LoggerPrefix, "File downloaded successfully", map[string]any{
		"path": localPath,
	})

	return localPath
}

// downloadOnce writes one response body to localPath and removes a partial
// file. retryable reports whether another attempt could succeed.
func downloadOnce(client *http.Client, req *http.Request, localPath string) (retryable bool, err error) {
	resp, err := client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return shouldRetry(resp.StatusCode), fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	out, err := os.Create(localPath)
	if err != nil {
		return false, err
	}
	_, err = io.Copy(out, resp.Body)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(localPath)
		return true, err
	}
	return false, nil
}

// DownloadFileSimple is a simplified version of DownloadFile without options
func DownloadFileSimple(url, filename string) string {
	return DownloadFile(url, filename, DownloadOptions{
		LoggerPrefix: "media",
	})
}
