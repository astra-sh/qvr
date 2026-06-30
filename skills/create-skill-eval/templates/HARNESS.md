---
# HARNESS.md — the manifest the optimizer reads to run this skill's eval.
# It makes the optimizer skill-agnostic: declare metrics, axes, run config here,
# and the loop drives cohorts + builds the frontier without knowing what the
# skill does. Frozen with the rest of eval/. See references/eval-contract.md.
skill: <inner-skill-name>
eval_format: native                # this dir is the native eval source

metrics:                           # every deterministic metric eval.py emits
  - id: exact                      # REQUIRED canonical correctness metric, ∈[0,1]
    axis: quality
    direction: max
    optimizer_role: gate           # gate = checked against --target-quality
  # - id: perf                     # uncomment if eval.py grades perf
  #   axis: perf
  #   direction: max
  #   optimizer_role: frontier

rubric: rubric.yaml                # LLM-judge dimensions (the rubric axis)

cost:                              # cost axis comes from the ledger, not eval.py
  source: ledger                   # qvr audit sessions -> tokens_out
  axis: cost
  direction: min

agents: [claude, codex]            # frozen fan-out set
n: 2                               # runs per agent per variant
models: {claude: claude-sonnet-4-6, codex: gpt-5.5}   # pinned for comparable cost
exit: {target_quality: 1.0, max_rounds: 6, patience: 2}

fixtures: []                       # files eval.py needs present (e.g. fixture.db)
prompt_template: null              # null = use the runner's default template
---

# <inner-skill-name> eval harness

**What "correct" means here.** <one paragraph: the deterministic check eval.py
performs — exact string? result-set match? schema? build exit code? Name the
grader strategy and why it's leak-safe (behaviour graders) — see
references/behaviour-graders.md.>

**Scenarios.** <why these N cases; which competency each pins; which are
input-only / rubric-only.>

**The freeze contract.** Everything in this `eval/` is byte-frozen for the loop:
`git diff <baseline> <candidate> -- '*/eval/*'` must stay empty. Only the skill's
behaviour files change between rounds. Fixing the harness means re-freezing a new
baseline, not editing mid-loop.
