#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_ROOT="$(cd -- "${SCRIPT_DIR}/../../.." && pwd)"
DEFAULT_OUTPUT_DIR="${WORKSPACE_ROOT}/skills"

usage() {
  cat <<EOF
Usage: $(basename "$0") <skill-name> [--dry-run] [--output-dir RELATIVE_PATH]

Create a new workspace skill scaffold from a learning.
Default output directory: skills/
EOF
}

SKILL_NAME=""
DRY_RUN=false
OUTPUT_DIR="skills"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=true
      shift
      ;;
    --output-dir)
      if [[ -z "${2:-}" ]]; then
        echo "missing value for --output-dir" >&2
        exit 1
      fi
      OUTPUT_DIR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      echo "unknown option: $1" >&2
      exit 1
      ;;
    *)
      if [[ -n "${SKILL_NAME}" ]]; then
        echo "unexpected extra argument: $1" >&2
        exit 1
      fi
      SKILL_NAME="$1"
      shift
      ;;
  esac
done

if [[ -z "${SKILL_NAME}" ]]; then
  usage
  exit 1
fi

if ! [[ "${SKILL_NAME}" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]]; then
  echo "skill name must use lowercase letters, numbers, and hyphens only" >&2
  exit 1
fi

if [[ "${OUTPUT_DIR}" = /* || "${OUTPUT_DIR}" =~ (^|/)\.\.(/|$) ]]; then
  echo "output directory must be a relative path under the workspace root" >&2
  exit 1
fi

TARGET_DIR="${WORKSPACE_ROOT}/${OUTPUT_DIR#./}/${SKILL_NAME}"
TITLE="$(echo "${SKILL_NAME}" | tr '-' ' ' | awk '{for(i=1;i<=NF;i++){$i=toupper(substr($i,1,1)) tolower(substr($i,2))}}1')"

render() {
  cat <<EOF
---
name: ${SKILL_NAME}
description: "Describe what this skill solves and when to use it."
---

# ${TITLE}

State the problem and the reusable solution.

## Quick Reference

| Situation | Action |
|-----------|--------|
| Trigger | What to do |

## Workflow

1. First step
2. Second step
3. Verification step

## Gotchas

- Important caveat
- Common failure mode

## Source

- Learning ID: LRN-YYYYMMDD-XXX
- Extraction Date: YYYY-MM-DD
EOF
}

if [[ "${DRY_RUN}" == "true" ]]; then
  echo "Would create:"
  echo "  ${TARGET_DIR}"
  echo "  ${TARGET_DIR}/SKILL.md"
  echo
  render
  exit 0
fi

if [[ -e "${TARGET_DIR}" ]]; then
  echo "target already exists: ${TARGET_DIR}" >&2
  exit 1
fi

mkdir -p "${TARGET_DIR}"
render > "${TARGET_DIR}/SKILL.md"

echo "Created ${TARGET_DIR}/SKILL.md"
