#!/usr/bin/env bash
set -euo pipefail

OUTPUT="${CLAUDE_TOOL_OUTPUT:-${CODEX_TOOL_OUTPUT:-${TOOL_OUTPUT:-}}}"
EXIT_CODE="${CLAUDE_TOOL_EXIT_CODE:-${CODEX_TOOL_EXIT_CODE:-${TOOL_EXIT_CODE:-0}}}"

if [[ "${EXIT_CODE}" != "0" ]]; then
  cat <<'EOF'
<error-detected>
A tool exited non-zero. If the failure was unexpected, non-obvious, or likely to recur, log it to `.learnings/ERRORS.md`.
</error-detected>
EOF
  exit 0
fi

patterns=(
  "error:"
  "failed"
  "command not found"
  "No such file"
  "Permission denied"
  "fatal:"
  "Exception"
  "Traceback"
  "timeout"
  "429"
  "500"
)

for pattern in "${patterns[@]}"; do
  if [[ "${OUTPUT}" == *"${pattern}"* ]]; then
    cat <<'EOF'
<error-detected>
A tool output looked like a failure. Consider logging it to `.learnings/ERRORS.md` if diagnosis or prevention would help future sessions.
</error-detected>
EOF
    exit 0
  fi
done
