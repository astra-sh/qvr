#!/usr/bin/env python3
"""Frozen deterministic evaluator for the optimize-skill-loop harness.

Reads a cohort's runs.jsonl + the frozen scenarios.jsonl, scores each run with a
pure, reproducible check (no LLM, no network, no clock), and emits scores.jsonl
in the exact shape `qvr audit annotate --from` consumes:

    {"session": "...", "agent": "...", "skill": "...",
     "metric": "exact", "score": 1.0, "grader": "exact"}

`scenarios.jsonl` is a MIX: rows carrying an `expected` are deterministic (i/o)
cases — regression anchors graded here; rows with NO `expected` are input-only
(open-ended) cases judged by the rubric instead, so this evaluator SKIPS them
(they never count toward the exact pass rate, and emit no score row).

This file ships *inside* the skill at `<skill>/eval/eval.py` and is part of the
FROZEN harness: once written for a skill it never changes for the duration of
the loop, so every cohort is judged identically.

The default check is exact/normalized string match. For a behaviour grader
(run the SQL and diff the rows, build the file and check the exit code, validate
against a schema) rewrite `norm()`/the compare below — that is the only edit this
template expects, and it also closes the only way an in-skill answer key could be
gamed (see SKILL.md "Why the scenarios can live in the skill").

Usage:
  eval.py --runs runs/v0.3.0.jsonl --scenarios eval/scenarios.jsonl \
          --skill slugify-title --metric exact --out scores/v0.3.0.exact.jsonl
  eval.py --runs ... --scenarios ... --explain      # human-readable per-scenario table
"""
import argparse, json, sys, unicodedata


def norm(s: str) -> str:
    """Whitespace/quote-insensitive comparison key. The grade is exact match on
    this normalized form, NOT a fuzzy score — determinism over leniency."""
    s = (s or "").strip().strip('"').strip("'").strip()
    s = unicodedata.normalize("NFC", s)
    return s


def load_jsonl(path):
    out = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                out.append(json.loads(line))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", required=True)
    ap.add_argument("--scenarios", required=True)
    ap.add_argument("--skill", default="")
    ap.add_argument("--metric", default="exact")
    ap.add_argument("--grader", default="exact")
    ap.add_argument("--out", default="")
    ap.add_argument("--explain", action="store_true")
    a = ap.parse_args()

    # Only i/o rows (those with `expected`) are deterministically gradable;
    # input-only rows are rubric-judged and deliberately absent from this map.
    expected = {s["id"]: s["expected"] for s in load_jsonl(a.scenarios) if "expected" in s}
    runs = load_jsonl(a.runs)

    rows, table = [], []
    passed = graded = 0
    for r in runs:
        sc = r["scenario_id"]
        if sc not in expected:
            continue                       # input-only scenario → rubric grades it, not us
        graded += 1
        want = norm(expected[sc])
        got = norm(r.get("output", ""))
        ok = (got == want)
        passed += ok
        rows.append({
            "session": r["session"], "agent": r.get("agent", "claude"),
            "skill": a.skill, "metric": a.metric,
            "score": 1.0 if ok else 0.0, "grader": a.grader,
        })
        table.append((sc, r.get("agent", "?"), "PASS" if ok else "FAIL", want, got))

    if a.explain:
        w = max([len(t[3]) for t in table] + [8])
        print(f"{'SCENARIO':<14}{'AGENT':<8}{'V':<6}{'EXPECTED':<{w+2}}GOT")
        for sc, ag, v, want, got in table:
            print(f"{sc:<14}{ag:<8}{v:<6}{want:<{w+2}}{got}")
        print(f"\n{passed}/{graded} pass  (rate={passed/max(graded,1):.3f}; "
              f"{len(runs)-graded} input-only runs skipped)")
        return

    if a.out:
        with open(a.out, "w") as f:
            for row in rows:
                f.write(json.dumps(row) + "\n")
        print(f"wrote {len(rows)} scores to {a.out} "
              f"({passed}/{graded} pass, rate={passed/max(graded,1):.3f}; "
              f"{len(runs)-graded} input-only runs skipped)",
              file=sys.stderr)
    else:
        for row in rows:
            print(json.dumps(row))


if __name__ == "__main__":
    main()
