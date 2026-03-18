package memoryindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var learningEntryHeader = regexp.MustCompile(`(?m)^## \[[A-Z]+-\d{8}-[A-Z0-9]+\] .+$`)

type workspaceDocument struct {
	Label string
	Path  string
}

func (i *Index) BootstrapWorkspaceFiles(ctx context.Context, workspace string) error {
	bootstrapped, err := i.metaBool(ctx, "bootstrapped_workspace")
	if err != nil {
		return err
	}
	if bootstrapped {
		return nil
	}

	docs := []workspaceDocument{
		{Label: "memory", Path: filepath.Join(workspace, "memory", "MEMORY.md")},
		{Label: "learning", Path: filepath.Join(workspace, ".learnings", "LEARNINGS.md")},
		{Label: "error", Path: filepath.Join(workspace, ".learnings", "ERRORS.md")},
		{Label: "feature_request", Path: filepath.Join(workspace, ".learnings", "FEATURE_REQUESTS.md")},
	}

	for _, doc := range docs {
		if err := i.bootstrapWorkspaceDocument(ctx, doc); err != nil {
			return err
		}
	}

	return i.setMeta(ctx, "bootstrapped_workspace", "1")
}

func (i *Index) bootstrapWorkspaceDocument(ctx context.Context, doc workspaceDocument) error {
	data, err := os.ReadFile(doc.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memoryindex: read workspace doc: %w", err)
	}

	var chunks []string
	switch doc.Label {
	case "memory":
		chunks = extractMemorySections(string(data))
	default:
		chunks = extractLearningEntries(string(data))
	}

	for _, chunk := range chunks {
		if err := i.AddObservation(ctx, Observation{
			Channel:  "memory",
			ChatID:   doc.Label,
			Role:     "assistant",
			SenderID: doc.Label,
			Content:  chunk,
		}); err != nil {
			return err
		}
	}

	return nil
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
