#!/usr/bin/env bash
set -euo pipefail

cat <<'EOF'
<self-improvement-reminder>
After completing this task, evaluate whether durable knowledge emerged:
- user correction or new project fact
- non-obvious workaround or best practice
- tool, provider, or integration failure
- missing capability requested by the user

If yes, log it to workspace `.learnings/` and promote only the short prevention rule.
</self-improvement-reminder>
EOF
