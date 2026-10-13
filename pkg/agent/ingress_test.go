package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
)

type ingressRequest struct {
	messages []providers.Message
	tools    []providers.ToolDefinition
	release  chan struct{}
}

type ingressProvider struct{ requests chan ingressRequest }

func (p *ingressProvider) GetDefaultModel() string { return "ingress" }

func (p *ingressProvider) Chat(
	ctx context.Context,
	m []providers.Message,
	defs []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	request := ingressRequest{
		messages: append([]providers.Message(nil), m...),
		tools:    append([]providers.ToolDefinition(nil), defs...),
		release:  make(chan struct{}),
	}
	select {
	case p.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-request.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &providers.LLMResponse{Content: "answer to " + m[len(m)-1].Content}, nil
}

func startIngressLoop(t *testing.T) (*AgentLoop, *bus.MessageBus, *ingressProvider) {
	t.Helper()
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	provider := &ingressProvider{requests: make(chan ingressRequest, 10)}
	al.registry.GetDefaultAgent().Provider = provider
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Run failed to stop")
		}
		al.Close()
		mb.Close()
		cleanup()
	})
	return al, mb, provider
}

func inboundRequest(t *testing.T, provider *ingressProvider) ingressRequest {
	t.Helper()
	select {
	case request := <-provider.requests:
		return request
	case <-time.After(2 * time.Second):
		t.Fatal("incoming request did not reach the model")
		return ingressRequest{}
	}
}

func publishIncoming(t *testing.T, mb *bus.MessageBus, msg bus.InboundMessage) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := mb.PublishInbound(ctx, msg); err != nil {
		t.Fatal(err)
	}
}

func awaitInboundDrain(t *testing.T, mb *bus.MessageBus) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for len(mb.InboundChan()) > 0 {
		select {
		case <-deadline.C:
			t.Fatal("active turn did not consume new inbound message")
		case <-time.After(time.Millisecond):
		}
	}
}

func finalOutbound(t *testing.T, mb *bus.MessageBus) bus.OutboundMessage {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case msg := <-mb.OutboundChan():
			if msg.Progress == nil {
				return msg
			}
		case <-deadline.C:
			t.Fatal("final reply was not delivered")
			return bus.OutboundMessage{}
		}
	}
}

func directIngressMessage(content string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   "telegram",
		ChatID:    "42",
		SenderID:  "telegram:42",
		Peer:      bus.Peer{Kind: "direct", ID: "42"},
		MessageID: "19",
		Content:   content,
	}
}

func TestRunFinalReplyPreservesInboundMessageAndTopic(t *testing.T) {
	for _, chatID := range []string{"42", "-123/9"} {
		t.Run(chatID, func(t *testing.T) {
			_, mb, provider := startIngressLoop(t)
			msg := directIngressMessage("reply here")
			msg.ChatID = chatID
			msg.MessageID = "31"
			publishIncoming(t, mb, msg)
			request := inboundRequest(t, provider)
			close(request.release)
			out := finalOutbound(t, mb)
			if out.ChatID != chatID || out.ReplyToMessageID != "31" {
				t.Fatalf("final reply target = (%q,%q), want (%q,31)", out.ChatID, out.ReplyToMessageID, chatID)
			}
		})
	}
}

func TestRunRejectsCapturedConversationResetBeforeTurnRegistration(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	defer mb.Close()
	agent := al.registry.GetDefaultAgent()
	store := agent.Sessions.(*session.ConversationStore)
	msg := directIngressMessage("stale captured request")
	msg.SessionKey = "agent:main:registration-gap"
	captured, _, err := store.CaptureTurn(msg.SessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ResetConversation(msg.SessionKey); err != nil {
		t.Fatal(err)
	}
	agent.Sessions = captured
	provider := &retryScriptProvider{}
	agent.Provider = provider
	sub := al.SubscribeEvents(32)
	defer al.UnsubscribeEvents(sub.ID)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	defer func() { cancel(); <-done }()
	publishIncoming(t, mb, msg)
	for {
		select {
		case event := <-sub.C:
			if event.Kind != EventKindTurnEnd {
				continue
			}
			payload := event.Payload.(TurnEndPayload)
			if payload.Status != TurnEndStatusAborted || provider.calls != 0 {
				t.Fatalf("stale captured turn status=%s model calls=%d", payload.Status, provider.calls)
			}
			select {
			case reply := <-mb.OutboundChan():
				t.Fatalf("stale captured turn replied: %+v", reply)
			default:
			}
			return
		case <-time.After(2 * time.Second):
			t.Fatal("stale captured turn did not terminate")
		}
	}
}

func TestRunBoundsAggregatePendingRequestsAndKeepsResetResponsive(t *testing.T) {
	_, mb, provider := startIngressLoop(t)
	publishIncoming(t, mb, directIngressMessage("blocked private turn"))
	_ = inboundRequest(t, provider)
	for n := 0; n < 64; n++ {
		publishIncoming(t, mb, selectedInlineMessage("42", fmt.Sprintf("accepted-%d", n), "queued selection"))
	}
	awaitInboundDrain(t, mb)
	overflow := selectedInlineMessage("42", "rejected-first", "overflow")
	overflow.MessageID = "overflow-ref"
	publishIncoming(t, mb, overflow)
	busy := finalOutbound(t, mb)
	if busy.ChatID != overflow.ChatID || busy.ReplyToMessageID != overflow.MessageID ||
		!strings.Contains(strings.ToLower(busy.Content), "busy") {
		t.Fatalf("overflow must finish its original publication: %+v", busy)
	}
	publishIncoming(t, mb, directIngressMessage("/clear"))
	reset := finalOutbound(t, mb)
	if !strings.Contains(strings.ToLower(reset.Content), "clear") {
		t.Fatalf("full queue blocked reset: %+v", reset)
	}
	_ = inboundRequest(t, provider)
	publishIncoming(t, mb, selectedInlineMessage("42", "accepted-next", "new selection"))
	publishIncoming(t, mb, selectedInlineMessage("42", "rejected-second", "new overflow"))
	busy = finalOutbound(t, mb)
	if busy.ChatID != "inline:rejected-second" || !strings.Contains(strings.ToLower(busy.Content), "busy") {
		t.Fatalf("inherited queue and drainer must share one capacity: %+v", busy)
	}
	publishIncoming(t, mb, directIngressMessage("/clear_inline"))
	reset = finalOutbound(t, mb)
	if reset.ChatID != "42" || !strings.Contains(strings.ToLower(reset.Content), "reset") {
		t.Fatalf("inline reset did not remain responsive: %+v", reset)
	}
}

func TestRunQueuesSelectedInlineRequestsWithTheirOwnTransportAndContext(t *testing.T) {
	cases := []struct {
		name          string
		first, second bus.InboundMessage
		wantHistory   bool
	}{
		{
			"direct then inline",
			directIngressMessage("PRIVATE_DIRECT"),
			selectedInlineMessage("42", "published", "PUBLIC_INLINE"),
			false,
		},
		{
			"inline then direct",
			selectedInlineMessage("42", "published", "PUBLIC_INLINE"),
			directIngressMessage("PRIVATE_DIRECT"),
			false,
		},
		{
			"same inline conversation different publication",
			selectedInlineMessage("42", "first", "FIRST_INLINE"),
			selectedInlineMessage("42", "second", "SECOND_INLINE"),
			true,
		},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			_, mb, p := startIngressLoop(t)
			publishIncoming(t, mb, scenario.first)
			first := inboundRequest(t, p)
			publishIncoming(t, mb, scenario.second)
			awaitInboundDrain(t, mb)
			close(first.release)
			firstReply := finalOutbound(t, mb)
			if firstReply.ChatID != scenario.first.ChatID ||
				!strings.Contains(firstReply.Content, scenario.first.Content) {
				t.Fatalf("first reply routed to wrong publication: %+v", firstReply)
			}
			second := inboundRequest(t, p)
			input := conversationInput(second.messages)
			if strings.Contains(input, scenario.first.Content) != scenario.wantHistory {
				t.Fatalf("unexpected conversation mixing: %s", input)
			}
			if isInlineMessage(scenario.second) {
				for _, tool := range second.tools {
					if !newToolAllowlist(inlineToolAllowlist()).Allows(tool.Function.Name) {
						t.Fatalf("queued inline request gained unsafe tool %q", tool.Function.Name)
					}
				}
			}
			close(second.release)
			secondReply := finalOutbound(t, mb)
			if secondReply.ChatID != scenario.second.ChatID ||
				!strings.Contains(secondReply.Content, scenario.second.Content) {
				t.Fatalf("selected request lost its destination: %+v", secondReply)
			}
		})
	}
}

var _ providers.LLMProvider = (*ingressProvider)(nil)

func TestRunResetCommandsCancelAndResetActiveConversationsImmediately(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(fmt.Sprintf("inline=%v", inline), func(t *testing.T) {
			_, mb, p := startIngressLoop(t)
			first := directIngressMessage("OLD_ACTIVE_DIALOG")
			command := directIngressMessage("/clear@picoclaw")
			command.MessageID = "20"
			followup := directIngressMessage("FRESH_FOLLOWUP")
			if inline {
				first = selectedInlineMessage("42", "published", "OLD_ACTIVE_DIALOG")
				command.Content = "!CLEAR_INLINE"
				followup = selectedInlineMessage("42", "next", "FRESH_FOLLOWUP")
			}
			publishIncoming(t, mb, first)
			active := inboundRequest(t, p)
			if !inline {
				steering := directIngressMessage("OLD_QUEUED_STEERING")
				publishIncoming(t, mb, steering)
				awaitInboundDrain(t, mb)
			}
			publishIncoming(t, mb, command)
			reply := finalOutbound(t, mb)
			if reply.ChatID != "42" ||
				!strings.Contains(
					strings.ToLower(reply.Content),
					map[bool]string{false: "cleared", true: "reset"}[inline],
				) {
				t.Fatalf("reset command did not reply independently: %+v", reply)
			}
			// The command must have completed while the original model operation was blocked.
			select {
			case <-active.release:
				t.Fatal("test released the active operation before reset")
			default:
			}
			publishIncoming(t, mb, followup)
			fresh := inboundRequest(t, p)
			input := conversationInput(fresh.messages)
			if strings.Contains(input, "OLD_ACTIVE_DIALOG") || strings.Contains(input, "OLD_QUEUED_STEERING") ||
				strings.Contains(input, command.Content) {
				t.Fatalf("reset retained old/command context: %s", input)
			}
			close(fresh.release)
			answer := finalOutbound(t, mb)
			if answer.ChatID != followup.ChatID || !strings.Contains(answer.Content, "FRESH_FOLLOWUP") {
				t.Fatalf("fresh reply lost transport: %+v", answer)
			}
		})
	}
}

func TestRunStopWakesIdleLoop(t *testing.T) {
	al, _, mb, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	defer al.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- al.Run(ctx) }()
	publishIncoming(t, mb, directIngressMessage("hello"))
	_ = finalOutbound(t, mb)
	// Let Run return to its idle receive before invoking its public stop lifecycle.
	time.Sleep(10 * time.Millisecond)
	al.Stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("Stop left Run blocked on an idle bus")
	}
}

func TestRunRetrievesFreshConversationAfterResetAndRestart(t *testing.T) {
	for _, passive := range []bool{false, true} {
		t.Run(fmt.Sprintf("passiveGroup=%t", passive), func(t *testing.T) {
			initial, cfg, _, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			initial.Close()
			cfg.Agents.Defaults.MemoryIndex = config.MemoryIndexConfig{
				Enabled:         true,
				MaxResults:      5,
				MaxSnippetChars: 500,
				MinQueryChars:   3,
			}
			run := func() (*AgentLoop, *bus.MessageBus, *ingressProvider, func()) {
				mb := bus.NewMessageBus()
				p := &ingressProvider{requests: make(chan ingressRequest, 10)}
				al := NewAgentLoop(cfg, mb, p)
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- al.Run(ctx) }()
				return al, mb, p, func() { cancel(); <-done; al.Close(); mb.Close() }
			}
			al, mb, p, stop := run()
			msg := directIngressMessage("OLD_DIALOG bicycle")
			msg.SessionKey = "agent:main:fresh-retrieval"
			if passive {
				msg.ChatID = "-123"
				msg.Peer = bus.Peer{Kind: "group", ID: "-123"}
			}
			publishIncoming(t, mb, msg)
			request := inboundRequest(t, p)
			close(request.release)
			_ = finalOutbound(t, mb)
			msg.Content = "/clear"
			publishIncoming(t, mb, msg)
			_ = finalOutbound(t, mb)
			agent := al.registry.GetDefaultAgent()
			if err := agent.MemoryIndex.AddObservation(
				t.Context(),
				memoryindex.Observation{
					SessionKey: msg.SessionKey,
					Channel:    "telegram",
					ChatID:     msg.ChatID,
					PeerKind:   "direct",
					Role:       "user",
					Content:    "OLD_LATE_WRITE bicycle",
					CreatedAt:  time.Now(),
				},
			); err != nil {
				t.Fatal(err)
			}
			msg.Content = "FRESH_RETRIEVAL bicycle"
			if passive {
				msg.Metadata = map[string]string{"observe_only": "true"}
			}
			publishIncoming(t, mb, msg)
			if passive {
				deadline := time.Now().Add(time.Second)
				for !strings.Contains(conversationInput(agent.Sessions.GetHistory(msg.SessionKey)), "FRESH_RETRIEVAL") {
					if time.Now().After(deadline) {
						t.Fatal("passive group message was not recorded")
					}
					time.Sleep(time.Millisecond)
				}

				for {
					hits, err := agent.MemoryIndex.Search(
						t.Context(),
						memoryindex.SearchRequest{Query: "bicycle", Channel: "telegram", ChatID: msg.ChatID, Limit: 10},
					)
					found := false
					for _, hit := range hits {
						found = found || strings.Contains(hit.Content, "FRESH_RETRIEVAL")
					}
					if err != nil {
						t.Fatal(err)
					}
					if found {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("passive message did not reach retrieval index")
					}
					time.Sleep(time.Millisecond)
				}
				msg.Metadata = nil
			} else {
				request = inboundRequest(t, p)
				close(request.release)
				_ = finalOutbound(t, mb)
			}
			agent.Sessions.SetHistory(msg.SessionKey, nil)
			if err := agent.Sessions.Save(msg.SessionKey); err != nil {
				t.Fatal(err)
			}
			stop()
			_, mb, p, stop = run()
			defer stop()
			msg.Content = "bicycle"
			publishIncoming(t, mb, msg)
			request = inboundRequest(t, p)
			input := conversationInput(request.messages)
			close(request.release)
			_ = finalOutbound(t, mb)
			if !strings.Contains(input, "RETRIEVED_MEMORY") || !strings.Contains(input, "FRESH_RETRIEVAL") ||
				strings.Contains(input, "OLD_DIALOG") ||
				strings.Contains(input, "OLD_LATE_WRITE") {
				t.Fatalf(
					"fresh conversation retrieval did not preserve its generation boundary across restart: %s",
					input,
				)
			}
		})
	}
}

func TestRunFallbackUsesConfiguredProviderEndpointAndCredentials(t *testing.T) {
	for _, protocol := range []string{"anthropic", "qwen-international", "dashscope-intl"} {
		for _, reference := range []string{"backup-alias", protocol + "/claude-backup", "claude-backup"} {
			t.Run(protocol+"/"+reference, func(t *testing.T) {
				var primaryCalls, backupCalls atomic.Int32
				primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					primaryCalls.Add(1)
					if r.Header.Get("Authorization") != "Bearer primary-key" {
						t.Error("primary credentials changed")
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"message":"temporary unavailable"}}`))
				}))
				defer primary.Close()
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					n := backupCalls.Add(1)
					var body struct {
						Model string `json:"model"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.Model != "claude-backup" || r.Header.Get("Authorization") != "Bearer backup-key" {
						t.Errorf("fallback model/auth=%s/%s", body.Model, r.Header.Get("Authorization"))
					}
					if n == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = w.Write([]byte(`{"error":{"message":"temporary unavailable"}}`))
						return
					}
					_, _ = w.Write(
						[]byte(
							`{"choices":[{"message":{"role":"assistant","content":"cross-provider recovered"},"finish_reason":"stop"}]}`,
						),
					)
				}))
				defer backup.Close()
				initial, cfg, _, _, cleanup := newTestAgentLoop(t)
				defer cleanup()
				initial.Close()
				cfg.Agents.Defaults.Model = "primary-alias"
				cfg.Agents.Defaults.ModelFallbacks = []string{reference}
				cfg.ModelList = []config.ModelConfig{
					{ModelName: "primary-alias", Model: "openai/gpt-primary", APIKey: "primary-key", APIBase: primary.URL},
					{
						ModelName: "backup-alias",
						Model:     protocol + "/claude-backup",
						APIKey:    "backup-key",
						APIBase:   backup.URL,
					},
				}
				provider, _, err := providers.CreateProviderFromConfig(&cfg.ModelList[0])
				if err != nil {
					t.Fatal(err)
				}
				mb := bus.NewMessageBus()
				defer mb.Close()
				al := NewAgentLoop(cfg, mb, provider)
				defer al.Close()
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan error, 1)
				go func() { done <- al.Run(ctx) }()
				defer func() { cancel(); <-done }()
				publishIncoming(t, mb, directIngressMessage("recover through backup"))
				out := finalOutbound(t, mb)
				if primaryCalls.Load() != 1 || backupCalls.Load() != 2 || out.Content != "cross-provider recovered" {
					t.Fatalf(
						"fallback endpoint/attempts primary=%d backup=%d reply=%q",
						primaryCalls.Load(),
						backupCalls.Load(),
						out.Content,
					)
				}
			})
		}
	}
}

func TestRunResetControlsRemainResponsiveWithBackpressuredAcknowledgements(t *testing.T) {
	al, mb, p := startIngressLoop(t)
	sub := al.SubscribeEvents(32)
	defer al.UnsubscribeEvents(sub.ID)
	publishIncoming(t, mb, directIngressMessage("blocked turn"))
	_ = inboundRequest(t, p)
	for n := 0; n < 64; n++ {
		if err := mb.PublishOutbound(
			t.Context(),
			bus.OutboundMessage{Channel: "telegram", ChatID: "42", Content: "queued output"},
		); err != nil {
			t.Fatal(err)
		}
	}
	publishIncoming(t, mb, directIngressMessage("/clear_inline"))
	publishIncoming(t, mb, directIngressMessage("/clear"))
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-sub.C:
			if event.Kind == EventKindTurnEnd && event.Payload.(TurnEndPayload).Status == TurnEndStatusAborted {
				return
			}
		case <-deadline.C:
			t.Fatal("independent inline-reset acknowledgement blocked the subsequent active reset")
		}
	}
}

func TestRunResetRevokesCompletedAnswerWaitingForOutboundCapacity(t *testing.T) {
	al, mb, p := startIngressLoop(t)
	sub := al.SubscribeEvents(32)
	defer al.UnsubscribeEvents(sub.ID)
	publishIncoming(t, mb, directIngressMessage("OLD_COMPLETED_ANSWER"))
	req := inboundRequest(t, p)
	store := al.GetRegistry().GetDefaultAgent().Sessions.(*session.ConversationStore)
	captured, _, err := store.CaptureTurn("agent:main:main")
	if err != nil {
		t.Fatal(err)
	}
	view := captured.(session.ConversationTurn)
	for n := 0; n < 64; n++ {
		if err := mb.PublishOutbound(t.Context(), bus.OutboundMessage{Channel: "telegram", ChatID: "42", Content: "queued output"}); err != nil {
			t.Fatal(err)
		}
	}
	close(req.release)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ended := false
	for !ended {
		select {
		case event := <-sub.C:
			ended = event.Kind == EventKindTurnEnd
		case <-deadline.C:
			t.Fatal("model turn did not finish before publication")
		}
	}
	publishIncoming(t, mb, directIngressMessage("/clear"))
	resetDeadline := time.Now().Add(time.Second)
	for {
		current, err := view.IsCurrent()
		if err != nil {
			t.Fatal(err)
		}
		if !current {
			break
		}
		if time.Now().After(resetDeadline) {
			t.Fatal("completed-turn publication backpressure stranded reset control")
		}
		time.Sleep(time.Millisecond)
	}
	for n := 0; n < 64; n++ {
		<-mb.OutboundChan()
	}
	publishIncoming(t, mb, directIngressMessage("NEW_GENERATION"))
	fresh := inboundRequest(t, p)
	close(fresh.release)
	for {
		out := finalOutbound(t, mb)
		if strings.Contains(out.Content, "OLD_COMPLETED_ANSWER") {
			t.Fatal("old generation answer was published after reset")
		}
		if strings.Contains(out.Content, "NEW_GENERATION") {
			if out.DeliveryID == "" {
				t.Fatal("final answer has no logical delivery identity")
			}
			break
		}
	}
}

func TestRunDuplicateFallbackAliasPreservesSelectedProviderModelAndRetryClient(t *testing.T) {
	var primaryCalls, firstCalls, secondCalls, mismatches atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
	}))
	defer primary.Close()
	backupHandler := func(expectedModel, expectedKey string, calls *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Model != expectedModel || r.Header.Get("Authorization") != "Bearer "+expectedKey {
				mismatches.Add(1)
			}
			if calls.Add(1)%2 == 1 {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
				return
			}
			_, _ = w.Write(
				[]byte(
					`{"choices":[{"message":{"role":"assistant","content":"duplicate alias recovered"},"finish_reason":"stop"}]}`,
				),
			)
		}
	}
	first := httptest.NewServer(backupHandler("claude-first", "first-key", &firstCalls))
	defer first.Close()
	second := httptest.NewServer(backupHandler("gpt-second", "second-key", &secondCalls))
	defer second.Close()
	for run := 0; run < 2; run++ {
		initial, cfg, _, _, cleanup := newTestAgentLoop(t)
		initial.Close()
		cfg.Agents.Defaults.Model = "primary"
		cfg.Agents.Defaults.ModelFallbacks = []string{"duplicated-backup"}
		cfg.ModelList = []config.ModelConfig{
			{ModelName: "primary", Model: "openai/gpt-primary", APIKey: "primary-key", APIBase: primary.URL},
			{ModelName: "duplicated-backup", Model: "anthropic/claude-first", APIKey: "first-key", APIBase: first.URL},
			{ModelName: "duplicated-backup", Model: "openai/gpt-second", APIKey: "second-key", APIBase: second.URL},
		}
		provider, _, err := providers.CreateProviderFromConfig(&cfg.ModelList[0])
		if err != nil {
			t.Fatal(err)
		}
		mb := bus.NewMessageBus()
		al := NewAgentLoop(cfg, mb, provider)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- al.Run(ctx) }()
		publishIncoming(t, mb, directIngressMessage("recover duplicated alias"))
		out := finalOutbound(t, mb)
		cancel()
		<-done
		al.Close()
		mb.Close()
		cleanup()
		if out.Content != "duplicate alias recovered" || mismatches.Load() != 0 ||
			primaryCalls.Load() != int32(run+1) ||
			firstCalls.Load()+secondCalls.Load() != int32(2*(run+1)) {
			t.Fatalf(
				"selected alias config drifted: response=%q mismatches=%d calls=%d/%d/%d",
				out.Content,
				mismatches.Load(),
				primaryCalls.Load(),
				firstCalls.Load(),
				secondCalls.Load(),
			)
		}
	}
	if firstCalls.Load() != 2 || secondCalls.Load() != 2 {
		t.Fatalf("duplicate alias round-robin was not preserved: calls=%d/%d", firstCalls.Load(), secondCalls.Load())
	}
}

func TestRunConcreteModelSwitchKeepsExactReferenceWithinDuplicateAlias(t *testing.T) {
	for _, reference := range []string{"anthropic/claude-first", "claude-first"} {
		t.Run(reference, func(t *testing.T) {
			var firstCalls, wrongCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				firstCalls.Add(1)
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Model != "claude-first" || r.Header.Get("Authorization") != "Bearer first-key" {
					t.Errorf("selected endpoint received model/auth %s/%s", body.Model, r.Header.Get("Authorization"))
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"exact model"},"finish_reason":"stop"}]}`))
			}))
			defer first.Close()
			wrong := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				wrongCalls.Add(1)
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"wrong model"},"finish_reason":"stop"}]}`))
			}))
			defer wrong.Close()
			initial, cfg, _, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			initial.Close()
			cfg.Agents.Defaults.Model = "initial"
			cfg.ModelList = []config.ModelConfig{
				{ModelName: "initial", Model: "openai/initial", APIBase: wrong.URL, APIKey: "initial-key"},
				{ModelName: "duplicated", Model: "anthropic/claude-first", APIBase: first.URL, APIKey: "first-key"},
				{ModelName: "duplicated", Model: "openai/second", APIBase: wrong.URL, APIKey: "wrong-key"},
			}
			p, _, err := providers.CreateProviderFromConfig(&cfg.ModelList[0])
			if err != nil {
				t.Fatal(err)
			}
			mb := bus.NewMessageBus()
			defer mb.Close()
			al := NewAgentLoop(cfg, mb, p)
			defer al.Close()
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- al.Run(ctx) }()
			defer func() { cancel(); <-done }()
			for n := 0; n < 3; n++ {
				publishIncoming(t, mb, directIngressMessage("/switch model to "+reference))
				if out := finalOutbound(t, mb); !strings.Contains(out.Content, reference) {
					t.Fatalf("model selection failed: %q", out.Content)
				}
				publishIncoming(t, mb, directIngressMessage("use the selected concrete model"))
				if out := finalOutbound(t, mb); out.Content != "exact model" {
					t.Fatalf("model drifted: %q", out.Content)
				}
			}
			if firstCalls.Load() != 3 || wrongCalls.Load() != 0 {
				t.Fatalf("exact selection calls=%d/%d", firstCalls.Load(), wrongCalls.Load())
			}
		})
	}
}
