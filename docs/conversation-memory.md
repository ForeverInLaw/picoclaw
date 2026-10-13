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
