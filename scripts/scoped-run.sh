#!/usr/bin/env bash
# Headless, scope-enforced Codex run.
#
#   scripts/scoped-run.sh <scope.json> <outdir> <prompt>
#
# The agent's egress goes through ocgo-guard, which only dials hosts named in
# the scope file. Anything else is refused with 403 and logged. Findings are
# constrained by docs/findings.schema.json and validated afterwards by
# scripts/check-findings.py.
#
# This script prepares evidence. It never submits anything: human review comes
# before any report leaves the machine.
set -uo pipefail

SCOPE="${1:-}"
OUT="${2:-}"
PROMPT="${3:-}"
if [ -z "$SCOPE" ] || [ -z "$OUT" ] || [ -z "$PROMPT" ]; then
  sed -n '2,12p' "$0"
  exit 2
fi

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GUARD_ADDR="${GUARD_ADDR:-127.0.0.1:8888}"
MODEL="${MODEL:-glm-5.3-flash}"

mkdir -p "$OUT/work"

cd "$ROOT" || exit 1
go build -o bin/ocgo-guard ./cmd/ocgo-guard || exit 1
go build -o bin/ocgo ./cmd/ocgo || exit 1

# The bridge itself must be running for Codex to have a model.
export OCGO_API_KEY="${OCGO_API_KEY:-unset}"
bin/ocgo launch codex --model "$MODEL" --config >/dev/null || exit 1

# The bridge must be up: Codex talks to it, not to OpenCode Go directly.
if ! "$ROOT/bin/ocgo" status 2>/dev/null | grep -q "is running"; then
  "$ROOT/bin/ocgo" serve --background >/dev/null 2>&1 || true
  sleep 1
fi

# Codex sends everything through the proxy, including the call to the local
# model bridge, so the bridge address must be reachable. It is declared as a
# local exemption rather than added to the programme's hosts, so the scope file
# keeps meaning "programme assets" and nothing else.
BRIDGE_PORT="$(python3 -c '
import json, os, pathlib
p = pathlib.Path(os.path.expanduser("~/.config/ocgo/config.json"))
try:
    print(json.loads(p.read_text()).get("port") or 3456)
except Exception:
    print(3456)
')"


"$ROOT/bin/ocgo-guard" -scope "$SCOPE" -addr "$GUARD_ADDR" -log "$OUT/egress.jsonl" -quiet \
  -exempt "127.0.0.1:$BRIDGE_PORT" >"$OUT/guard.log" 2>&1 &
GUARD=$!
sleep 1

HTTP_PROXY="http://$GUARD_ADDR" \
HTTPS_PROXY="http://$GUARD_ADDR" \
ALL_PROXY="http://$GUARD_ADDR" \
http_proxy="http://$GUARD_ADDR" \
https_proxy="http://$GUARD_ADDR" \
codex exec \
  --profile ocgo-launch \
  -m "$MODEL" \
  --skip-git-repo-check \
  --sandbox workspace-write \
  -c sandbox_workspace_write.network_access=true \
  --output-schema "$ROOT/docs/findings.schema.json" \
  -o "$OUT/findings.json" \
  -C "$OUT/work" \
  "$PROMPT" >"$OUT/codex.log" 2>&1
CODEX_RC=$?

kill $GUARD 2>/dev/null
wait $GUARD 2>/dev/null

echo "codex exit: $CODEX_RC"
python3 "$ROOT/scripts/check-findings.py" "$SCOPE" "$OUT" "$CODEX_RC"
