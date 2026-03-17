# Examples

## Learning Entry

```markdown
## [LRN-20260317-001] best_practice

**Logged**: 2026-03-17T21:45:00Z
**Priority**: high
**Status**: pending
**Area**: tools

### Summary
Telegram cross-chat forwards must not inherit reply targets from another chat

### Details
Forwarding a greeting from one Telegram chat to another reused the source
`reply_to_message_id`. Telegram rejected the send because the target chat did
not contain that message.

### Suggested Action
Drop reply targets on cross-chat and cross-channel sends.

### Metadata
- Source: investigation
- Related Files: pkg/channels/base.go, pkg/channels/telegram/telegram.go
- Tags: telegram, reply, routing

---
```

## Error Entry

~~~~markdown
## [ERR-20260317-001] openai_compat_image_model

**Logged**: 2026-03-17T22:05:00Z
**Priority**: high
**Status**: resolved
**Area**: models

### Summary
Image routing passed a model alias instead of the resolved provider model ID

### Error
```
API request failed:
Status: 400
Body: {"error":{"message":"Invalid model name passed in model=blackbig-kimi-k2.5"}}
```

### Context
- Operation: image-model routing
- Provider: openai_compat via blackbox proxy

### Suggested Fix
Pass the resolved model ID to the provider call, not the alias.

### Metadata
- Reproducible: yes
- Related Files: pkg/agent/instance.go

### Resolution
- **Resolved**: 2026-03-17T22:10:00Z
- **Commit/PR**: 195d6cd
- **Notes**: image route now uses the resolved provider model ID

---
~~~~

## Feature Request Entry

```markdown
## [FEAT-20260317-001] speech-to-text-tool

**Logged**: 2026-03-17T20:45:00Z
**Priority**: high
**Status**: resolved
**Area**: tools

### Requested Capability
Transcribe voice and media content into text

### User Context
Needed the agent to understand audio messages and media attachments.

### Complexity Estimate
medium

### Suggested Implementation
Add a dedicated transcription tool backed by NVIDIA Riva.

### Metadata
- Frequency: first_time
- Related Features: media handling

---
```
