#!/usr/bin/env bash
# Capture one real Codex CLI session through the proxy, with tracing on.
#
#   scripts/capture.sh [workdir] [prompt] [extra mockglm args]
#
# The upstream is the scripted local stand-in from internal/mockglm, so the
# capture needs no OpenCode Go credential and spends none of the allowance. The
# point is to record what Codex actually sends and what the bridge sends back.
#
# TURNS=<n> scripts n exec_command turns instead of the default four-turn
# script; combine with "-prompt-tokens 960000" to provoke compaction.
set -uo pipefail

WORK="${1:-${TMPDIR:-/tmp}/ocgo-capture}"
PROMPT="${2:-Read notes.txt and calc.py, then fix the bug in calc.py.}"
MOCK_ARGS="${3:-}"
TURNS="${TURNS:-0}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

command -v codex >/dev/null || { echo "codex not found on PATH"; exit 1; }

rm -rf "$WORK"
mkdir -p "$WORK"/{repo,traces,captures}

cd "$ROOT" || exit 1
go build -o bin/ocgo ./cmd/ocgo || exit 1
go build -o bin/mockglm ./cmd/mockglm || exit 1

export OCGO_API_KEY=capture-test-key

# The scripted upstream reports fixed usage figures, so recording them would
# put imaginary dollars into the real spend ledger. A capture gets its own.
export OCGO_USAGE_FILE="$WORK/usage.json"
bin/ocgo launch codex --model glm-5.3-flash --config || exit 1

cd "$WORK/repo" || exit 1
git init -q .
printf 'alpha\nbeta\ngamma\n' > notes.txt
printf 'def add(a, b):\n    return a - b\n' > calc.py
git add -A && git -c user.email=a@b -c user.name=c commit -qm init

TURNS_FLAG=""
if [ "$TURNS" -gt 0 ]; then
  python3 - "$TURNS" >"$WORK/turns.json" <<'PY'
import json, sys
n = int(sys.argv[1])
turns = [{"kind": "tool", "tool": "exec_command",
          "command": "echo turn-%d && ls -la" % i} for i in range(n)]
turns.append({"kind": "text", "text": "Done."})
print(json.dumps(turns))
PY
  TURNS_FLAG="-turns-file $WORK/turns.json"
fi

# shellcheck disable=SC2086
"$ROOT/bin/mockglm" -addr 127.0.0.1:3457 -capture "$WORK/captures" -split 5 \
  $TURNS_FLAG $MOCK_ARGS >"$WORK/mockglm.log" 2>&1 &
MOCK=$!
"$ROOT/bin/ocgo" serve --trace "$WORK/traces" \
  --upstream http://127.0.0.1:3457/v1/chat/completions >"$WORK/serve.log" 2>&1 &
SRV=$!
sleep 2

echo "health proxy: $(curl -s http://127.0.0.1:3456/health | tr -d '\n')"
echo "health mock:  $(curl -s http://127.0.0.1:3457/health | tr -d '\n')"

OPENAI_API_KEY=ocgo timeout 600 codex --profile ocgo-launch -m glm-5.3-flash \
  exec --sandbox workspace-write --skip-git-repo-check "$PROMPT" >"$WORK/codex.log" 2>&1
CODEX_RC=$?

kill $SRV $MOCK 2>/dev/null
wait $SRV $MOCK 2>/dev/null

echo "=== codex exit: $CODEX_RC ==="
echo "=== codex.log (tail) ==="
tail -20 "$WORK/codex.log"
echo "=== trace files ==="
ls -la "$WORK/traces"
echo "=== captured upstream requests ==="
ls -la "$WORK/captures"
