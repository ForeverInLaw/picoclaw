package pico

import (
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
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
	requests       map[string]uint64
	requestOrder   []string
	sequence       uint64
	activeSequence uint64
	activeRequest  string
	messages       []PicoMessage
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

func (p *picoReplay) recordRequest(sessionID, requestID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := p.session(sessionID)
	if session.requests == nil {
		session.requests = make(map[string]uint64)
	}
	if _, exists := session.requests[requestID]; exists {
		return
	}
	session.sequence++
	session.requests[requestID] = session.sequence
	session.requestOrder = append(session.requestOrder, requestID)
	if len(session.requestOrder) > maxReplayMessages {
		delete(session.requests, session.requestOrder[0])
		session.requestOrder = session.requestOrder[1:]
	}
}

func (p *picoReplay) retain(sessionID string, event PicoMessage) {
	if event.Type != TypeMessageCreate && event.Type != TypeMessageUpdate && event.Type != TypeMessageDelete &&
		event.Type != TypeActivityUpdate {
		return
	}
	id, _ := event.Payload["message_id"].(string)
	if id == "" {
		return
	}
	session := p.session(sessionID)
	requestID, _ := event.Payload["reply_to_message_id"].(string)
	if requestID != "" {
		sequence, known := session.requests[requestID]
		if requestID == session.activeRequest {
			sequence, known = session.activeSequence, true
		}
		if !known && len(session.requests) > 0 {
			return
		}
		if !known {
			sequence = session.activeSequence + 1
		}
		if sequence < session.activeSequence {
			return
		}
		if sequence > session.activeSequence {
			session.activeSequence, session.activeRequest = sequence, requestID
			session.messages = nil
		}
	}
	replacedID, _ := event.Payload["replace_message_id"].(string)
	kept := make([]PicoMessage, 0, len(session.messages)+1)
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
			if (previous.Payload["state"] == bus.ProgressCompleted || previous.Payload["state"] == bus.ProgressFailed) &&
				event.Payload["state"] == bus.ProgressStarted {
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
	for len(kept) > maxReplayMessages {
		discard := 0
		for index, candidate := range kept {
			if candidate.Payload["final"] != true {
				discard = index
				break
			}
		}
		kept = append(kept[:discard], kept[discard+1:]...)
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
