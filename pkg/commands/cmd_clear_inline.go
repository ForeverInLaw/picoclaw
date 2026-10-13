package commands

import "context"

func clearInlineCommand() Definition {
	return Definition{
		Name:        "clear_inline",
		Description: "Start a fresh inline conversation across destination chats",
		Usage:       "/clear_inline",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.ClearInlineHistory == nil {
				return req.Reply(unavailableMsg)
			}
			if err := rt.ClearInlineHistory(); err != nil {
				return req.Reply("Failed to reset inline conversation: " + err.Error())
			}
			return req.Reply("Inline conversation reset across all destination chats.")
		},
	}
}
