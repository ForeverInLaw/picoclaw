---
name: tg-sticker-emoji-mood
description: Proactively send Telegram stickers that match the conversation mood. Use only in Telegram chats, mostly for casual banter, jokes, greetings, celebration, or light emotional support. Skip for serious technical or task-focused replies. Uses the bundled send_sticker.sh helper.
metadata:
  openclaw:
    emoji: "📦"
---

# Telegram Sticker Mood

Use this skill only when replying in Telegram and the conversation would genuinely benefit from a sticker.

The goal is to make the bot feel more expressive, not noisy.

## When To Use

Use a sticker when at least one is true:
- the conversation is playful, joking, or meme-heavy
- the user shared good news or hype
- you are greeting or saying goodbye
- you just completed something and want a small celebratory reaction
- the user is mildly frustrated and a soft supportive sticker fits

Do not use a sticker when:
- the user asked a serious technical question
- the user is debugging, asking for code changes, or expects focus
- the conversation is formal, tense, or business-like
- you already sent a sticker recently and the vibe has not changed
- the user asked you to stop

Default rate:
- casual chat: occasional, not constant
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

Use the helper:

```bash
bash {baseDir}/scripts/send_sticker.sh --sticker-set "supermegahype" --emoji "😂"
```

The script resolves the current Telegram chat automatically from the exec tool environment.

If you already know a file_id, use:

```bash
bash {baseDir}/scripts/send_sticker.sh --sticker "CAACAgIAAxkBA..."
```

To inspect a sticker set:

```bash
bash {baseDir}/scripts/send_sticker.sh --list-set "supermegahype"
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
- `supermegahype` for hype, jokes, celebration, chaos, silly reactions
- `rostikbalbes` for chill, deadpan, awkward, confused, or everyday reactions

If both seem plausible:
- prefer `supermegahype` for louder/funnier energy
- prefer `rostikbalbes` for dryer/quieter energy

## Rules

- Only for Telegram.
- Only use `supermegahype` and `rostikbalbes`.
- Never assume the user wants stickers in serious conversations.
- One sticker max.
- If a set lookup fails, either try another set once or skip the sticker.
- Do not turn the conversation into sticker spam.
