package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/web/backend/utils"
)

var browserBootstrapMu sync.Mutex

const (
	browserDelay    = 500 * time.Millisecond
	shutdownTimeout = 15 * time.Second
)

// shutdownApp gracefully shuts down all server components and resources.
// It performs the following shutdown sequence:
//   - Shuts down the API handler to close all active SSE (Server-Sent Events) connections
//   - Disables HTTP keep-alive to prevent new connections during shutdown
//   - Attempts graceful HTTP server shutdown with timeout
//   - Logs shutdown status at appropriate log levels
//
// The function handles timeout errors gracefully by logging them at info level
// since context.DeadlineExceeded is expected when there are active long-running
// connections (such as SSE streams).
//
// This function should be called during application termination to ensure
// clean resource cleanup and proper connection closure.
func shutdownApp() {
	// First, shutdown API handler to close all SSE connections
	if apiHandler != nil {
		apiHandler.Shutdown()
	}

	if server != nil {
		// Disable keep-alive to allow graceful shutdown
		server.SetKeepAlivesEnabled(false)

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			// Context deadline exceeded is expected if there are active connections
			// This is not necessarily an error, so log it at info level
			if errors.Is(err, context.DeadlineExceeded) {
				logger.Infof("Server shutdown timeout after %v, forcing close", shutdownTimeout)
			} else {
				logger.Errorf("Server shutdown error: %v", err)
			}
		} else {
			logger.Infof("Server shutdown completed successfully")
		}
	}
}

func openBrowser() error {
	if serverAddr == "" {
		return fmt.Errorf("server address not set")
	}
	browserBootstrapMu.Lock()
	browserURL := serverAddr
	if localAutoLogin != nil {
		browserURL += localAutoLogin.URLPath()
		localAutoLogin = nil
	} else if dashboardSetupToken != "" {
		browserURL += "/launcher-setup#setup_token=" + url.QueryEscape(dashboardSetupToken)
		dashboardSetupToken = ""
	}
	browserBootstrapMu.Unlock()
	return utils.OpenBrowser(browserURL)
}
