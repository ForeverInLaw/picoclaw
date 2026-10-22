// Package modelruntime persists bounded, sanitized model routing observations.
package modelruntime

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	MaxRecords    = 64
	MaxAttempts   = 64
	MaxCandidates = 64
	Heartbeat     = 30 * time.Second
	StaleAfter    = 90 * time.Second
)

const maxSnapshotBytes = 2 << 20

func Path(configPath string) string { return configPath + ".model-runtime.json" }

type Record struct {
	ID            string                      `json:"id"`
	ParentID      string                      `json:"parent_id,omitempty"`
	AgentID       string                      `json:"agent_id"`
	Kind          string                      `json:"kind"`
	StartedAt     time.Time                   `json:"started_at"`
	FinishedAt    time.Time                   `json:"finished_at,omitzero"`
	State         string                      `json:"state"`
	Chain         []providers.RoutingIdentity `json:"chain"`
	Selected      *providers.RoutingIdentity  `json:"selected,omitempty"`
	Attempts      []providers.RoutingEvent    `json:"attempts"`
	TotalAttempts int                         `json:"total_attempts"`
	Truncated     bool                        `json:"truncated,omitempty"`
}

type Snapshot struct {
	DroppedRecords int                             `json:"dropped_records,omitempty"`
	Version        int                             `json:"version"`
	RunID          string                          `json:"run_id"`
	PID            int                             `json:"pid"`
	StartedAt      time.Time                       `json:"started_at"`
	UpdatedAt      time.Time                       `json:"updated_at"`
	StoppedAt      time.Time                       `json:"stopped_at,omitzero"`
	Records        []Record                        `json:"records"`
	Availability   []providers.RoutingAvailability `json:"availability"`
}

// Collector updates memory synchronously and coalesces disk writes on one worker.
type Collector struct {
	mu           sync.Mutex
	writeMu      sync.Mutex
	path         string
	snapshot     Snapshot
	availability func() []providers.RoutingAvailability
	dirty        chan struct{}
	stop         chan struct{}
	done         chan struct{}
	closeOnce    sync.Once
}

func New(configPath string, availability func() []providers.RoutingAvailability) (*Collector, error) {
	now := time.Now().UTC()
	c := &Collector{
		path: Path(configPath),
		snapshot: Snapshot{
			Version:      1,
			RunID:        uuid.NewString(),
			PID:          os.Getpid(),
			StartedAt:    now,
			Records:      []Record{},
			Availability: []providers.RoutingAvailability{},
		},
		availability: availability,
		dirty:        make(chan struct{}, 1),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	if err := c.Flush(); err != nil {
		return nil, err
	}
	go c.writeLoop()
	return c, nil
}

func (c *Collector) writeLoop() {
	defer close(c.done)
	ticker := time.NewTicker(Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-c.dirty:
			_ = c.Flush()
		case <-ticker.C:
			_ = c.Flush()
		case <-c.stop:
			return
		}
	}
}

func bounded(value string) string {
	runes := []rune(value)
	if len(runes) > 200 {
		return string(runes[:200])
	}
	return value
}

func identity(value providers.RoutingIdentity) providers.RoutingIdentity {
	value.ID = bounded(value.ID)
	value.Name = bounded(value.Name)
	value.Model = bounded(value.Model)
	value.Provider = bounded(value.Provider)
	value.AccountID = bounded(value.AccountID)
	return value
}

func (c *Collector) changed() {
	select {
	case c.dirty <- struct{}{}:
	default:
	}
}

// Begin returns an opaque record handle; completed records reject late updates.
func (c *Collector) Begin(agentID, kind, parentID string, chain []providers.RoutingIdentity) *Trace {
	c.mu.Lock()
	defer c.mu.Unlock()
	record := Record{
		ID:        uuid.NewString(),
		ParentID:  bounded(parentID),
		AgentID:   bounded(agentID),
		Kind:      kind,
		StartedAt: time.Now().UTC(),
		State:     "running",
		Chain:     []providers.RoutingIdentity{},
		Attempts:  []providers.RoutingEvent{},
	}
	for _, candidate := range chain[:min(len(chain), MaxCandidates)] {
		record.Chain = append(record.Chain, identity(candidate))
	}
	record.Truncated = len(chain) > MaxCandidates
	c.snapshot.Records = append(c.snapshot.Records, record)
	if len(c.snapshot.Records) > MaxRecords {
		c.snapshot.DroppedRecords += len(c.snapshot.Records) - MaxRecords
		c.snapshot.Records = c.snapshot.Records[len(c.snapshot.Records)-MaxRecords:]
	}
	c.changed()
	return &Trace{collector: c, ID: record.ID}
}

type Trace struct {
	collector *Collector
	ID        string
}

func (t *Trace) Record(event providers.RoutingEvent) {
	if t == nil {
		return
	}
	c := t.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.snapshot.Records {
		record := &c.snapshot.Records[i]
		if record.ID != t.ID || !record.FinishedAt.IsZero() {
			continue
		}
		event.Candidate = identity(event.Candidate)
		event.Reason = bounded(event.Reason)
		if event.Phase == "attempt" {
			record.TotalAttempts++
		}
		record.Attempts = append(record.Attempts, event)
		if len(record.Attempts) > MaxAttempts {
			record.Attempts = record.Attempts[len(record.Attempts)-MaxAttempts:]
			record.Truncated = true
		}
		if event.Phase == "success" {
			selected := event.Candidate
			record.Selected = &selected
		}
		if event.Phase == "waiting" {
			record.State = "waiting"
		} else {
			record.State = "running"
		}
		c.changed()
		return
	}
}

func (t *Trace) Finish(state string) {
	if t == nil {
		return
	}
	c := t.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.snapshot.Records {
		record := &c.snapshot.Records[i]
		if record.ID == t.ID && record.FinishedAt.IsZero() {
			record.State = state
			record.FinishedAt = time.Now().UTC()
			c.changed()
			return
		}
	}
}

func (t *Trace) Child(kind string, chain []providers.RoutingIdentity) *Trace {
	if t == nil {
		return nil
	}
	return t.collector.Begin("", kind, t.ID, chain)
}

// Flush serializes writes, including the final shutdown snapshot.
func (c *Collector) Flush() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	available := []providers.RoutingAvailability{}
	if c.availability != nil {
		available = c.availability()
	}
	available = available[:min(len(available), MaxCandidates)]
	for i := range available {
		available[i].Candidate = identity(available[i].Candidate)
	}
	c.mu.Lock()
	c.snapshot.UpdatedAt = time.Now().UTC()
	c.snapshot.Availability = available
	data, err := json.Marshal(c.snapshot)
	for err == nil && len(data) > maxSnapshotBytes && len(c.snapshot.Records) > 1 {
		c.snapshot.Records = c.snapshot.Records[1:]
		c.snapshot.DroppedRecords++
		data, err = json.Marshal(c.snapshot)
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(c.path, data, 0o600)
}

func (c *Collector) Close() {
	c.closeOnce.Do(func() {
		close(c.stop)
		<-c.done
		c.mu.Lock()
		c.snapshot.StoppedAt = time.Now().UTC()
		c.mu.Unlock()
		_ = c.Flush()
	})
}

func Read(configPath string) (Snapshot, error) {
	file, err := os.Open(Path(configPath))
	if err != nil {
		return Snapshot{}, err
	}
	defer file.Close()
	info, statErr := file.Stat()
	if statErr != nil {
		return Snapshot{}, statErr
	}
	if info.Size() > maxSnapshotBytes {
		return Snapshot{}, errors.New("runtime snapshot exceeds size limit")
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(io.LimitReader(file, maxSnapshotBytes+1))
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, err
	}
	if snapshot.Version != 1 || len(snapshot.Records) > MaxRecords || len(snapshot.Availability) > MaxCandidates {
		return Snapshot{}, errors.New("unsupported runtime snapshot")
	}
	for _, record := range snapshot.Records {
		if len(record.Attempts) > MaxAttempts || len(record.Chain) > MaxCandidates {
			return Snapshot{}, errors.New("invalid runtime snapshot bounds")
		}
	}
	return snapshot, nil
}

// CaptureChain binds the first actual operation's priority before reserves are preferred.
func (t *Trace) CaptureChain(chain []providers.RoutingIdentity) {
	if t == nil {
		return
	}
	c := t.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.snapshot.Records {
		record := &c.snapshot.Records[i]
		if record.ID != t.ID || record.TotalAttempts > 0 || !record.FinishedAt.IsZero() {
			continue
		}
		record.Chain = []providers.RoutingIdentity{}
		for _, candidate := range chain[:min(len(chain), MaxCandidates)] {
			record.Chain = append(record.Chain, identity(candidate))
		}
		record.Truncated = record.Truncated || len(chain) > MaxCandidates
		c.changed()
		return
	}
}
