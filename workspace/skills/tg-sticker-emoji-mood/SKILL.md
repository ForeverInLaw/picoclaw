---
name: tg-sticker-emoji-mood
description: Proactively send Telegram stickers that match the conversation mood. Use only in Telegram chats, mostly for casual banter, jokes, greetings, celebration, or light emotional support. Skip for serious technical or task-focused replies. Use the native send_sticker tool only.
metadata:
  openclaw:
    emoji: "📦"
---

# Telegram Sticker Mood

Use this skill only when replying in Telegram and the conversation would benefit from a sticker.

The goal is to make the bot feel expressive and alive. Prefer sending a sticker in casual chat unless the conversation is clearly serious or task-focused.

## When To Use

Use a sticker when at least one is true:
- the conversation is playful, joking, or meme-heavy
- the user shared good news or hype
- you are greeting or saying goodbye
- you just completed something and want a small celebratory reaction
- the user is mildly frustrated and a soft supportive sticker fits
- the exchange is short, casual, or emotional and a sticker would make the response feel more natural

Do not use a sticker when:
- the user asked a serious technical question
- the user is debugging, asking for code changes, or expects focus
- the conversation is formal, tense, or business-like
- the user asked you to stop

Default rate:
- casual chat: frequent; default to using a sticker unless there is a reason not to
- serious/tool-heavy chat: almost never

## Order

Prefer this order:
1. send the text reply
2. send one sticker if it improves the tone

For very casual greetings or pure banter, sticker-first is acceptable.

Never send more than one sticker for a single reply.

## Allowed Sticker Packs

Use only these packs:
- `supermegahype`
- `rostikbalbes`

Do not use any other sticker sets unless the user explicitly updates this allowlist.

## How To Send

Always use the native `send_sticker` tool.

Preferred call:

```json
{"sticker_set":"rostikbalbes","emoji":"😂"}
```

If you already know a Telegram sticker `file_id`, you may call:

```json
{"sticker":"CAACAgIAAxkBA..."}
```

If you need to inspect the contents of an allowed set first, use:

```json
{"list_set":"supermegahype"}
```

## Mood Mapping

- Funny / banter: `😂`, `🤣`, `😆`
- Hype / success: `🎉`, `🥳`, `🚀`, `✅`
- Support / comfort: `🫂`, `💙`, `🥺`
- Greeting / goodbye: `👋`, `🤗`
- Thinking / shrug: `🤔`, `🤷`
- Chill / vibe: `😎`, `✌️`

## Pack Preferences

Use these packs only:
- `rostikbalbes` as the default and preferred pack for most situations
- `supermegahype` only when you want a louder, more chaotic, more meme-heavy reaction

If both seem plausible:
- prefer `rostikbalbes` by default
- use `supermegahype` only for explicitly loud, celebratory, or absurd energy

## Rules

- Only for Telegram.
- Only use `supermegahype` and `rostikbalbes`.
- Use `send_sticker` only. Do not use `exec`, `curl`, `read_file`, or inspect config files to send stickers.
- Never assume the user wants stickers in serious conversations.
- One sticker max.
- Prefer `rostikbalbes` unless `supermegahype` is clearly a better fit.
- If a set lookup fails, either try another set once or skip the sticker.
- Do not turn the conversation into sticker spam.
