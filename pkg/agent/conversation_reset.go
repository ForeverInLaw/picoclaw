package agent

import (
	"context"
	"fmt"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
)

func (al *AgentLoop) resetConversation(agent *AgentInstance, key string) error {
	store, ok := agent.Sessions.(interface{ ResetConversation(key string) error })
	if !ok {
		return fmt.Errorf("session backend does not support conversation reset")
	}
	if err := store.ResetConversation(key); err != nil {
		return err
	}
	if al.steering != nil {
		al.steering.clearScope(key)
	}
	if turn := al.getActiveTurnState(key); turn != nil {
		turn.requestHardAbort()
	}
	return nil
}

// Resets control conversation lifetime and must bypass active-turn steering.
func (al *AgentLoop) handleActiveReset(ctx context.Context, msg bus.InboundMessage) bool {
	name, ok := commands.ParseCommandName(msg.Content)
	if !ok || name != "clear" && name != "clear_inline" {
		return false
	}
	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil {
		return false
	}
	key, err := messageConversationKey(msg, route, agent)
	if err != nil {
		return false
	}
	opts := processOptions{
		SessionKey:     key,
		Channel:        msg.Channel,
		ChatID:         msg.ChatID,
		PeerKind:       msg.Peer.Kind,
		TelegramUserID: telegramUserID(msg),
	}
	response, handled := al.handleCommand(ctx, msg, agent, &opts)
	if handled && response != "" {
		_ = al.bus.PublishOutbound(
			ctx,
			bus.OutboundMessage{
				Channel:          msg.Channel,
				ChatID:           msg.ChatID,
				Content:          response,
				ReplyToMessageID: msg.MessageID,
			},
		)
	}
	return handled
}
