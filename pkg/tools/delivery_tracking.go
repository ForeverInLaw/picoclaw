package tools

// DeliveredMessage records a user-visible message that a tool sent directly.
type DeliveredMessage struct {
	Channel string
	ChatID  string
	Content string
}

// RoundDeliveryTracker is implemented by tools that send user-visible
// messages directly and need the agent loop to suppress duplicate final
// responses and persist delivered content.
type RoundDeliveryTracker interface {
	ResetSentInRound()
	HasSentInRound() bool
	DeliveredInRound() []DeliveredMessage
}
