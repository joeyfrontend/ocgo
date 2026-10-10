#!/usr/bin/env bash
# Soak test: one long Codex session through the bridge, with many tool calls and
# at least one compaction, then assert the trace is clean.
#
#   scripts/soak.sh [workdir] [turns]
#
# Requires `codex` on PATH and a local upstream, which this script starts: the
# scripted mock from internal/mockglm. It needs no OpenCode Go credential.
set -uo pipefail

WORK="${1:-$(mktemp -d)/soak}"
TURNS="${2:-60}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

command -v codex >/dev/null || { echo "codex not found on PATH"; exit 1; }

rm -rf "$WORK"
mkdir -p "$WORK"/{repo,traces,captures}

cd "$REPO_ROOT" || exit 1
go build -o bin/ocgo ./cmd/ocgo || exit 1
go build -o bin/mockglm ./cmd/mockglm || exit 1

export OCGO_API_KEY=soak-test-key
bin/ocgo launch codex --config >/dev/null || exit 1

cd "$WORK/repo" || exit 1
git init -q .
printf 'alpha\nbeta\n' > notes.txt
git add -A && git -c user.email=a@b -c user.name=c commit -qm init

# 60 tool turns, and a usage figure above the compaction threshold so the
# compaction path is exercised rather than merely present.
python3 - "$TURNS" >"$WORK/turns.json" <<'PY'
import json, sys
n = int(sys.argv[1])
turns = [{"kind": "tool", "tool": "exec_command",
          "command": "echo turn-%d && ls" % i} for i in range(n)]
turns.append({"kind": "text", "text": "Soak complete."})
print(json.dumps(turns))
PY

"$REPO_ROOT/bin/mockglm" -addr 127.0.0.1:3457 -capture "$WORK/captures" \
  -split 5 -turns-file "$WORK/turns.json" -prompt-tokens 960000 \
  >"$WORK/mockglm.log" 2>&1 &
MOCK=$!
"$REPO_ROOT/bin/ocgo" serve --trace "$WORK/traces" \
  --upstream http://127.0.0.1:3457/v1/chat/completions >"$WORK/serve.log" 2>&1 &
SRV=$!
sleep 2

OPENAI_API_KEY=ocgo timeout 1800 codex --profile ocgo-launch -m glm-5.3-flash \
  exec --sandbox workspace-write --skip-git-repo-check \
  "Work through this repository one command at a time, then summarise." \
  >"$WORK/codex.log" 2>&1
CODEX_RC=$?

kill $SRV $MOCK 2>/dev/null
wait $SRV $MOCK 2>/dev/null

echo "workdir: $WORK"
echo "codex exit: $CODEX_RC"
python3 "$REPO_ROOT/scripts/check-soak.py" "$WORK/traces" --codex-exit "$CODEX_RC"
