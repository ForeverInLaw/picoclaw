package email

import (
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
)

func init() {
	channels.RegisterFactory("email", func(cfg *config.Config, messageBus *bus.MessageBus) (channels.Channel, error) {
		return NewEmailChannel(cfg, messageBus)
	})
}
