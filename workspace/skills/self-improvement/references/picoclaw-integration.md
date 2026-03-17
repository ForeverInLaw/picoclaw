# Picoclaw Integration

Use this skill inside the Picoclaw workspace, not as a generic repo-level notes folder.

## Runtime Layout

Local repo workspace:

```text
workspace/
├── .learnings/
│   ├── LEARNINGS.md
│   ├── ERRORS.md
│   └── FEATURE_REQUESTS.md
├── AGENTS.md
├── SOUL.md
├── TOOLS.md
├── USER.md
├── IDENTITY.md
├── memory/
│   └── MEMORY.md
└── skills/
    └── self-improvement/
```

Typical deployed workspace:

```text
/home/andy/.picoclaw/workspace/
```

## Prompt Injection

Picoclaw injects these files into the system context:

- `AGENTS.md`
- `SOUL.md`
- `TOOLS.md`
- `USER.md`
- `IDENTITY.md`
- `memory/MEMORY.md`

That means promotion targets are real runtime context, not archival notes.

## Promotion Map

- `AGENTS.md` for process rules, execution order, or review loops
- `TOOLS.md` for tool safety, provider quirks, auth gotchas, or environment details
- `SOUL.md` for communication style and behavioral rules
- `USER.md` for durable user-specific constraints or preferences
- `memory/MEMORY.md` for project facts and long-lived local knowledge

Keep one-off incidents in `.learnings/`. Promote only the short prevention rule.

## Operational Guidance

Use `.learnings/` as the backlog of unresolved mistakes and requests:

- `LEARNINGS.md` for corrections and durable insights
- `ERRORS.md` for failures that required diagnosis
- `FEATURE_REQUESTS.md` for missing capabilities

Review these files before repeating work in the same domain.

## What Picoclaw Does Not Provide

Picoclaw does not currently have the same hook system as Claude Code or OpenClaw. Do not assume automatic per-tool reminders. The primary activation mechanisms here are:

- `self-improvement` as a workspace skill
- explicit guidance in `AGENTS.md` and `TOOLS.md`
- manual or periodic review of `.learnings/`
