#!/usr/bin/env bash
# run-cohort.sh — the thin, deterministic, NAKED inner-loop runner.
#
# For each (agent x scenario x N) it launches a FRESH, no-leak headless session,
# makes that session run the inner skill on one scenario's `input`, and records
# the output. That is ALL it does. It does NOT mint run-keys, does NOT set session
# ids, and does NOT correlate sessions — qvr owns every bit of session tracking.
#
# After this runs, `qvr audit discover --agent <agents>` ingests the native
# sessions (claude + codex + …) and the ledger supplies, per run, the session id,
# the agent, the cost (tokens/turns/tools), and the version cohort — keyed by the
# stable `subtree_hash`. The old run-key/correlate dance is gone: qvr 0.30.x
# derives clean spans for every agent, so a naked run is fully attributed.
#
# The ONE thing qvr cannot infer and the runner therefore OWNS:
#   the VERSION tag this cohort ran (the loop set it via `qvr switch` first). It is
#   the candidate's identity — the key quality joins on — NOT session bookkeeping.
# The runner owns NO session id, NO timing/window, NO correlation: qvr stamps each
# session with the skill content version it ran (`subtree_hash`) at `audit discover`,
# so the grader attributes a cohort's ledger sessions by skill IDENTITY — there is
# no wall-clock window to record and nothing injected into the run.
#
# Usage:
#   run-cohort.sh --skill slugify-title --version slugify-title/v0.5.0 \
#     --scenarios .claude/skills/slugify-title/eval/scenarios.jsonl \
#     --agents claude,codex --n 1 --out run/runs/v0.5.0.jsonl
#
# scenarios.jsonl (frozen, ships in the skill at <skill>/eval/): one object per line,
#   {"id": "...", "input": "...", "expected": "..."}  — `expected` is OPTIONAL and
# is for the grader only; only id+input are ever sent to the agent.
set -euo pipefail

SKILL="" VERSION="" SCENARIOS="" AGENTS="claude" N=1 OUT="" MODEL="${QVR_LOOP_MODEL:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --skill) SKILL="$2"; shift 2;;
    --version) VERSION="$2"; shift 2;;
    --scenarios) SCENARIOS="$2"; shift 2;;
    --agents) AGENTS="$2"; shift 2;;
    --n) N="$2"; shift 2;;
    --out) OUT="$2"; shift 2;;
    --model) MODEL="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "$SKILL" ] && [ -n "$SCENARIOS" ] && [ -n "$OUT" ] || { echo "need --skill --scenarios --out" >&2; exit 2; }
[ -n "$VERSION" ] || { echo "need --version (the tag you switched to — it is the cohort key)" >&2; exit 2; }
mkdir -p "$(dirname "$OUT")"; : > "$OUT"

# realpath of the project dir, so it matches qvr's stored working_directory
# (e.g. macOS reports /private/tmp/… for a /tmp/… cwd).
PROJECT=$(python3 -c 'import os;print(os.path.realpath(os.getcwd()))')

# The task prompt template. FROZEN — keep it byte-identical every round (it is one
# of the five frozen invariants). It NAMES the skill so the agent loads it (qvr
# then attributes the run to the skill's content version) and pins the output shape
# so the deterministic grader can parse it. No run-key tag, no correlation cruft.
prompt_for() { # $1=input
  cat <<EOF
Use the "$SKILL" skill to perform its task on this input. Read the skill's
SKILL.md, follow it exactly, and output ONLY the final result on a single line
with no commentary, no code fences, no quotes.

INPUT:
$1
EOF
}

run_claude() { # $1=prompt  -> prints result text (empty on any failure)
  claude -p "$1" --output-format json \
    ${MODEL:+--model "$MODEL"} --permission-mode acceptEdits 2>/dev/null \
    | python3 -c 'import sys,json
try: print(json.load(sys.stdin).get("result","") or "")
except Exception: print("")'
}
run_codex()  { codex exec --json "$1" 2>/dev/null \
    | python3 -c 'import sys,json
# codex streams one JSON object per line; the answer is the LAST item.completed
# whose item.type == "agent_message".
ans=""
for line in sys.stdin:
  line=line.strip()
  if not line: continue
  try: d=json.loads(line)
  except Exception: continue
  it=d.get("item") or {}
  if d.get("type")=="item.completed" and it.get("type")=="agent_message":
    ans=it.get("text","")
print(ans)'; }
run_cursor() { cursor-agent -p "$1" 2>/dev/null; }
run_gemini() { gemini -p "$1" 2>/dev/null; }

emit() { # $1 agent $2 scenario_id $3 input $4 output
  python3 - "$1" "$2" "$3" "$4" "$VERSION" "$PROJECT" "$MODEL" <<'PY' >> "$OUT"
import sys, json
a,sc,inp,out,ver,proj,model = sys.argv[1:8]
# The runner tracks NOTHING about the session: qvr owns session ids, timing, cost,
# and version attribution. No started_ms/ended_ms — there is no wall-clock window to
# record because the grader attributes ledger sessions by skill IDENTITY (subtree_hash),
# not by time. `session` is emitted EMPTY only so frozen graders that thread a
# `session` field into their score rows (e.g. create-skill-eval's eval.py) don't
# KeyError; nothing keys on it (quality joins on `version`, cost on skill identity).
print(json.dumps({"agent":a,"scenario_id":sc,"input":inp,"output":out.strip(),
                  "version":ver,"project":proj,"model":model or None,
                  "session":""}))
PY
}

IFS=',' read -ra AGENT_LIST <<< "$AGENTS"
# Read scenarios on fd 3 so an agent subprocess that reads stdin (codex exec does)
# can't drain the scenario list mid-loop. Agent calls also get </dev/null.
while IFS= read -r line <&3; do
  [ -z "$line" ] && continue
  sid_base=$(printf '%s' "$line" | python3 -c 'import sys,json;print(json.loads(sys.stdin.read())["id"])')
  input=$(printf '%s' "$line" | python3 -c 'import sys,json;print(json.loads(sys.stdin.read())["input"])')
  for agent in "${AGENT_LIST[@]}"; do
    for i in $(seq 1 "$N"); do
      P=$(prompt_for "$input")
      out=""
      # A single agent failure must NOT abort the cohort: record empty output
      # (it simply grades 0) and carry on.
      case "$agent" in
        claude) out=$(run_claude "$P" </dev/null) || out="";;
        codex)  out=$(run_codex  "$P" </dev/null) || out="";;
        cursor) out=$(run_cursor "$P" </dev/null) || out="";;
        gemini) out=$(run_gemini "$P" </dev/null) || out="";;
        *) echo "unsupported agent: $agent" >&2; exit 2;;
      esac
      emit "$agent" "$sid_base" "$input" "$out"
      if [ -z "$out" ]; then
        echo "  [$agent] $sid_base #$i -> (EMPTY — recorded, will grade 0)" >&2
      else
        echo "  [$agent] $sid_base #$i -> ${out:0:60}" >&2
      fi
    done
  done
done 3< "$SCENARIOS"
echo "wrote $(wc -l < "$OUT") runs to $OUT" >&2
echo "next: qvr audit discover --agent $AGENTS   # qvr ingests these sessions; cost/ids come from the ledger" >&2
