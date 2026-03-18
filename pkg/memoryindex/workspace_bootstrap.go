package memoryindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var learningEntryHeader = regexp.MustCompile(`(?m)^## \[[A-Z]+-\d{8}-[A-Z0-9]+\] .+$`)

type workspaceDocument struct {
	Label     string
	Path      string
	SourceKey string
}

func (i *Index) BootstrapWorkspaceFiles(ctx context.Context, workspace string) error {
	return i.SyncWorkspaceFiles(ctx, workspace)
}

func (i *Index) SyncWorkspaceFiles(ctx context.Context, workspace string) error {
	docs := []workspaceDocument{
		{Label: "memory", Path: filepath.Join(workspace, "memory", "MEMORY.md"), SourceKey: "memory/MEMORY.md"},
		{Label: "learning", Path: filepath.Join(workspace, ".learnings", "LEARNINGS.md"), SourceKey: ".learnings/LEARNINGS.md"},
		{Label: "error", Path: filepath.Join(workspace, ".learnings", "ERRORS.md"), SourceKey: ".learnings/ERRORS.md"},
		{Label: "feature_request", Path: filepath.Join(workspace, ".learnings", "FEATURE_REQUESTS.md"), SourceKey: ".learnings/FEATURE_REQUESTS.md"},
	}

	for _, doc := range docs {
		if err := i.syncWorkspaceDocument(ctx, doc); err != nil {
			return err
		}
	}

	return nil
}

func (i *Index) syncWorkspaceDocument(ctx context.Context, doc workspaceDocument) error {
	metaKey := "workspace_doc_hash:" + doc.SourceKey
	prevHash, err := i.metaString(ctx, metaKey)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(doc.Path)
	if err != nil {
		if os.IsNotExist(err) {
			if prevHash != "" {
				if err := i.ReplaceSourceObservations(ctx, "workspace_doc", doc.SourceKey, nil); err != nil {
					return err
				}
				return i.setMeta(ctx, metaKey, "")
			}
			return nil
		}
		return fmt.Errorf("memoryindex: read workspace doc: %w", err)
	}

	currentHash := sha256Hex(data)
	if currentHash == prevHash {
		return nil
	}

	var chunks []string
	switch doc.Label {
	case "memory":
		chunks = extractMemorySections(string(data))
	default:
		chunks = extractLearningEntries(string(data))
	}

	observations := make([]Observation, 0, len(chunks))
	for _, chunk := range chunks {
		observations = append(observations, Observation{
			Channel:  "memory",
			ChatID:   doc.Label,
			Role:     "assistant",
			SenderID: doc.Label,
			Content:  chunk,
		})
	}

	if err := i.ReplaceSourceObservations(ctx, "workspace_doc", doc.SourceKey, observations); err != nil {
		return err
	}
	return i.setMeta(ctx, metaKey, currentHash)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func extractMemorySections(content string) []string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var (
		chunks  []string
		heading string
		body    []string
	)

	flush := func() {
		text := strings.TrimSpace(strings.Join(body, "\n"))
		if heading == "" || text == "" {
			body = nil
			return
		}
		if strings.HasPrefix(text, "(") && strings.HasSuffix(text, ")") {
			body = nil
			return
		}
		chunks = append(chunks, fmt.Sprintf("[workspace_memory]\nsection: %s\n\n%s", heading, text))
		body = nil
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			flush()
			heading = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if heading != "" {
			body = append(body, line)
		}
	}
	flush()

	return chunks
}

func extractLearningEntries(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	headers := learningEntryHeader.FindAllStringIndex(content, -1)
	if len(headers) == 0 {
		return nil
	}

	chunks := make([]string, 0, len(headers))
	for idx, match := range headers {
		start := match[0]
		end := len(content)
		if idx+1 < len(headers) {
			end = headers[idx+1][0]
		}
		entry := strings.TrimSpace(content[start:end])
		entry = strings.TrimSpace(strings.TrimSuffix(entry, "---"))
		if entry != "" {
			chunks = append(chunks, "[workspace_learning]\n\n"+entry)
		}
	}

	return chunks
}
