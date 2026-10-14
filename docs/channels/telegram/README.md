> Back to [README](../../../README.md)

# Telegram

The Telegram channel uses long polling via the Telegram Bot API for bot-based communication. It supports text messages, media attachments (photos, voice, audio, documents), voice transcription via Groq Whisper, and built-in command handling.

## Configuration

```json
{
  "channels": {
    "telegram": {
      "enabled": true,
      "token": "123456789:ABCdefGHIjklMNOpqrsTUVwxyz",
      "allow_from": ["123456789"],
      "proxy": ""
    }
  }
}
```

| Field      | Type   | Required | Description                                                        |
| ---------- | ------ | -------- | ------------------------------------------------------------------ |
| enabled    | bool   | Yes      | Whether to enable the Telegram channel                             |
| token      | string | Yes      | Telegram Bot API Token                                             |
| allow_from | array  | No       | Allowlist of user IDs; empty means all users are allowed           |
| proxy      | string | No       | Proxy URL for connecting to the Telegram API (e.g. http://127.0.0.1:7890) |

## Setup

1. Search for `@BotFather` in Telegram
2. Send the `/newbot` command and follow the prompts to create a new bot
3. Obtain the HTTP API Token
4. Fill in the Token in the configuration file
5. (Optional) Configure `allow_from` to restrict which user IDs can interact (you can get IDs via `@userinfobot`)

## Rich replies

Guest mentions such as `@bot question` are handled through Telegram's
`guest_message` updates and `answerGuestQuery`. They use the same isolated
conversation and retained answer storage as selected inline results. This is
separate from BotFather's `/setinline` setting for the inline result picker.

Guest replies use Telegram's native reply reference without adding another
copy of the request inside the answer. Selected inline results keep their
request quote because the typed query need not appear as a chat message.
New assistant turns store the model's answer without delivery formatting;
quotes intentionally written by the model remain part of its answer. Existing
history is retained without rewriting old messages.

Replies in direct chats, groups, topics, and inline destinations use Telegram's
native Rich Markdown: headings, lists, tables, code blocks, and LaTeX formulas.
Inline math uses `$x^2$`; display math uses `$$E = mc^2$$` or a `math` code block.
Model output using `\(…\)` and `\[…\]` is normalized outside source code.
No TeX renderer or image upload is needed.

Rich replies support 32,768 characters per message. Longer direct and group
answers are split into additional messages, preserving topic routing and the
first message's reply reference. Long inline answers use Previous/Next buttons;
the complete result is retained under the workspace's `.state/telegram-inline`
directory, so navigation survives a bot restart. Published result pages are
separate from conversation history. Formatting rejection falls back to readable
plain Markdown with Telegram's smaller plain-text limit; inline fallback also
keeps all pages available.

Repeated chosen-result updates preserve a confirmed answer and its current
navigation page. An interrupted publication retries the retained generated
source through a stable inline edit, without another model request. Failed
admission remains retryable, including after restart.

This uses [Telego 1.12.1](https://github.com/mymmrac/telego/releases/tag/v1.12.1)
and [Telegram Bot API 10.3](https://core.telegram.org/bots/api), checked on
2026-10-13. A custom `base_url` must provide the same rich-message methods.

## Tool activity

The `message` tool sends its content directly and does not create a duplicate
activity message. With tool feedback enabled, the answer streams into a new
message after tool activity; the earlier thinking placeholder is not reused.

Executed tools other than `message` in direct chats and groups get a separate activity message,
including shell commands, file operations, web search, and custom tools. It starts
with the action preview and is edited with its result or error. Longer output is
available in a collapsed details block; output beyond the activity limit is
marked `[truncated]`. Activity delivery retries edit the same message and never
replace the assistant's answer. Inline tool permissions stay restricted and
inline destinations do not receive these activity messages.

## Answer streaming

When `streaming.enabled` is true, private chats receive native rich drafts when
tool feedback is disabled. With tool feedback enabled, a new answer message is
sent after activity and edited as tokens arrive. Groups and inline destinations
receive supported rich message edits. The final
answer is persisted independently, with its original reply and topic reference.
A failed preview disables further previews for that request while final delivery
continues; final edits can be retried once without restarting generation or tools.
Repeated finalization does not publish another answer, and cancellation stops
further draft or message updates.

Tool activity and retry status have their own message identities. Providers with
explicit content phases stream only answer text. Chat Completions providers
stream public `content` deltas without waiting for the complete response;
separate reasoning fields stay private. If the response requests tools, its
preview is canceled before tool execution and the next response gets a fresh
stream. Configuring backup models does not disable streaming.

Inline page navigation is restricted to the user who selected that inline result
and to its original published destination. Retained results expire after 30 days;
the per-bot store is capped at 256 results and 64 MiB, retiring older results when
needed. Expired page buttons report that the page is unavailable. Keep the workspace
state directory when restarting the bot if published page navigation is needed.

Inline recovery status appears as a disabled keyboard row beneath the answer.
Updating that row leaves answer text and formulas intact. The row is cleared
when recovery completes or fails, and final delivery replaces it with any
required page navigation. Queued retry updates cannot restore status after the
answer has already been finalized.

Tool activity shows each action and a concise completion. Tools may supply explicit public activity detail (`ForActivity`); file operations, searches, and shell commands retain their intended public output. LLM-only results such as archived context stay private. Activity detail passes through the configured sensitive-data filter and does not create extra standalone messages on other channels.

Multipart final replies track each acknowledged part across stream finalization and manager delivery. A logical delivery ID follows the request through both paths; independent messages have distinct IDs even when their text and destination match. Recovery resumes the remaining parts and retries edits by their stable message ID. If a new send loses its acknowledgement, delivery reports an unknown outcome and stops automatic resends for that operation. Expiring a pending cursor also reports failure, without confirming the failed stream or authorizing its replay. Pending recovery state is bounded and remains local to the running process.

Delivery IDs use timestamped UUIDv7 identities activated at the first final
delivery, so model/tool generation and previews do not consume the ten-minute
recovery lifetime. An expired ID is rejected even after another send prunes its
cursor; retaining unbounded failure tombstones is unnecessary.
