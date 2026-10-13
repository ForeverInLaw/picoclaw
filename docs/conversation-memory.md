# Conversation memory

`/clear` starts a new conversation while preserving previous transcripts as an
archive. The active generation is committed atomically in
`sessions/boundaries/`; reopening the same storage keeps the reset boundary.
An active turn captures its generation, so its later writes, summaries, and
rollback cannot refill a conversation that was reset. A successful reset also
cancels that turn. Storage failures produce a failed reset reply and leave the
active generation unchanged.

After reset, automatic archive retrieval and shared daily notes are excluded.
Unclassified content in `memory/MEMORY.md` is also excluded: writing to that
file alone does not establish that its content is intentionally retained.
Saved facts are the content under an explicit `Saved facts` Markdown heading
in `memory/MEMORY.md`, or the content of `memory/SAVED_FACTS.md`. New facts
should use either of these forms. Existing unclassified memory is preserved
on disk; classify facts intentionally before expecting them to survive reset
as automatic context.

Explicit archive requests can still use `chat_memory` with its existing chat
access controls. Reset does not erase indexed records or archive files. It
also does not remove agent instructions, skills, or the user profile.

The historical reset symptom was reproduced using `MEMORY.md` and recent daily
notes while archive retrieval was disabled. `memory_index.enabled` defaults to
false; production configuration was not supplied or inspected, so archive
retrieval is not assumed to explain the deployed behavior. No production
containers or ports were changed.

## Telegram inline conversations

Only a selected Telegram inline result starts a turn. Typing and abandoned
queries never enter session storage. Each conversation uses the current agent
and Telegram user UID as its identity; an inline message ID is only a delivery
destination. Selected questions and replies therefore continue across chats
and bot restarts, while different users and agents remain separate.

Inline model context includes its own transcript and summary, agent
instructions, and skills. It excludes direct/group transcripts, automatic
archive retrieval, `USER.md`, Saved facts, and shared workspace memory or daily
notes. Inline turns are not added to the private chat-memory index. Existing
context budgeting and summarization apply to inline history, including repeated
compression when one pass does not make the history fit. The tool allowlist
remains `web_search`, `web_fetch`, and `fact_check`.

Send `/clear_inline` in the bot's private chat to start a fresh Inline
conversation for the current agent across destinations. It leaves your direct
conversation and other users' Inline conversations intact. `/clear` resets the
current direct/group conversation independently. Both commands report storage
errors, and inline reset uses the same persistent generation boundary as normal
reset.
