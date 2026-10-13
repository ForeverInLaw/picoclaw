package session

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type conversationBoundary struct {
	Generation string    `json:"generation"`
	ResetAt    time.Time `json:"reset_at"`
}

// ConversationStore preserves archived generations while directing new turns
// to the active conversation. A reset is committed with one atomic file write.
type ConversationStore struct {
	*conversationView
	mu         sync.Mutex
	store      SessionStore
	dir        string
	boundaries map[string]conversationBoundary
}

func NewConversationStore(store SessionStore, dir string) *ConversationStore {
	s := &ConversationStore{store: store, dir: dir, boundaries: make(map[string]conversationBoundary)}
	s.conversationView = &conversationView{owner: s}
	return s
}

func (s *ConversationStore) boundaryPath(key string) string {
	return filepath.Join(s.dir, "boundaries", fmt.Sprintf("%x.json", sha256.Sum256([]byte(key))))
}

func (s *ConversationStore) boundaryLocked(key string) (conversationBoundary, error) {
	if boundary, ok := s.boundaries[key]; ok {
		return boundary, nil
	}
	var boundary conversationBoundary
	if s.dir != "" {
		data, err := os.ReadFile(s.boundaryPath(key))
		if err != nil && !os.IsNotExist(err) {
			return boundary, fmt.Errorf("read conversation boundary: %w", err)
		}
		if err == nil {
			if err := json.Unmarshal(data, &boundary); err != nil {
				return boundary, fmt.Errorf("decode conversation boundary: %w", err)
			}
			if boundary.Generation == "" || boundary.ResetAt.IsZero() {
				return boundary, fmt.Errorf("invalid conversation boundary")
			}
		}
	}
	s.boundaries[key] = boundary
	return boundary, nil
}

// CaptureTurn freezes the active generation so writes and rollbacks from an
// in-flight turn become inert if the user resets its conversation.
func (s *ConversationStore) CaptureTurn(key string) (SessionStore, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	boundary, err := s.boundaryLocked(key)
	if err != nil {
		return nil, time.Time{}, err
	}
	return &conversationView{owner: s, key: key, generation: boundary.Generation, bound: true}, boundary.ResetAt, nil
}

// ResetConversation leaves previous transcripts stored under their original
// keys. It advances the active generation only after persistence succeeds.
func (s *ConversationStore) ResetConversation(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.boundaryLocked(key); err != nil {
		return err
	}
	boundary := conversationBoundary{Generation: uuid.NewString(), ResetAt: time.Now().UTC()}
	if s.dir != "" {
		data, err := json.Marshal(boundary)
		if err != nil {
			return err
		}
		if err := fileutil.WriteFileAtomic(s.boundaryPath(key), data, 0o600); err != nil {
			return fmt.Errorf("persist conversation reset: %w", err)
		}
	}
	s.boundaries[key] = boundary
	return nil
}

type conversationView struct {
	owner      *ConversationStore
	key        string
	generation string
	bound      bool
}

func (v *conversationView) apply(key string, operation func(SessionStore, string) error) error {
	v.owner.mu.Lock()
	defer v.owner.mu.Unlock()
	boundary, err := v.owner.boundaryLocked(key)
	if err != nil {
		return err
	}
	if v.bound && key == v.key && boundary.Generation != v.generation {
		return nil
	}
	if boundary.Generation != "" {
		key += ":generation:" + boundary.Generation
	}
	return operation(v.owner.store, key)
}

func (v *conversationView) write(key string, operation func(SessionStore, string) error) {
	if err := v.apply(key, operation); err != nil {
		log.Printf("session: conversation write: %v", err)
	}
}

func (v *conversationView) AddMessage(key, role, content string) {
	v.write(key, func(store SessionStore, resolved string) error { store.AddMessage(resolved, role, content); return nil })
}

func (v *conversationView) AddFullMessage(key string, message providers.Message) {
	v.write(key, func(store SessionStore, resolved string) error { store.AddFullMessage(resolved, message); return nil })
}

func (v *conversationView) GetHistory(key string) []providers.Message {
	history := []providers.Message{}
	if err := v.apply(key, func(store SessionStore, resolved string) error { history = store.GetHistory(resolved); return nil }); err != nil {
		log.Printf("session: conversation history: %v", err)
	}
	return history
}

func (v *conversationView) GetSummary(key string) string {
	var summary string
	if err := v.apply(key, func(store SessionStore, resolved string) error { summary = store.GetSummary(resolved); return nil }); err != nil {
		log.Printf("session: conversation summary: %v", err)
	}
	return summary
}

func (v *conversationView) SetSummary(key, summary string) {
	v.write(key, func(store SessionStore, resolved string) error { store.SetSummary(resolved, summary); return nil })
}

func (v *conversationView) SetHistory(key string, history []providers.Message) {
	v.write(key, func(store SessionStore, resolved string) error { store.SetHistory(resolved, history); return nil })
}

func (v *conversationView) TruncateHistory(key string, keepLast int) {
	v.write(key, func(store SessionStore, resolved string) error { store.TruncateHistory(resolved, keepLast); return nil })
}

func (v *conversationView) Save(key string) error {
	return v.apply(key, func(store SessionStore, resolved string) error { return store.Save(resolved) })
}

func (v *conversationView) Close() error { return v.owner.store.Close() }
