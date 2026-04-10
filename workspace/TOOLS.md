# Tools

Durable tool notes, integration quirks, and environment gotchas live here.

## Self-Improvement

- Use the `self-improvement` skill when failures, corrections, or reusable patterns appear.
- Search `.learnings/*.md` with `rg` before repeating work in an area that failed before.
- Promote proven tool and environment rules from `.learnings/` into this file.

## Logging Targets

- `.learnings/LEARNINGS.md` for corrections, knowledge gaps, and best practices
- `.learnings/ERRORS.md` for command, provider, and integration failures
- `.learnings/FEATURE_REQUESTS.md` for missing capabilities requested by users

## Chat Memory

- Use `chat_memory` proactively when the answer may depend on prior discussion, earlier agreements, who said what, or what was discussed in a chat or time window.
- Use `chat_memory` with `mode=summary` for questions like "what was discussed in this chat", "what happened over the last day", or "summarize that group".
- Use `chat_memory` with `mode=search` for questions like "who mentioned X", "when did we talk about Y", or "where was this discussed".
- Do not say you do not remember or cannot know until `chat_memory` has been checked.
- When you use `chat_memory`, explicitly tell the user that you checked chat memory or chat history.

## Personal Todo

- Use `personal_todo` for personal task management requests like "запиши мне", "добавь в мои задачи", "что у меня в todo", "покажи мой список", or "отметь пункт 3 выполненным".
- Do not store personal todo items in `memory/MEMORY.md` or retrieve them with `chat_memory`.
- `personal_todo` owns personal task state; use it instead of file tools for todo CRUD.

## Fact Check

- Use `fact_check` when the user explicitly asks to verify, fact-check, or check whether a claim or URL is true.
- Prefer `fact_check` over free-form `web_search` when the task is to produce a verification verdict with sources.
- If `fact_check` returns `mixed` or `unverified`, do not state the claim as certain.

## Telegram Stickers

- Use `send_sticker` for Telegram sticker sending.
- Do not use `exec`, `curl`, `read_file`, or config inspection to send Telegram stickers.
- If sticker sending is unavailable, skip the sticker instead of probing secrets or shelling out.

## Voice / Audio Transcription

- If `transcribe_media` is visible, use it to transcribe audio files, voice notes, or `media://` audio refs into text.
- Incoming audio may already be transcribed automatically before the main agent sees the message.
- The active ASR backend may be NVIDIA Riva, Whisper-compatible transcription, ElevenLabs, or another configured transcriber.
- Do not claim that audio transcription is unavailable until you have checked whether `transcribe_media` is present or whether the incoming audio was already transcribed upstream.
