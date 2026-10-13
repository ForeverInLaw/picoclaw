package channels

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
)

func TestManagerBuildsIndependentNamedInstances(t *testing.T) {
	config.RegisterChannelSettings("instance_test", struct {
		Token string `json:"token"`
	}{})
	var tokens []string
	RegisterFactory("instance_test", func(cfg *config.Config, messageBus *bus.MessageBus) (Channel, error) {
		for _, entry := range cfg.ChannelList {
			if entry.Type != "instance_test" {
				continue
			}
			var settings struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal(entry.Settings, &settings); err != nil {
				return nil, err
			}
			tokens = append(tokens, settings.Token)
		}
		return &mockChannel{BaseChannel: *NewBaseChannel("instance_test", nil, messageBus, nil)}, nil
	})
	cfg := config.DefaultConfig()
	cfg.ChannelList = config.ChannelList{
		"one": {Enabled: true, Type: "instance_test", Settings: json.RawMessage(`{"token":"one-secret"}`)},
		"two": {Enabled: true, Type: "instance_test", Settings: json.RawMessage(`{"token":"two-secret"}`)},
	}
	messageBus := bus.NewMessageBus()
	defer messageBus.Close()
	manager, err := NewManager(cfg, messageBus, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(manager.GetEnabledChannels()) != 2 || len(tokens) != 2 || tokens[0] == tokens[1] {
		t.Fatalf("instance factory configs mixed: %v", tokens)
	}
	for _, name := range []string{"one", "two"} {
		ch, ok := manager.GetChannel(name)
		if !ok || ch.Name() != name {
			t.Fatalf("instance %s missing", name)
		}
		ch.(*mockChannel).PublishInbound(
			context.Background(),
			bus.InboundMessage{
				Channel: "instance_test",
				ChatID:  "42",
				Sender:  bus.SenderInfo{CanonicalID: "instance_test:42"},
			},
		)
		msg := <-messageBus.InboundChan()
		if msg.Channel != name || msg.Sender.CanonicalID != "instance_test:42" {
			t.Fatalf("identity changed: %#v %v", msg, nil)
		}
	}
}
