package agent

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

func TestTelegramToolProgressAndFinalReplyAreIndependent(t *testing.T) {
	for _, peer := range []string{"direct", "group"} {
		t.Run(peer, func(t *testing.T) {
			al, _, mb, _, cleanup := newTestAgentLoop(t)
			defer cleanup()
			provider := &scriptedToolProvider{}
			a := al.registry.GetDefaultAgent()
			a.Provider = provider
			al.RegisterTool(&mockCustomTool{})
			response, err := al.runAgentLoop(
				context.Background(),
				a,
				processOptions{
					SessionKey:       "progress-" + peer,
					Channel:          "telegram",
					ChatID:           "-100:4",
					PeerKind:         peer,
					ReplyToMessageID: "19",
					UserMessage:      "run tool",
					SendResponse:     true,
				},
			)
			if err != nil || response != "done" {
				t.Fatalf("response=%q err=%v", response, err)
			}
			var delivered []bus.OutboundMessage
			for {
				select {
				case msg := <-mb.OutboundChan():
					delivered = append(delivered, msg)
				default:
					goto drained
				}
			}
		drained:
			if len(delivered) != 3 {
				t.Fatalf("expected start, completion, answer; got %+v", delivered)
			}
			start, end, answer := delivered[0], delivered[1], delivered[2]
			if start.Progress == nil || end.Progress == nil || answer.Progress != nil {
				t.Fatalf("progress must be separate from answer: %+v", delivered)
			}
			if start.Progress.State != bus.ProgressStarted || end.Progress.State != bus.ProgressCompleted ||
				end.Progress.Result == "" {
				t.Fatalf("missing start/outcome: %+v %+v", start.Progress, end.Progress)
			}
			if start.Progress.RequestID == "" || start.Progress.ActionID == "" ||
				start.Progress.RequestID != end.Progress.RequestID ||
				start.Progress.ActionID != end.Progress.ActionID {
				t.Fatal("tool invocation identity was not preserved")
			}
			if start.ChatID != "-100:4" || start.ReplyToMessageID != "19" || end.ReplyToMessageID != "19" ||
				answer.Content != "done" {
				t.Fatalf("lost routing or answer: %+v", delivered)
			}
		})
	}
}
