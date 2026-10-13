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

This uses [Telego 1.12.1](https://github.com/mymmrac/telego/releases/tag/v1.12.1)
and [Telegram Bot API 10.3](https://core.telegram.org/bots/api), checked on
2026-10-13. A custom `base_url` must provide the same rich-message methods.

## Tool activity

Every executed tool in direct chats and groups gets a separate activity message,
including shell commands, file operations, web search, and custom tools. It starts
with the action preview and is edited with its result or error. Longer output is
available in a collapsed details block; output beyond the activity limit is
marked `[truncated]`. Activity delivery retries edit the same message and never
replace the assistant's answer. Inline tool permissions stay restricted and
inline destinations do not receive these activity messages.

## Answer streaming

When `streaming.enabled` is true, private chats receive native rich drafts.
Groups and inline destinations receive supported rich message edits. The final
answer is persisted independently, with its original reply and topic reference.
A failed preview disables further previews for that request while final delivery
continues; final edits can be retried once without restarting generation or tools.
Repeated finalization does not publish another answer, and cancellation stops
further draft or message updates.

Only answer content enters this stream. Tool activity and retry status have their
own message identities. Internal reasoning and commentary that introduces tool
calls do not appear in the answer preview.

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
