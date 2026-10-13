# Isolate Telegram inline conversations

Keep a separate inline conversation for each Telegram user, shared across the chats where that user invokes the bot. Inline answers can be published in different chats, so their history and automatic retrieval must use only inline conversation history rather than direct or group conversations. Reusing the direct conversation would offer continuity with private messages, but could disclose their contents in a published inline answer.
