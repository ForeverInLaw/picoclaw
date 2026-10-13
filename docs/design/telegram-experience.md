# Telegram experience

Agreed design from the Telegram feature discussion. Implementation is pending on `feat/telegram-experience`, based on `dev`.

## Rich messages and formulas

- Upgrade the existing Telego dependency to the latest stable release supported by the current Telegram Bot API. At review time these are Telego 1.12.1 and Bot API 10.3.
- Use native Rich Markdown for final replies in direct chats, groups, and inline conversations.
- Support native LaTeX formulas both within text and as separate display blocks. Reuse Telegram's rendering rather than adding an image-based formula renderer.
- Use native rich drafts in direct chats. Group and inline updates must use their supported message-editing paths because rich drafts require a private chat.
- Preserve delivery of long replies, reply references, topics, and error fallbacks during the migration.

Sources: [Telegram rich messages](https://core.telegram.org/bots/api#rich-messages), [rich drafts](https://core.telegram.org/bots/api#sendrichmessagedraft), [Telego releases](https://github.com/mymmrac/telego/releases).

## Inline conversations

- Persist a separate inline conversation per agent and Telegram user. Calls made by that user in different chats continue the same inline conversation.
- Keep its history and automatic retrieval separate from direct and group conversations; do not import their private context.
- Record selected inline calls, rather than every query emitted while a user types.
- Use a bounded model context and summarize older history through the existing session infrastructure.
- Add `/clear_inline` in the bot's direct chat to reset that user's inline conversation across all chats.
- Preserve the existing restriction on tools available in inline mode; this work does not grant shell or file access there.

Decision: [isolate inline conversations](../adr/0001-isolate-telegram-inline-conversations.md).

## Tool activity messages

- Show every tool call in direct chats and groups, including shell commands, file operations, searches, and other tools.
- Create one short message per call containing the tool and a useful preview of the command or action.
- Update that same message when the call completes, showing a short result or error. Make longer output available in a collapsible block within Telegram's message limits.
- Keep tool activity messages separate from the assistant reply and its streaming placeholder.
- Reuse the existing tool execution events and extend their delivery with result information as needed. Avoid tool-specific progress implementations and duplicate result messages.

## Conversation reset

- `/clear` resets the current conversation's history and summary.
- Earlier dialog excerpts and temporary notes must not be automatically added to the new conversation, including after a process restart.
- Keep intentionally saved facts and archived dialogs. Searching old dialogs remains possible through an explicit user request.
- Report persistence failures rather than claiming a successful reset.
- Apply the same reset semantics to `/clear_inline`, scoped to the user's inline conversation.

Decision: [reset versus memory deletion](../adr/0002-separate-conversation-reset-from-memory-deletion.md).

The existing archive search is disabled by default. Whether it explains the reported reset behavior depends on the deployed configuration; workspace memory and recent notes are another source of retained context.

## Reasoning, streaming, and retries

- Do not substitute reasoning for an empty final answer.
- Keep intermediate tool-call commentary separate from the final answer stream. Preserve content type or phase where a provider supplies it; buffer ambiguous content until its purpose is known.
- If the model returns neither a final answer nor a tool call, make one additional attempt to obtain an answer, then report a clear failure.
- Apply a common retry policy in direct chats and inline conversations: up to two retries for transient network failures, timeouts, and temporary server errors, with increasing delays.
- For temporary HTTP 429 responses, respect the API's requested delay within a bounded overall wait. Authentication errors, invalid requests, and exhausted quota should produce a clear failure.
- Show a short retry status such as “Retrying, attempt 2/3”.
- Retry the failed operation rather than restarting the whole turn and repeating completed tool actions. Coordinate SDK retries, model fallback, and application retries so nested loops do not multiply the request budget.
- Handle Telegram delivery retries separately from model requests. A failed intermediate edit should not silently leave the user without further progress or a final outcome.

## Search providers

- Add both Parallel and Exa behind the existing `SearchProvider` interface and `web_search` tool.
- Make the primary provider configurable; default to Parallel and use Exa as the backup for transient failures.
- Keep provider-specific request and response handling inside each adapter. Return the existing common result shape with titles, URLs, and useful excerpts.
- Configure credentials through the existing configuration mechanism. Provider availability and credentials still need verification for live API checks.

Sources: [Parallel search](https://docs.parallel.ai/search/search-quickstart), [Exa search](https://exa.ai/docs/reference/search).

## Implementation order and verification

1. Fix reasoning leakage and establish bounded retries without replaying completed actions.
2. Upgrade Telegram support and verify Rich Markdown, inline/display formulas, and each supported delivery mode.
3. Separate tool activity delivery from answer delivery, then verify shell, file, search, and error outcomes.
4. Implement a persistent conversation reset boundary and verify that old dialog context stays excluded after restart while saved facts remain.
5. Add isolated inline history and reset; verify continuity across chats and isolation from direct/group context.
6. Add both search adapters and verify primary selection, transient fallback, and normalized results.

Use focused tests for these behaviors. Live Telegram verification is needed for native formula rendering, rich message edits, and tool activity presentation; live search checks require configured provider credentials.
