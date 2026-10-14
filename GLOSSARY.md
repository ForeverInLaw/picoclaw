# Picoclaw

Picoclaw provides assistant conversations through chat channels and Telegram inline calls.

## Language

**Inline conversation**:
A Telegram user's ongoing conversation with the assistant through inline calls, shared across the chats where that user invokes the bot. It is separate from the user's direct conversation and from group conversations.

**Tool activity message**:
A chat message showing actions performed by assistant tools, such as shell commands, file operations, and web searches. A channel may group consecutive actions into one activity message or show each action and its outcome separately.

**Conversation reset**:
The start of a new conversation without the previous dialog or its notes being automatically recalled. Saved facts remain available, and archived dialogs remain accessible through an explicit request.

**Saved fact**:
Information intentionally retained beyond a single conversation, distinct from the conversation's messages and temporary notes.

**MCP artifact**:
A complete tool text result retained behind an explicit reference owned by its agent and base conversation. It survives Conversation reset and restart until an operator explicitly removes it; its contents are recalled only through authorized access.

**Active context**:
The current conversation evidence prepared for the assistant's next answer, including recent messages and summaries of earlier messages.

**Conversation summary**:
A condensed account of conversation messages. Newly created summaries retain original evidence for explicit recovery; historical imported summaries may lack recoverable source messages.
