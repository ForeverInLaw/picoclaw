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
