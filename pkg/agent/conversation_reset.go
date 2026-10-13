package agent

import "fmt"

func (al *AgentLoop) resetConversation(agent *AgentInstance, key string) error {
	store, ok := agent.Sessions.(interface{ ResetConversation(string) error })
	if !ok {
		return fmt.Errorf("session backend does not support conversation reset")
	}
	if err := store.ResetConversation(key); err != nil {
		return err
	}
	if turn := al.getActiveTurnState(key); turn != nil {
		turn.requestHardAbort()
	}
	return nil
}
