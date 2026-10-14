package skills

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// WriteOriginMetadata records registry provenance in the same private atomic
// file for dashboard and agent installs.
func WriteOriginMetadata(targetDir, registryName, slug, version string) error {
	meta := struct {
		Version          int    `json:"version"`
		Registry         string `json:"registry"`
		Slug             string `json:"slug"`
		InstalledVersion string `json:"installed_version"`
		InstalledAt      int64  `json:"installed_at"`
	}{Version: 1, Registry: registryName, Slug: slug, InstalledVersion: version, InstalledAt: time.Now().UnixMilli()}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(filepath.Join(targetDir, ".skill-origin.json"), data, 0o600)
}
