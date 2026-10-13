package pico

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"sync"
)

const (
	maxReplaySessions = 128
	maxReplayMessages = 64
)

// picoReplay retains the latest request's snapshots for brief disconnects. It
// does not replace persisted conversation history or replay tool operations.
// Its lock also orders reconnect snapshots before subsequent live events.
type picoReplay struct {
	mu       sync.Mutex
	sessions map[string]*picoSessionReplay
	order    []string
}

type picoSessionReplay struct {
	requestID string
	messages  []PicoMessage
}

func (p *picoReplay) session(id string) *picoSessionReplay {
	if p.sessions == nil {
		p.sessions = make(map[string]*picoSessionReplay)
	}
	if session := p.sessions[id]; session != nil {
		return session
	}
	if len(p.order) >= maxReplaySessions {
		delete(p.sessions, p.order[0])
		p.order = p.order[1:]
	}
	session := &picoSessionReplay{}
	p.sessions[id] = session
	p.order = append(p.order, id)
	return session
}

func (p *picoReplay) begin(sessionID, requestID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.session(sessionID)
	session.requestID = requestID
	session.messages = nil
}

func (p *picoReplay) retain(sessionID string, event PicoMessage) {
	if event.Type != TypeMessageCreate && event.Type != TypeMessageUpdate && event.Type != TypeMessageDelete && event.Type != TypeActivityUpdate {
		return
	}
	id, _ := event.Payload["message_id"].(string)
	if id == "" {
		return
	}
	session := p.session(sessionID)
	requestID, _ := event.Payload["reply_to_message_id"].(string)
	if requestID != "" && session.requestID != "" && requestID != session.requestID {
		return
	}
	replacedID, _ := event.Payload["replace_message_id"].(string)
	kept := session.messages[:0]
	for _, previous := range session.messages {
		previousID, _ := previous.Payload["message_id"].(string)
		if previousID == replacedID || previousID == id && event.Type == TypeMessageDelete {
			continue
		}
		if previousID == id {
			// A final result is monotonic even when a stale preview or duplicate
			// activity start arrives after its completed outcome.
			if previous.Payload["final"] == true && event.Payload["final"] != true {
				return
			}
			if (previous.Payload["state"] == bus.ProgressCompleted || previous.Payload["state"] == bus.ProgressFailed) && event.Payload["state"] == bus.ProgressStarted {
				return
			}
			previous.Payload = mergePayload(previous.Payload, event.Payload)
			event = previous
		} else {
			kept = append(kept, previous)
		}
	}
	if event.Type != TypeMessageDelete {
		if event.Type == TypeMessageUpdate {
			event.Type = TypeMessageCreate
		}
		kept = append(kept, event)
	}
	if len(kept) > maxReplayMessages {
		kept = kept[len(kept)-maxReplayMessages:]
	}
	session.messages = kept
}

func mergePayload(previous, current map[string]any) map[string]any {
	merged := make(map[string]any, len(previous)+len(current))
	for key, value := range previous {
		merged[key] = value
	}
	for key, value := range current {
		merged[key] = value
	}
	return merged
}
