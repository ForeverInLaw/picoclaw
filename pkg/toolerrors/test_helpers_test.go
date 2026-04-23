package toolerrors

import (
	"encoding/json"
	"os"
)

func readRawLines(path string) ([][]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return compactLines(content), nil
}

func readLogFile(path string) ([]Entry, error) {
	lines, err := readRawLines(path)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(lines))
	for _, line := range lines {
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
