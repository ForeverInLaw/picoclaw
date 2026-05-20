package agenthook

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/memory/facts"
	"github.com/sipeed/picoclaw/pkg/memory/facts/extract"
	"github.com/sipeed/picoclaw/pkg/memory/facts/recall"
	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

type fakeStore struct {
	hits []facts.RecallHit
}

func (s *fakeStore) Insert(ctx context.Context, f facts.Fact) (int64, error) { return 0, nil }
func (s *fakeStore) Update(ctx context.Context, f facts.Fact) error          { return nil }
func (s *fakeStore) SoftDelete(ctx context.Context, id int64) error          { return nil }
func (s *fakeStore) GetByID(ctx context.Context, id int64) (facts.Fact, error) {
	return facts.Fact{}, facts.ErrNotFound
}
func (s *fakeStore) FindByKey(ctx context.Context, ns, e, a string) (facts.Fact, error) {
	return facts.Fact{}, facts.ErrNotFound
}
func (s *fakeStore) ListByNamespace(ctx context.Context, ns []string) ([]facts.Fact, error) {
	return nil, nil
}
func (s *fakeStore) KNN(ctx context.Context, ns []string, q []float32, n float64, k int, m float64) ([]facts.RecallHit, error) {
	return s.hits, nil
}
func (s *fakeStore) KeywordSearch(ctx context.Context, ns []string, q string, k int) ([]facts.RecallHit, error) {
	return nil, nil
}
func (s *fakeStore) Close() error { return nil }

type stubEmbed struct{}

func (stubEmbed) Embed(ctx context.Context, text string) ([]float32, float64, error) {
	return []float32{1, 0, 0}, 1.0, nil
}

type fakeQueue struct{ jobs []extract.Job }

func (q *fakeQueue) Enqueue(j extract.Job) { q.jobs = append(q.jobs, j) }

func TestHook_BeforeLLM_PrependsRecalledFacts(t *testing.T) {
	store := &fakeStore{hits: []facts.RecallHit{
		{Fact: facts.Fact{Entity: "Андрей", Attribute: "likes", Value: "грейпфрут"}, Score: 0.9},
	}}
	r := recall.NewRecaller(store, stubEmbed{}, 5, 0.0)
	h := New(TelegramScope{BotUsername: "c0md_bot"}, r, nil)

	req := &agent.LLMHookRequest{
		Channel:  "telegram",
		ChatID:   "-100123",
		Messages: []protocoltypes.Message{{Role: "user", Content: "что любит Андрей?"}},
	}
	out, dec, err := h.BeforeLLM(context.Background(), req)
	if err != nil {
		t.Fatalf("BeforeLLM: %v", err)
	}
	if dec.Action != agent.HookActionModify {
		t.Fatalf("want modify, got %q", dec.Action)
	}
	if len(out.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(out.Messages))
	}
	if out.Messages[0].Role != "system" {
		t.Fatalf("first message should be system, got %q", out.Messages[0].Role)
	}
	if !strings.Contains(out.Messages[0].Content, "грейпфрут") {
		t.Fatalf("prefix missing recall: %q", out.Messages[0].Content)
	}
}

func TestHook_BeforeLLM_OtherChannelIsNoop(t *testing.T) {
	store := &fakeStore{hits: []facts.RecallHit{
		{Fact: facts.Fact{Entity: "X", Attribute: "Y", Value: "Z"}, Score: 0.9},
	}}
	r := recall.NewRecaller(store, stubEmbed{}, 5, 0.0)
	h := New(TelegramScope{BotUsername: "c0md_bot"}, r, nil)

	req := &agent.LLMHookRequest{
		Channel:  "discord",
		ChatID:   "99",
		Messages: []protocoltypes.Message{{Role: "user", Content: "hi"}},
	}
	out, dec, _ := h.BeforeLLM(context.Background(), req)
	if dec.Action != agent.HookActionContinue {
		t.Fatalf("want continue for non-telegram, got %q", dec.Action)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("messages should be unchanged, got %d", len(out.Messages))
	}
}

func TestHook_BeforeLLM_NoHitsIsNoop(t *testing.T) {
	store := &fakeStore{hits: nil}
	r := recall.NewRecaller(store, stubEmbed{}, 5, 0.0)
	h := New(TelegramScope{BotUsername: "c0md_bot"}, r, nil)
	req := &agent.LLMHookRequest{
		Channel:  "telegram",
		ChatID:   "1",
		Messages: []protocoltypes.Message{{Role: "user", Content: "hi"}},
	}
	_, dec, _ := h.BeforeLLM(context.Background(), req)
	if dec.Action != agent.HookActionContinue {
		t.Fatalf("want continue when no hits, got %q", dec.Action)
	}
}

func TestHook_AfterLLM_EnqueuesExtraction(t *testing.T) {
	q := &fakeQueue{}
	h := New(TelegramScope{BotUsername: "c0md_bot"}, nil, q)
	resp := &agent.LLMHookResponse{
		Meta:     agent.EventMeta{SessionKey: "tg:chat:-100123", Iteration: 4, TurnID: "t1"},
		Channel:  "telegram",
		Response: &protocoltypes.LLMResponse{Content: "ответ бота"},
	}
	_, dec, _ := h.AfterLLM(context.Background(), resp)
	if dec.Action != agent.HookActionContinue {
		t.Fatalf("want continue, got %q", dec.Action)
	}
	if len(q.jobs) != 1 {
		t.Fatalf("want 1 enqueued, got %d", len(q.jobs))
	}
	if q.jobs[0].SessionKey != "tg:chat:-100123" {
		t.Fatalf("session: %q", q.jobs[0].SessionKey)
	}
	if len(q.jobs[0].Window) == 0 {
		t.Fatal("window should include the assistant reply")
	}
}

func TestHook_AfterLLM_NoWindowSkipsEnqueue(t *testing.T) {
	q := &fakeQueue{}
	h := New(TelegramScope{BotUsername: "c0md_bot"}, nil, q)
	resp := &agent.LLMHookResponse{
		Meta:    agent.EventMeta{SessionKey: "tg:chat:-100123", Iteration: 4},
		Channel: "telegram",
	}
	_, _, _ = h.AfterLLM(context.Background(), resp)
	if len(q.jobs) != 0 {
		t.Fatalf("want 0 enqueued without window, got %d", len(q.jobs))
	}
}
