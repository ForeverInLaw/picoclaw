---
name: self-improvement
description: "Capture corrections, errors, missing capabilities, and durable patterns into workspace `.learnings/`. Use when a tool or provider fails, the user corrects the agent, a requested capability does not exist, a non-obvious workaround is discovered, or a proven pattern should be promoted into AGENTS.md, TOOLS.md, SOUL.md, USER.md, or memory/MEMORY.md. Review `.learnings/*.md` before major work in areas that failed before."
---

# Self-Improvement

Use this skill to turn mistakes, corrections, and discoveries into reusable workspace knowledge instead of losing them in chat history.

## Quick Reference

| Situation | Action |
|-----------|--------|
| Tool, shell, provider, or integration failure | Append an `ERR-...` entry to `.learnings/ERRORS.md` |
| User correction or new project fact | Append an `LRN-...` entry to `.learnings/LEARNINGS.md` |
| Missing capability request | Append a `FEAT-...` entry to `.learnings/FEATURE_REQUESTS.md` |
| Same issue keeps recurring | Reuse the existing entry, add `See Also`, bump `Recurrence-Count`, and consider promotion |
| Broadly useful rule | Distill and promote it to `AGENTS.md`, `TOOLS.md`, `SOUL.md`, `USER.md`, or `memory/MEMORY.md` |
| Reusable solution | Scaffold a new skill with `scripts/extract-skill.sh` or `scripts/extract-skill.ps1` |

## Picoclaw Workflow

1. Review `.learnings/*.md` before major work in an area with prior failures.
2. Log immediately after a failure, correction, or non-obvious discovery.
3. Keep the raw entry specific: what happened, what was wrong, and what to do differently next time.
4. Promote only concise, durable rules. Do not copy incident writeups into prompt files.
5. Mark entries `resolved`, `promoted`, or `promoted_to_skill` when the loop closes.

Read [references/picoclaw-integration.md](references/picoclaw-integration.md) when you need the workspace-specific promotion map and runtime file layout.

## Logging Rules

### Learnings

Use `.learnings/LEARNINGS.md` for:
- user corrections
- knowledge gaps
- durable best practices
- better approaches discovered through debugging or implementation

Use category values like `correction`, `knowledge_gap`, `best_practice`, or `insight`.

### Errors

Use `.learnings/ERRORS.md` for:
- failing commands
- provider/API failures
- broken integrations
- timeouts, unexpected output, and reproducible runtime breakage

Prefer copying the exact failure text into the `### Error` block.

### Feature Requests

Use `.learnings/FEATURE_REQUESTS.md` for:
- missing tools
- missing channels or integrations
- workflow gaps
- repeated user asks that the current system cannot satisfy cleanly

## Promotion Targets

Promote terse rules, not long stories:

- `AGENTS.md` for workflows, sequencing, review loops, and automation rules
- `TOOLS.md` for tool gotchas, provider quirks, auth requirements, and environment notes
- `SOUL.md` for behavior, tone, and operating principles
- `USER.md` for durable user preferences, permissions, and hard constraints
- `memory/MEMORY.md` for project facts or durable local knowledge that should survive future sessions

When in doubt, keep the raw incident in `.learnings/` and promote only the prevention rule.

## Recurring Pattern Handling

Before adding a new entry for a familiar issue:

1. Search `.learnings/` with `rg`.
2. If an entry exists, update it instead of creating noise.
3. Use a stable `Pattern-Key` when the issue is a repeated class of failure.
4. Increase `Recurrence-Count` and update `Last-Seen`.
5. Promote the rule once it is proven across multiple tasks.

Useful searches:

```bash
rg -n "Pattern-Key: " .learnings
rg -n "Status\\*\\*: pending" .learnings
rg -n "See Also:" .learnings
```

## Review Cadence

Review `.learnings/`:
- before major work
- after non-trivial fixes
- after repeated failures in the same area
- when the user says the agent keeps making the same mistake

Read [references/examples.md](references/examples.md) for concrete entry examples.

## Scripts

Use these helpers when they reduce repetition:

- `scripts/activator.sh` emits a minimal reminder block for hook-based environments
- `scripts/error-detector.sh` emits a reminder when a tool output looks like a failure
- `scripts/extract-skill.sh` scaffolds a new skill under the workspace `skills/` directory
- `scripts/extract-skill.ps1` is the PowerShell equivalent for Windows environments

Read [references/hooks-setup.md](references/hooks-setup.md) only if you need hook-based activation in Claude Code or Codex-style environments. Picoclaw itself relies on workspace prompt injection, not those hooks.

## Skill Extraction

Promote a learning into a standalone skill when at least one is true:

- the same issue recurs across tasks
- the fix is verified and non-obvious
- the pattern is broadly useful outside one file or one incident
- the user explicitly asks to save it as a skill

Workflow:

1. Choose a lowercase hyphenated skill name.
2. Run `scripts/extract-skill.sh <skill-name> --dry-run` or `scripts/extract-skill.ps1 <skill-name> -DryRun`.
3. Fill in the generated `SKILL.md`.
4. Update the source learning to `promoted_to_skill` and add `Skill-Path`.

Use [assets/SKILL-TEMPLATE.md](assets/SKILL-TEMPLATE.md) if you create the skill manually.
