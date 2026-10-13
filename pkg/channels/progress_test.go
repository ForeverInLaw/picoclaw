package channels

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
)

type progressChannel struct {
	mockChannel
	activities []bus.OutboundMessage
}

func (c *progressChannel) SendProgress(_ context.Context, msg bus.OutboundMessage) error {
	c.activities = append(c.activities, msg)
	return nil
}

func TestProgressDoesNotConsumeFinalAnswerPlaceholder(t *testing.T) {
	m := newTestManager()
	ch := &progressChannel{
		mockChannel: mockChannel{sendFn: func(context.Context, bus.OutboundMessage) error { return nil }},
	}
	m.channels["telegram"] = ch
	m.workers["telegram"] = newChannelWorker("telegram", ch)
	m.RecordPlaceholder("telegram", "-100:4", "answer-placeholder")
	for _, request := range []string{"first", "second"} {
		for _, state := range []bus.ProgressState{bus.ProgressStarted, bus.ProgressCompleted} {
			if err := m.SendMessage(
				context.Background(),
				bus.OutboundMessage{
					Channel:          "telegram",
					ChatID:           "-100:4",
					Content:          "activity",
					ReplyToMessageID: "19",
					Progress: &bus.ProgressMessage{
						RequestID: request,
						ActionID:  "tool-1",
						Kind:      bus.ProgressTool,
						State:     state,
					},
				},
			); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !m.UpdatePlaceholder(context.Background(), "telegram", "-100:4", "answer preview") {
		t.Fatal("progress consumed the answer placeholder")
	}
	if err := m.SendMessage(
		context.Background(),
		bus.OutboundMessage{Channel: "telegram", ChatID: "-100:4", Content: "final answer", ReplyToMessageID: "19"},
	); err != nil {
		t.Fatal(err)
	}
	if len(ch.activities) != 4 || ch.editedMessages != 2 || len(ch.sentMessages) != 0 {
		t.Fatalf(
			"activities=%d answer edits=%d extra sends=%d",
			len(ch.activities),
			ch.editedMessages,
			len(ch.sentMessages),
		)
	}
	if ch.activities[2].Progress.RequestID != "second" || ch.activities[2].ReplyToMessageID != "19" {
		t.Fatal("lost request or reply association")
	}
}
