package session

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/providers"
)

var ErrContextChanged = errors.New("canonical conversation changed during context import")

type CanonicalContext struct {
	History []providers.Message `json:"history"`
	Summary string              `json:"summary"`
}

func (c CanonicalContext) Fingerprint() (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// ContextReader reads history and summary as one state and preserves read errors.
type ContextReader interface {
	ReadContext(key string) (CanonicalContext, error)
}

// ContextTurn checks the canonical snapshot inside the existing generation gate.
// It prevents an old background snapshot reconciling a newer active projection.
type ContextTurn interface {
	ConversationTurn
	ContextReader
	CommitContextIfCurrent(key, expected string, operation func() error) (bool, error)
}

func readCanonicalContext(store SessionStore, key string) (CanonicalContext, error) {
	if reader, ok := store.(ContextReader); ok {
		return reader.ReadContext(key)
	}
	return CanonicalContext{History: store.GetHistory(key), Summary: store.GetSummary(key)}, nil
}

func (v *conversationView) ReadContext(key string) (CanonicalContext, error) {
	var state CanonicalContext
	err := v.apply(key, func(store SessionStore, resolved string) error {
		var err error
		state, err = readCanonicalContext(store, resolved)
		return err
	})
	return state, err
}

func (v *conversationView) CommitContextIfCurrent(key, expected string, operation func() error) (bool, error) {
	return v.CommitIfCurrent(func() error {
		state, err := readCanonicalContext(v.owner.store, v.ConversationKey(key))
		if err != nil {
			return err
		}
		actual, err := state.Fingerprint()
		if err != nil {
			return err
		}
		if actual != expected {
			return ErrContextChanged
		}
		return operation()
	})
}

func (b *JSONLBackend) ReadContext(key string) (CanonicalContext, error) {
	if reader, ok := b.store.(interface {
		ReadContext(key string) ([]providers.Message, string, error)
	}); ok {
		history, summary, err := reader.ReadContext(key)
		return CanonicalContext{History: history, Summary: summary}, err
	}
	history, err := b.store.GetHistory(context.Background(), key)
	if err != nil {
		return CanonicalContext{}, err
	}
	summary, err := b.store.GetSummary(context.Background(), key)
	return CanonicalContext{History: history, Summary: summary}, err
}

func (sm *SessionManager) ReadContext(key string) (CanonicalContext, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if stored, ok := sm.sessions[key]; ok {
		return CanonicalContext{
			History: append([]providers.Message(nil), stored.Messages...),
			Summary: stored.Summary,
		}, nil
	}
	if sm.storage != "" {
		filename := sanitizeFilename(key)
		if filename == "." || !filepath.IsLocal(filename) {
			return CanonicalContext{}, os.ErrInvalid
		}
		if _, err := os.Stat(filepath.Join(sm.storage, filename+".json")); err == nil {
			return CanonicalContext{}, fmt.Errorf("canonical JSON session could not be loaded: %s", filename)
		} else if !os.IsNotExist(err) {
			return CanonicalContext{}, fmt.Errorf("read canonical JSON session: %w", err)
		}
	}
	return CanonicalContext{History: []providers.Message{}}, nil
}
