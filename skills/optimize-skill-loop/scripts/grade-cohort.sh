#!/usr/bin/env bash
# grade-cohort.sh — grade one version's cohort with every deterministic metric the
# harness manifest declares, and write VERSION-TAGGED score rows.
#
# This is the concrete grading step the loop runs after a cohort (it replaces the
# `metrics_from …` shorthand). It does two things the report depends on:
#   1. reads the metric ids from <eval>/HARNESS.md (no hand-listing of metrics), and
#   2. stamps each score row with `version`, so report.py aggregates quality per
#      (agent, version) straight from the `version` field — never through a session
#      id. eval.py grades `output` vs `expected` (pure); no session id is required.
#
# Usage:
#   grade-cohort.sh --skill skillops-sql --version v0.4.0 \
#     --runs run/runs/v0.4.0.jsonl --eval .claude/skills/skillops-sql/eval \
#     --out-dir run/scores
#
# Writes run/scores/<version>.<metric>.jsonl per deterministic metric. Annotating
# the ledger (for compare's SCORE column) is OPTIONAL and a separate step — it
# needs qvr-supplied session ids (`qvr audit sessions`), which the naked runner
# does not carry. report.py needs only these version-stamped score rows.
# -e so a non-zero eval.py (its stderr is silenced) aborts loudly instead of
# writing an empty score file; matches run-cohort.sh.
set -euo pipefail

SKILL="" VERSION="" RUNS="" EVAL="" OUTDIR="run/scores"
while [ $# -gt 0 ]; do
  case "$1" in
    --skill) SKILL="$2"; shift 2;;
    --version) VERSION="$2"; shift 2;;
    --runs) RUNS="$2"; shift 2;;
    --eval) EVAL="$2"; shift 2;;
    --out-dir) OUTDIR="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "$SKILL" ] && [ -n "$VERSION" ] && [ -n "$RUNS" ] && [ -n "$EVAL" ] || {
  echo "need --skill --version --runs --eval" >&2; exit 2; }
mkdir -p "$OUTDIR"

# Deterministic metric ids from the manifest (the real `metrics_from`): every
# entry of metrics[] in HARNESS.md's YAML front-matter.
metrics_from() { # $1=HARNESS.md
  python3 - "$1" <<'PY'
import sys, re
txt = open(sys.argv[1], encoding="utf-8").read()
m = re.match(r"^---\n(.*?)\n---\n", txt, re.S)
body = m.group(1) if m else txt
ids = []
try:
    import yaml
    for d in (yaml.safe_load(body) or {}).get("metrics", []) or []:
        if isinstance(d, dict) and d.get("id"):
            ids.append(d["id"])
except Exception:
    in_metrics = False
    for line in body.splitlines():
        if re.match(r"^metrics:\s*$", line): in_metrics = True; continue
        if in_metrics and re.match(r"^\S", line): in_metrics = False
        if in_metrics:
            mm = re.search(r"\bid:\s*([A-Za-z0-9_]+)", line)
            if mm: ids.append(mm.group(1))
print("\n".join(ids))
PY
}

METRICS=$(metrics_from "$EVAL/HARNESS.md")
[ -n "$METRICS" ] || { echo "no metrics declared in $EVAL/HARNESS.md" >&2; exit 2; }

for M in $METRICS; do
  OUT="$OUTDIR/$VERSION.$M.jsonl"
  # grade to stdout, then stamp `version` onto each row (the loop owns version→runs)
  python3 "$EVAL/eval.py" --runs "$RUNS" --scenarios "$EVAL/scenarios.jsonl" \
      --skill "$SKILL" --metric "$M" 2>/dev/null \
    | python3 -c 'import sys, json
v = sys.argv[1]
for line in sys.stdin:
    line = line.strip()
    if not line: continue
    r = json.loads(line); r["version"] = v; print(json.dumps(r))' "$VERSION" > "$OUT"
  n=$(wc -l < "$OUT" | tr -d ' ')
  echo "  graded $M: $n rows -> $OUT" >&2
done
