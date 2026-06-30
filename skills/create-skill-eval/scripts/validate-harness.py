#!/usr/bin/env python3
"""Validate that a skill's eval/ directory conforms to the eval contract
(references/eval-contract.md) before it is frozen / handed to the optimizer.

The optimizer runs this SAME check and refuses a non-conformant eval/. Run it
yourself first so freezing never produces a harness the loop will reject.

Checks:
  1. required files exist: HARNESS.md, scenarios.jsonl, eval.py, rubric.yaml
  2. HARNESS.md front-matter parses and declares: skill, metrics[] (each with
     id/axis/direction/optimizer_role), exactly one gate metric, rubric, cost,
     agents, n, models, exit
  3. scenarios.jsonl rows parse and carry id + input; >=1 row has `expected`
  4. every declared fixture exists
  5. eval.py answers `--metric <id> --explain` (exit 0) for each declared metric

Usage:
  validate-harness.py --skill skillops-sql --eval .claude/skills/skillops-sql/eval
Exit: 0 conformant, 1 violations (printed), 2 bad invocation.
"""
import argparse, json, os, re, subprocess, sys

REQUIRED = ["HARNESS.md", "scenarios.jsonl", "eval.py", "rubric.yaml"]


def parse_front_matter(path):
    txt = open(path, encoding="utf-8").read()
    m = re.match(r"^---\n(.*?)\n---\n", txt, re.S)
    if not m:
        return None, "HARNESS.md has no `---` YAML front-matter block"
    body = m.group(1)
    try:
        import yaml
        return yaml.safe_load(body), None
    except ModuleNotFoundError:
        # tolerate no-PyYAML: a minimal loader good enough for the manifest keys
        return _mini_yaml(body), None
    except Exception as e:
        return None, f"HARNESS.md front-matter is not valid YAML: {e}"


def _mini_yaml(body):
    """Last-resort parser: top-level `key:` detection only (presence, not deep
    structure). Enough to assert required keys exist when PyYAML is absent."""
    keys = {}
    for line in body.splitlines():
        m = re.match(r"^([a-zA-Z_]+):(.*)$", line)
        if m:
            keys[m.group(1)] = m.group(2).strip() or True
    return keys


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--skill", required=True)
    ap.add_argument("--eval", required=True, help="path to the skill's eval/ dir")
    a = ap.parse_args()
    errs, warns = [], []
    ev = a.eval

    for f in REQUIRED:
        if not os.path.isfile(os.path.join(ev, f)):
            errs.append(f"missing required file: {f}")
    if errs:
        return _report(errs, warns)

    manifest, err = parse_front_matter(os.path.join(ev, "HARNESS.md"))
    metrics = []
    if err:
        errs.append(err)
    elif isinstance(manifest, dict):
        for k in ("skill", "metrics", "rubric", "cost", "agents", "n", "models", "exit"):
            if k not in manifest:
                errs.append(f"HARNESS.md missing key: {k}")
        metrics = manifest.get("metrics") or []
        if not isinstance(metrics, list):
            # PyYAML-absent _mini_yaml records a nested `metrics:` as a bare True
            # (it parses presence, not structure). Treat that as un-introspectable
            # so we fall back to the id-less probe instead of iterating a non-list.
            metrics = []
        if isinstance(metrics, list) and metrics and isinstance(metrics[0], dict):
            gates = [m for m in metrics if m.get("optimizer_role") == "gate"]
            if len(gates) != 1:
                errs.append(f"need exactly one metric with optimizer_role=gate, found {len(gates)}")
            for m in metrics:
                for k in ("id", "axis", "direction", "optimizer_role"):
                    if k not in m:
                        errs.append(f"metric {m.get('id','?')} missing {k}")
        else:
            warns.append("could not introspect metrics[] (install PyYAML for full check); "
                         "falling back to id-less metric probe")
        for fx in (manifest.get("fixtures") or []):
            if isinstance(fx, str) and not os.path.exists(os.path.join(ev, fx)):
                errs.append(f"declared fixture missing: {fx}")

    # scenarios
    sc_path = os.path.join(ev, "scenarios.jsonl")
    n_expected = 0
    for i, line in enumerate(open(sc_path, encoding="utf-8"), 1):
        line = line.strip()
        if not line:
            continue
        try:
            row = json.loads(line)
        except Exception as e:
            errs.append(f"scenarios.jsonl line {i}: bad JSON ({e})"); continue
        if "id" not in row or "input" not in row:
            errs.append(f"scenarios.jsonl line {i}: needs id + input")
        if "expected" in row:
            n_expected += 1
    if n_expected == 0:
        errs.append("scenarios.jsonl has no row with `expected` (no deterministic cases)")

    # eval.py answers each declared metric
    metric_ids = [m.get("id") for m in metrics if isinstance(m, dict) and m.get("id")] or ["exact"]
    for mid in metric_ids:
        try:
            r = subprocess.run(
                [sys.executable, os.path.join(ev, "eval.py"),
                 "--runs", "/dev/null", "--scenarios", sc_path,
                 "--skill", a.skill, "--metric", mid, "--explain"],
                capture_output=True, timeout=60)
            if r.returncode != 0:
                errs.append(f"eval.py --metric {mid} --explain exited {r.returncode}: "
                            f"{r.stderr.decode()[:200]}")
        except Exception as e:
            errs.append(f"eval.py --metric {mid} failed to run: {e}")

    return _report(errs, warns)


def _report(errs, warns):
    for w in warns:
        print(f"warn: {w}")
    if errs:
        print(f"NON-CONFORMANT ({len(errs)}):")
        for e in errs:
            print(f"  - {e}")
        return 1
    print("eval/ is conformant.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
