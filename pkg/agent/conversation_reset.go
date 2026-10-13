package agent

import (
	"context"
	"fmt"
	"github.com/sipeed/picoclaw/pkg/session"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/logger"
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
		replyCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		if err := al.bus.PublishOutbound(
			replyCtx,
			bus.OutboundMessage{
				Channel:          msg.Channel,
				ChatID:           msg.ChatID,
				Content:          response,
				ReplyToMessageID: msg.MessageID,
			},
		); err != nil {
			logger.WarnCF("agent", "Failed to publish reset command reply", map[string]any{
				"channel": msg.Channel,
				"chat_id": msg.ChatID,
				"error":   err.Error(),
			})
		}
	}
	return handled
}

func captureConversationAgent(agent *AgentInstance, key string) (*AgentInstance, time.Time, error) {
	store, ok := agent.Sessions.(*session.ConversationStore)
	if !ok {
		return agent, time.Time{}, nil
	}
	turnStore, resetAt, err := store.CaptureTurn(key)
	if err != nil {
		return nil, time.Time{}, err
	}
	clone := *agent
	clone.Sessions = turnStore
	return &clone, resetAt, nil
}
