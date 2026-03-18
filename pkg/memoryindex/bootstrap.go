package memoryindex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/pkg/providers"
)

type sessionMeta struct {
	Key string `json:"key"`
}

func (i *Index) BootstrapSessions(ctx context.Context, sessionsDir string) error {
	bootstrapped, err := i.IsBootstrapped(ctx)
	if err != nil {
		return err
	}
	if bootstrapped {
		return nil
	}

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return i.MarkBootstrapped(ctx)
		}
		return fmt.Errorf("memoryindex: read sessions dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		if err := i.bootstrapSessionFile(ctx, sessionsDir, entry.Name()); err != nil {
			return err
		}
	}

	return i.MarkBootstrapped(ctx)
}

func (i *Index) bootstrapSessionFile(ctx context.Context, sessionsDir, metaName string) error {
	metaPath := filepath.Join(sessionsDir, metaName)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("memoryindex: read session meta: %w", err)
	}

	var meta sessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("memoryindex: decode session meta: %w", err)
	}
	if strings.TrimSpace(meta.Key) == "" {
		return nil
	}

	jsonlName := strings.TrimSuffix(metaName, ".meta.json") + ".jsonl"
	jsonlPath := filepath.Join(sessionsDir, jsonlName)
	file, err := os.Open(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memoryindex: open session jsonl: %w", err)
	}
	defer file.Close()

	channel, chatID := parseSessionSource(meta.Key)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if err := i.AddObservation(ctx, Observation{
			SessionKey: meta.Key,
			Channel:    channel,
			ChatID:     chatID,
			Role:       msg.Role,
			Content:    msg.Content,
		}); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("memoryindex: scan session jsonl: %w", err)
	}

	return nil
}

func parseSessionSource(sessionKey string) (channel, chatID string) {
	parts := strings.Split(sessionKey, ":")
	if len(parts) >= 5 && parts[0] == "agent" {
		return parts[2], strings.Join(parts[4:], ":")
	}
	return "", ""
}
