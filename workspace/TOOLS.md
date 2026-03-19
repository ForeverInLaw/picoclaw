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
