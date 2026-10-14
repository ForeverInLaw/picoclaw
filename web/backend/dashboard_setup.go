package main

import (
	"fmt"
	"path/filepath"

	"github.com/sipeed/picoclaw/web/backend/dashboardauth"
	"github.com/sipeed/picoclaw/web/backend/middleware"
)

// Initial setup needs a capability available only to the launcher owner.
func createDashboardSetupToken(dbPath string) (string, string, error) {
	path := filepath.Join(filepath.Dir(dbPath), dashboardauth.SetupTokenFilename)
	token, err := middleware.NewLauncherDashboardSessionCookie()
	if err != nil {
		return "", path, err
	}
	if err := writeDashboardSetupToken(path, []byte(token+"\n")); err != nil {
		return "", path, fmt.Errorf("write setup code %q: %w", path, err)
	}
	return token, path, nil
}
