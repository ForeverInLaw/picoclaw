# Hook Setup

Use hooks only in environments that support them directly, such as Claude Code or Codex-style local agents. Picoclaw itself relies on workspace prompt injection and does not need these hooks to use the skill.

## Claude Code

Project-level example:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "./workspace/skills/self-improvement/scripts/activator.sh"
          }
        ]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "./workspace/skills/self-improvement/scripts/error-detector.sh"
          }
        ]
      }
    ]
  }
}
```

## Codex-Style Local Setup

Use the same pattern in `.codex/settings.json` if your local agent runner supports command hooks:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "./workspace/skills/self-improvement/scripts/activator.sh"
          }
        ]
      }
    ]
  }
}
```

## Runtime Workspace Setup

If you run directly inside the deployed workspace, the relative path usually becomes:

```bash
./skills/self-improvement/scripts/activator.sh
./skills/self-improvement/scripts/error-detector.sh
```

## Verification

- Trigger the activator and check that it emits a `<self-improvement-reminder>` block
- Run a failing shell command and confirm the detector emits `<error-detected>`
- Dry-run skill extraction:

```bash
./workspace/skills/self-improvement/scripts/extract-skill.sh test-skill --dry-run
```
