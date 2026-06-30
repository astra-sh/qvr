# Behaviour-grader recipes for `eval.py`

A **behaviour grader** runs the skill's output and checks what it *does*, instead
of matching the output text. Prefer it: it's leak-safe (knowing the golden
doesn't help produce it, so the whole `eval/` can ship in the skill) and it's a
truer measure of correctness. Each recipe below is a `--metric` branch you drop
into `eval.py`. All must stay **pure** — no network, no LLM, no clock/RNG in the
grade itself (a perf *companion* number may use a timer; the frozen *score* must
not — see Perf below).

Each branch reads the cohort `runs.jsonl` (one run per line: `{agent, session,
scenario_id, input, output, version}`) + the frozen `scenarios.jsonl`, and emits
annotate-shaped rows:

```json
{"session": "...", "agent": "...", "skill": "...", "metric": "<id>", "score": 0..1, "grader": "<id>"}
```

## Recipe: text2sql — execute and diff result sets (`--metric exact`)

The output is a SQL query; "correct" = it returns the golden rows. Execute it
**read-only on a copy** of the frozen fixture and compare result sets as a sorted
multiset (order-insensitive across rows, so no `ORDER BY` is required to pass).

```python
import os, sqlite3
# Resolve fixtures relative to THIS file, never CWD: the loop runs eval.py from the
# project root (`python3 <skill>/eval/eval.py …`), so "eval/fixture.db" would miss.
EVAL_DIR = os.path.dirname(os.path.abspath(__file__))
def grade_exact(sql, expected, fixture=None):
    fixture = fixture or os.path.join(EVAL_DIR, "fixture.db")
    try:
        con = sqlite3.connect(f"file:{fixture}?mode=ro", uri=True)  # ro: a query can't mutate it
        rows = con.execute(sql).fetchall()
    except Exception:
        return 0.0                                  # bad/destructive/syntactically-wrong SQL fails
    finally:
        try: con.close()
        except Exception: pass
    norm = lambda rs: sorted(tuple(round(c,2) if isinstance(c,float) else str(c) for c in r) for r in rs)
    return 1.0 if norm(rows) == norm(expected) else 0.0
```

The agent's output is rarely a bare query — it may carry a ```sql fence or a
trailing prose line. Extract the SQL before executing (strip fences; take the
first statement) so a correct query inside formatting still grades as correct.

`expected` in `scenarios.jsonl` is the golden **result set** (`[[8]]`, `[["claude",3],…]`),
not a golden query — many queries are correct. Verify every golden against the
fixture *before* freezing; a wrong golden poisons the grader.

## Recipe: perf — deterministic plan cost + latency companion (`--metric perf`)

"Time to get results" is a real quality axis, but raw wall-clock isn't
reproducible, so it can't be the frozen score. Grade perf from the **query plan**
(deterministic) and report median latency as a **companion** number only.

- Run perf against a **large, indexed** fixture (e.g. `fixture-perf.db`, built by
  a seeded script) so the signal is real; the tiny correctness fixture shows none.
- The fixture must carry the **same indexes as the real target DB** (incl. partial
  / expression indexes) so index-friendliness is what's being graded.

```python
import os, sqlite3, time
def grade_perf(sql, fixture=None):
    fixture = fixture or os.path.join(os.path.dirname(os.path.abspath(__file__)), "fixture-perf.db")
    con = sqlite3.connect(f"file:{fixture}?mode=ro", uri=True)
    try:
        plan = con.execute("EXPLAIN QUERY PLAN " + sql).fetchall()   # deterministic
    except Exception:
        return 0.0, None
    detail = " ".join(str(r[-1]) for r in plan)
    scans  = detail.count("SCAN")                  # full table scan = no usable index
    # frozen score: 1.0 when every access uses an index; decays per full scan
    score  = max(0.0, 1.0 - 0.34 * scans)
    # companion (NOT the score): median-of-5 warm latency, for the plot/article only
    ts = []
    for _ in range(5):
        t = time.perf_counter(); con.execute(sql).fetchall(); ts.append((time.perf_counter()-t)*1000)
    con.close()
    return score, round(sorted(ts)[2], 2)          # (frozen score, latency_ms companion)
```

Emit the frozen `score` as the `perf` metric row; stash `latency_ms` in the row's
`details`/a sidecar so the report can show the headline ("~40× faster") without
the decision depending on it. (Proven gap on a 300k-span fixture: index-matching
predicate ≈ 0.97 ms / score 1.0 vs a function-wrapped predicate ≈ 41 ms that
defeats the index.)

## Recipe: schema-validate (`--metric exact`)

Output is JSON that must satisfy a schema and/or deep-equal a golden object:

```python
import json, jsonschema   # schema frozen in eval/, golden in scenarios.jsonl
def grade_schema(out, expected, schema):
    try: obj = json.loads(out)
    except Exception: return 0.0
    try: jsonschema.validate(obj, schema)
    except Exception: return 0.0
    return 1.0 if obj == expected else 0.0
```

## Recipe: build / test exit code (`--metric exact`)

Output is code (or a patch) that must compile or pass a test. Write it into a
sandboxed copy of a fixture project and grade on the exit code:

```python
import subprocess, tempfile, shutil, os
def grade_build(out, fixture_dir, target_rel, cmd):
    work = tempfile.mkdtemp()
    try:
        shutil.copytree(fixture_dir, work, dirs_exist_ok=True)
        open(os.path.join(work, target_rel), "w").write(out)
        r = subprocess.run(cmd, cwd=work, capture_output=True, timeout=120)
        return 1.0 if r.returncode == 0 else 0.0
    except Exception:
        return 0.0
    finally:
        shutil.rmtree(work, ignore_errors=True)
```

(`subprocess` here runs the *graded artifact's* build, not an LLM — still pure and
reproducible given the frozen fixture + command.)

## Leak rule of thumb

If the grade is **pure string-match** on something the agent emits verbatim, a run
that reads `scenarios.jsonl` off disk could echo the answer — hold those
`expected`s in the outer run dir, or switch to a behaviour grade. Every recipe
above is leak-safe: knowing the golden rows / object / exit code doesn't shortcut
producing the SQL / JSON / code, so the scenarios can ship in the skill.
