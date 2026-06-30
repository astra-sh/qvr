#!/usr/bin/env python3
"""Deterministic end-of-loop report: per-agent frontier + the chosen best skill.

Everything here is a pure function of the recorded cohorts — no LLM calls, no
clock, no randomness — so the same inputs always yield the same winner and the
same plot. The only non-deterministic dimension (the rubric/LLM-judge score) is
read from already-recorded numbers, never recomputed.

Inputs
  --runs      glob of runs/*.jsonl produced by run-cohort.sh. Each row is one naked
              run: {agent, scenario_id, input, output, version, project} — NO session
              id and NO timing (qvr owns sessions, cost, and the per-run clock).
  --scenarios the frozen scenario set (`<skill>/eval/scenarios.jsonl`). Rows with
              an `expected` feed the deterministic pass rate; input-only rows are
              rubric-judged (via --scores) and skip the exact axis.
  --skill     inner skill name (labels + filters ledger sessions to this skill)
  --scores    (optional) glob of scores/*.jsonl — version-stamped grader rows
  --sessions  (optional) `qvr audit sessions --output json` dump — for cost axis
  --out-json  decision file (machine-readable: per-agent frontier + winner)
  --out-svg   the plot across agents with the winner starred

Metric scores come from the frozen grader (eval.py) via --scores, version-stamped:
`exact` (the gate quality metric) and any extra frontier metric like `perf`, plus
`rubric`. For a string-match skill that ships no exact scores, the quality axis
falls back to re-grading `norm(output)==norm(expected)` from the run rows.

COST comes entirely from the ledger, NOT from session ids in the runs. qvr 0.30.x
stamps each session with the skill CONTENT version that was active during it —
`skill_versions: [{skill, version, commit, subtree_hash}]`, switch-invariant and
provable (it no longer live-resolves against the current checkout). So a session's
tokens attach to a cohort by skill IDENTITY (its `subtree_hash`), not by a wall-clock
window. The runner owns only the version tag; qvr owns the sessions, their tokens,
and the content version each ran. No window, no run-key, no correlation.

Decision rule (deterministic, documented so it's reproducible)
  quality(agent,version) = mean `exact` score over that cohort's i/o runs.
  perf(agent,version)    = mean `perf` score (higher better); None if no perf metric.
  cost(agent,version)    = mean ledger tokens_out over the sessions whose captured
                           skill content version (subtree_hash) identifies this
                           cohort (lower better); None if no --sessions / no match.
  A version is ON an agent's frontier if no other version for that agent beats-or-
    ties it on ALL of {quality ↑, cost ↓, perf ↑} with at least one strict.
  The CHOSEN winner per agent = the frontier point with, in strict order:
    1. highest quality, 2. lowest cost, 3. highest perf, 4. highest rubric,
    5. highest version string.
  The OVERALL best skill = the version chosen for the most agents; ties broken by
    1. highest mean quality across agents, 2. lowest mean cost, 3. highest version.
"""
import argparse, glob, json, re, statistics, sys, unicodedata
from collections import defaultdict


def norm(s):
    s = (s or "").strip().strip('"').strip("'").strip()
    return unicodedata.normalize("NFC", s)


def load_glob(pat):
    rows = []
    for p in sorted(glob.glob(pat)):
        rows += [json.loads(l) for l in open(p) if l.strip()]
    return rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", required=True)
    ap.add_argument("--scenarios", required=True)
    ap.add_argument("--skill", default="skill")
    ap.add_argument("--scores", default="")
    ap.add_argument("--sessions", default="")
    ap.add_argument("--out-json", default="report.json")
    ap.add_argument("--out-svg", default="report.svg")
    # deterministic exit predicate
    ap.add_argument("--target-quality", type=float, default=1.0,
                    help="per-agent quality at/above which an agent is converged")
    ap.add_argument("--round", type=int, default=1, help="current round number")
    ap.add_argument("--max-rounds", type=int, default=0, help="hard cap (0 = unbounded)")
    ap.add_argument("--history", default="",
                    help="jsonl of prior rounds' overall_best (for no-advance/patience)")
    ap.add_argument("--patience", type=int, default=2,
                    help="stop if overall_best is unchanged this many consecutive rounds")
    a = ap.parse_args()

    # `expected` here is only the string-match FALLBACK map (for skills that ship no
    # exact scores). Behaviour-graded skills carry non-string `expected` (e.g. a
    # result set) and get quality from the grader's exact scores via --scores, so we
    # keep only string expecteds here.
    expected = {s["id"]: norm(s["expected"]) for s in load_glob(a.scenarios)
                if isinstance(s.get("expected"), str)}
    runs = load_glob(a.runs)

    # COST: qvr now records, per session, the skill CONTENT version active during it
    # (`skill_versions: [{skill, version, commit, subtree_hash}]`) — switch-invariant
    # and provable, so we attribute a session's tokens to a cohort by skill IDENTITY
    # (its `subtree_hash`) instead of bucketing by a wall-clock window. No window, no
    # guard, no "one cohort per window" constraint. We still scope to the loop's
    # project dir(s) so an unrelated run of the same version elsewhere can't pollute
    # the cost. `subtree_hash` is the join key; the runner's `version` tag is the
    # human label we fold it back onto.
    # cohort_cost: (agent, version) -> [tokens_out per attributed session]
    cohort_cost = defaultdict(list)
    run_cohorts = {(r["agent"], r["version"]) for r in runs}
    run_versions = {r["version"] for r in runs}
    projects = {r["project"] for r in runs if r.get("project")}

    def vtail(v):                       # trailing tag segment: "reg/skill/v0.5.0" -> "v0.5.0"
        return (v or "").rsplit("/", 1)[-1]

    def is_commit(lbl):                 # a bare commit sha, not a human ref/tag
        return bool(re.fullmatch(r"[0-9a-f]{7,40}", lbl or ""))

    def to_runner_tag(label):
        # fold a session's (possibly short or commit-only) skill version label onto
        # the runner's full --version tag, matching on the trailing tag segment. Falls
        # back to the raw label for a cohort the runs don't cover (cost-only).
        for t in run_versions:
            if t == label or vtail(t) == vtail(label):
                return t
        return label

    if a.sessions:
        sj = json.load(open(a.sessions))
        sessions = sj if isinstance(sj, list) else sj.get("sessions", [])

        def skill_ver(s):               # this skill's {version,commit,subtree_hash} entry
            for v in (s.get("skill_versions") or []):
                if v.get("skill") == a.skill:
                    return v
            return None

        # Canonicalize each subtree_hash to ONE label across sessions: the content
        # hash is the stable identity, but its label can still come back as a full ref
        # on one session and commit-only on another (the residual ref↔commit skew).
        # Prefer a ref/tag label over a bare commit sha so both fold into the cohort
        # the runner switch-tagged.
        label_of_hash = {}
        for s in sessions:
            sv = skill_ver(s)
            if not sv or not sv.get("subtree_hash"):
                continue
            h, lbl = sv["subtree_hash"], (sv.get("version") or sv.get("commit"))
            if not lbl:
                continue
            cur = label_of_hash.get(h)
            if cur is None or (not is_commit(lbl) and is_commit(cur)):
                label_of_hash[h] = lbl

        for s in sessions:
            sv = skill_ver(s)
            if not sv:
                continue
            if projects and s.get("working_directory") not in projects:
                continue
            label = label_of_hash.get(sv.get("subtree_hash")) \
                or sv.get("version") or sv.get("commit")
            if not label:
                continue
            t = s.get("tokens_out")
            if t is not None:
                cohort_cost[(s.get("agent_name"), to_runner_tag(label))].append(t)
    score_rows = load_glob(a.scores) if a.scores else []
    agg = defaultdict(lambda: {"exact": [], "cost": [], "rubric": [], "perf": []})

    # PREFERRED: score rows that carry their own `version` are self-describing, so we
    # aggregate quality/perf/rubric straight from them — one score per graded run —
    # without re-joining through the session id (which some agents collapse across
    # runs). Cost still comes from the ledger via the run rows' sessions.
    versioned = [r for r in score_rows if r.get("version")]
    if versioned:
        for r in versioned:
            k = (r["agent"], r["version"])
            m = r.get("metric")
            if m in ("exact", "perf", "rubric"):
                agg[k][m].append(r["score"])
    else:
        # LEGACY: session-keyed scores (+ string-match fallback for string skills).
        metric_scores = defaultdict(dict)
        for r in score_rows:
            m, sess = r.get("metric"), r.get("session")
            if m and sess:
                metric_scores[m][sess] = r["score"]
        exact_scores = metric_scores.get("exact", {})
        perf_scores = metric_scores.get("perf", {})
        rubric = metric_scores.get("rubric", {})
        for r in runs:
            k = (r["agent"], r["version"])
            sc = r["scenario_id"]
            sid = r.get("session")
            if sid and sid in exact_scores:
                agg[k]["exact"].append(exact_scores[sid])
            elif sc in expected:
                agg[k]["exact"].append(1.0 if norm(r.get("output", "")) == expected[sc] else 0.0)
            if sid and sid in perf_scores:
                agg[k]["perf"].append(perf_scores[sid])
            if sid and sid in rubric:
                agg[k]["rubric"].append(rubric[sid])

    # make sure every (agent, version) that produced runs is represented, even if it
    # has only a cost cohort (no scores) — so the frontier never silently drops one.
    for k in list(run_cohorts) + list(cohort_cost.keys()):
        agg[k]  # touch (defaultdict) to materialize the key

    cohorts = []
    for (agent, version), d in agg.items():
        cst = cohort_cost.get((agent, version), [])
        cohorts.append({
            "agent": agent, "version": version, "n": len(d["exact"]),
            "quality": round(statistics.mean(d["exact"]), 4) if d["exact"] else None,
            "cost": round(statistics.mean(cst), 1) if cst else None,
            "perf": round(statistics.mean(d["perf"]), 4) if d["perf"] else None,
            "rubric": round(statistics.mean(d["rubric"]), 4) if d["rubric"] else None,
        })

    # per-agent frontier + chosen winner
    by_agent = defaultdict(list)
    for c in cohorts:
        by_agent[c["agent"]].append(c)

    # 3-axis dominance: quality ↑, cost ↓, perf ↑ (perf optional — None ⇒ neutral 0)
    def dominated(c, others):
        # An absent quality (None — a cost-only cohort with no graded runs) is a
        # neutral floor of 0, matching cost/perf, so dominance never crashes on
        # a None comparison.
        cq = c["quality"] if c["quality"] is not None else 0
        cc = c["cost"] if c["cost"] is not None else 0
        cp = c["perf"] if c["perf"] is not None else 0
        for o in others:
            if o is c:
                continue
            oq = o["quality"] if o["quality"] is not None else 0
            oc = o["cost"] if o["cost"] is not None else 0
            op = o["perf"] if o["perf"] is not None else 0
            ge = (oq >= cq and oc <= cc and op >= cp)
            gt = (oq > cq or oc < cc or op > cp)
            if ge and gt:
                return True
        return False

    winners = {}
    for agent, cs in by_agent.items():
        for c in cs:
            c["frontier"] = not dominated(c, cs)
        front = [c for c in cs if c["frontier"]]
        front.sort(key=lambda c: (-(c["quality"] or 0),
                                  c["cost"] if c["cost"] is not None else 0,
                                  -(c["perf"] or 0), -(c["rubric"] or 0), ))
        # tie on quality+cost+perf: prefer highest version string, deterministically
        best_q = front[0]["quality"] or 0
        best_c = front[0]["cost"] if front[0]["cost"] is not None else 0
        best_p = front[0]["perf"] if front[0]["perf"] is not None else 0
        cands = [c for c in front
                 if (c["quality"] or 0) == best_q
                 and (c["cost"] if c["cost"] is not None else 0) == best_c
                 and (c["perf"] if c["perf"] is not None else 0) == best_p]
        cands.sort(key=lambda c: c["version"])
        win = cands[-1]
        win["chosen"] = True
        winners[agent] = win["version"]

    # overall best skill
    tally = defaultdict(int)
    for v in winners.values():
        tally[v] += 1
    def overall_key(v):
        qs = [c["quality"] for c in cohorts if c["version"] == v and c["quality"] is not None]
        cz = [c["cost"] for c in cohorts if c["version"] == v and c["cost"] is not None]
        return (tally[v], statistics.mean(qs) if qs else 0,
                -(statistics.mean(cz) if cz else 0), v)
    overall = max(tally, key=overall_key) if tally else None

    # ---- deterministic exit predicate ----
    # per-agent chosen quality → converged vs open (with headroom to the target)
    chosen_q = {}
    for agent, cs in by_agent.items():
        win = next(c for c in cs if c.get("chosen"))
        chosen_q[agent] = win["quality"]
    # A winner with no measured quality (None) is never converged: it drops to
    # open_agents with full headroom so the loop keeps grading it.
    converged = sorted(ag for ag, q in chosen_q.items()
                       if q is not None and q >= a.target_quality)
    open_agents = sorted(
        ({"agent": ag, "quality": q,
          "headroom": round(a.target_quality - (q or 0), 4)}
         for ag, q in chosen_q.items() if q is None or q < a.target_quality),
        key=lambda x: (-x["headroom"], x["agent"]))      # worst-first, deterministic
    next_target = open_agents[0]["agent"] if open_agents else None

    # no-advance / patience over the recorded history of overall_best
    stagnant = 0
    if a.history:
        try:
            hist = [json.loads(l)["overall_best"] for l in open(a.history) if l.strip()]
        except OSError:
            hist = []
        seq = hist + [overall]
        for v in reversed(seq):
            if v == overall:
                stagnant += 1
            else:
                break

    reasons = []
    if not open_agents:
        reasons.append(f"all agents reached target quality {a.target_quality}")
    if a.max_rounds and a.round >= a.max_rounds:
        reasons.append(f"max rounds reached ({a.round}/{a.max_rounds})")
    if a.history and stagnant >= a.patience:
        reasons.append(f"overall_best '{overall}' unchanged for {stagnant} rounds "
                       f"(patience {a.patience})")
    stop = bool(reasons)

    exit_block = {
        "stop": stop,
        "reasons": reasons,
        "recommend": "stop" if stop else f"continue — target agent: {next_target}",
        "round": a.round, "max_rounds": a.max_rounds or None,
        "target_quality": a.target_quality,
        "converged_agents": converged,
        "open_agents": open_agents,           # each: {agent, quality, headroom}
        "next_target": next_target,
    }

    # cost-attribution health: warn (don't silently drop) when a cohort matched no
    # ledger session by skill identity, so a None cost axis is visible, not mistaken
    # for free. Most likely cause: `qvr audit discover` was not run after the cohort,
    # the session ran in a different project dir, or its skill version came back
    # commit-only with no ref to fold onto the runner tag (the residual ref↔commit skew).
    warnings = []
    if a.sessions:
        for (agent, version) in sorted(run_cohorts):
            if not cohort_cost.get((agent, version)):
                warnings.append(f"cost unattributed for {agent}@{version}: no ledger "
                                f"session carried this skill's content version "
                                f"(subtree_hash) — did you run `qvr audit discover` "
                                f"after this cohort?")
        for w in warnings:
            print(f"warning: {w}", file=sys.stderr)

    decision = {"skill": a.skill, "winner_per_agent": winners,
                "overall_best": overall, "exit": exit_block,
                "warnings": warnings,
                "cohorts": sorted(
                    cohorts, key=lambda c: (c["agent"], c["version"]))}
    json.dump(decision, open(a.out_json, "w"), indent=2)
    render_svg(a.out_svg, a.skill, decision)
    print(json.dumps(decision, indent=2))


def render_svg(path, skill, decision):
    cohorts = decision["cohorts"]
    agents = sorted({c["agent"] for c in cohorts})
    versions = sorted({c["version"] for c in cohorts})
    if not agents or not versions:
        # nothing graded — emit a placeholder rather than crashing, so a bad
        # --runs glob (no matching run files) fails loud but clean.
        open(path, "w").write(
            f'<svg xmlns="http://www.w3.org/2000/svg" width="520" height="80">'
            f'<text x="16" y="44" font-family="sans-serif" font-size="14" '
            f'fill="#b00">{skill}: no cohorts — check --runs glob matched run files</text></svg>')
        return
    palette = ["#b0b0b5", "#0a84ff", "#34c759", "#ff9f0a", "#ff375f", "#5e5ce6"]
    vcolor = {v: palette[i % len(palette)] for i, v in enumerate(versions)}
    W, H = 200 + len(agents) * 200, 420
    ox, oy, ph = 70, 90, 250
    base = oy + ph
    out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" '
           f'font-family="-apple-system,Segoe UI,Helvetica,Arial,sans-serif">']
    out.append(f'<rect width="{W}" height="{H}" fill="#fbfbfd"/>')
    out.append(f'<text x="24" y="34" font-size="18" font-weight="700" fill="#1d1d1f">'
               f'{skill}: quality across agents — winner ★</text>')
    has_perf = any(c.get("perf") is not None for c in cohorts)
    sub = f'overall best: {decision["overall_best"]} · bars = quality (exact pass rate)'
    if has_perf:
        sub += ' · p=perf score below bar'
    out.append(f'<text x="24" y="55" font-size="12" fill="#6e6e73">{sub}</text>')
    for q in (0, .25, .5, .75, 1.0):
        y = base - q * ph * .85
        out.append(f'<line x1="{ox}" y1="{y:.0f}" x2="{W-30}" y2="{y:.0f}" stroke="#ececed"/>')
        out.append(f'<text x="{ox-8}" y="{y+4:.0f}" font-size="10" fill="#86868b" text-anchor="end">{q:.2f}</text>')
    gw = (W - ox - 60) / len(agents)
    bw = min(46, gw / (len(versions) + 1))
    for ai, agent in enumerate(agents):
        gx = ox + ai * gw + 20
        for vi, v in enumerate(versions):
            c = next((x for x in cohorts if x["agent"] == agent and x["version"] == v), None)
            if not c or c["quality"] is None:
                continue
            x = gx + vi * (bw + 6)
            h = c["quality"] * ph * .85
            out.append(f'<rect x="{x:.0f}" y="{base-h:.0f}" width="{bw:.0f}" height="{h:.0f}" '
                       f'rx="4" fill="{vcolor[v]}"/>')
            out.append(f'<text x="{x+bw/2:.0f}" y="{base-h-6:.0f}" font-size="11" '
                       f'font-weight="600" fill="#1d1d1f" text-anchor="middle">{c["quality"]:.2f}</text>')
            if c.get("perf") is not None:
                out.append(f'<text x="{x+bw/2:.0f}" y="{base+30:.0f}" font-size="9" '
                           f'fill="#86868b" text-anchor="middle">p{c["perf"]:.2f}</text>')
            if c.get("chosen"):
                out.append(f'<text x="{x+bw/2:.0f}" y="{base-h-20:.0f}" font-size="15" '
                           f'fill="#ffcc00" text-anchor="middle">★</text>')
        out.append(f'<text x="{gx + len(versions)*(bw+6)/2:.0f}" y="{base+18}" '
                   f'font-size="13" fill="#1d1d1f" text-anchor="middle">{agent}</text>')
    lx = ox
    for vi, v in enumerate(versions):
        out.append(f'<rect x="{lx}" y="{oy-4}" width="11" height="11" rx="2" fill="{vcolor[v]}"/>')
        out.append(f'<text x="{lx+15}" y="{oy+6}" font-size="11" fill="#6e6e73">{v}</text>')
        lx += 30 + len(v) * 7
    out.append('</svg>')
    open(path, "w").write("\n".join(out))


if __name__ == "__main__":
    main()
