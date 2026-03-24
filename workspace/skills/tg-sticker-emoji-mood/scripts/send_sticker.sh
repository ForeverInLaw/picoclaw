#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  send_sticker.sh --sticker <FILE_ID>
  send_sticker.sh --sticker-set <SET_NAME> --emoji <EMOJI>
  send_sticker.sh --list-set <SET_NAME>

Chat targeting:
  Uses TELEGRAM_CHAT_ID or PICOCLAW_CHAT_ID automatically.
  You can override with --chat-id <ID>.

Auth:
  Uses TELEGRAM_BOT_TOKEN if set.
  Otherwise tries PICOCLAW_CONFIG or the default runtime config.json.
EOF
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_CONFIG="${PICOCLAW_CONFIG:-${SCRIPT_DIR}/../../../../config.json}"

CHAT_ID="${TELEGRAM_CHAT_ID:-${PICOCLAW_CHAT_ID:-}}"
FILE_ID=""
SET_NAME=""
EMOJI=""
LIST_SET=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --chat-id) CHAT_ID="${2:-}"; shift 2 ;;
    --sticker) FILE_ID="${2:-}"; shift 2 ;;
    --sticker-set) SET_NAME="${2:-}"; shift 2 ;;
    --emoji) EMOJI="${2:-}"; shift 2 ;;
    --list-set) LIST_SET="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

load_token_from_config() {
  local cfg="$1"
  [[ -f "$cfg" ]] || return 1
  python3 - "$cfg" <<'PY'
import json, sys
path = sys.argv[1]
try:
    with open(path, 'r', encoding='utf-8') as f:
        data = json.load(f)
    token = (((data.get("channels") or {}).get("telegram") or {}).get("token") or "").strip()
    if token:
        print(token)
except Exception:
    pass
PY
}

BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-}"
if [[ -z "$BOT_TOKEN" ]]; then
  BOT_TOKEN="$(load_token_from_config "$DEFAULT_CONFIG")"
fi

if [[ -z "$BOT_TOKEN" ]]; then
  echo "Error: TELEGRAM_BOT_TOKEN is not set and no token found in config" >&2
  exit 1
fi

API="https://api.telegram.org/bot${BOT_TOKEN}"

require_chat_id() {
  if [[ -z "$CHAT_ID" ]]; then
    echo "Error: chat id is missing. TELEGRAM_CHAT_ID/PICOCLAW_CHAT_ID not provided." >&2
    exit 1
  fi
}

get_sticker_set() {
  local set_name="$1"
  curl -fsS -X POST "${API}/getStickerSet" \
    -d name="${set_name}"
}

send_sticker_by_id() {
  local chat_id="$1" file_id="$2"
  curl -fsS -X POST "${API}/sendSticker" \
    -d chat_id="${chat_id}" \
    -d sticker="${file_id}"
}

list_set() {
  local set_name="$1"
  local response
  response="$(get_sticker_set "${set_name}")"
  echo "${response}" | python3 - <<'PY'
import json, sys
data = json.load(sys.stdin)
if not data.get("ok"):
    print("Error:", data.get("description", "unknown"), file=sys.stderr)
    sys.exit(1)
for s in data["result"]["stickers"]:
    print(f"{s.get('emoji', '')}\t{s['file_id']}")
PY
}

send_sticker_by_emoji() {
  local chat_id="$1" set_name="$2" emoji="$3"
  local response file_id
  response="$(get_sticker_set "${set_name}")"
  file_id="$(
    python3 - "$emoji" <<'PY' <<<"$response"
import json, random, sys
emoji = sys.argv[1]
data = json.load(sys.stdin)
if not data.get("ok"):
    print("Error:" + str(data.get("description", "unknown")), file=sys.stderr)
    sys.exit(1)
stickers = data["result"]["stickers"]
matches = [s for s in stickers if s.get("emoji") == emoji]
pool = matches or stickers
print(random.choice(pool)["file_id"])
PY
  )"
  send_sticker_by_id "${chat_id}" "${file_id}"
}

if [[ -n "$LIST_SET" ]]; then
  list_set "$LIST_SET"
  exit 0
fi

require_chat_id

if [[ -n "$FILE_ID" ]]; then
  send_sticker_by_id "$CHAT_ID" "$FILE_ID"
  exit 0
fi

if [[ -n "$SET_NAME" && -n "$EMOJI" ]]; then
  send_sticker_by_emoji "$CHAT_ID" "$SET_NAME" "$EMOJI"
  exit 0
fi

usage >&2
exit 1
