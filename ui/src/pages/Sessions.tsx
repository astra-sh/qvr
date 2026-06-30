import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { RefreshCw } from "lucide-react";
import {
  api,
  prettyAgent,
  scopeToken,
  useFetch,
  type DiscoverReport,
  type SessionScore,
} from "../api";
import {
  Badge,
  Button,
  EmptyState,
  ErrorBox,
  Field,
  Loading,
  MultiSelect,
  PageHead,
  RefreshButton,
  Select,
  Table,
  Td,
  Th,
} from "../components/qvr";
import { fmtEpochMs, fmtSpan, fmtTokenPair } from "../lib/format";

// The agents qvr can discover today. The filter offers these plus any agent
// actually present in the loaded rows (so an unexpected one still shows up).
const KNOWN_AGENTS = [
  "claude",
  "codex",
  "copilot",
  "cursor",
  "droid",
  "gemini",
  "hermes",
  "openclaw",
  "opencode",
  "pi",
];

export default function Sessions() {
  const [agent, setAgent] = useState("");
  // Skills + versions are multi-select checklists: a session matches ANY chosen
  // skill AND ANY chosen version (the server ORs within each).
  const [skills, setSkills] = useState<string[]>([]);
  const [versions, setVersions] = useState<string[]>([]);
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  // Token sort is server-side (the list is limit-truncated), so it lives in
  // the fetch key like the filters.
  const [sortTokens, setSortTokens] = useState(false);

  // Re-fetch whenever the scope or any filter changes (the key encodes them all).
  const key = `sessions:${scopeToken()}:${agent}|${skills.join(",")}|${versions.join(",")}|${since}|${until}|${sortTokens}`;
  // 10s polling keeps the list live against the server's background scan.
  const { data, error, loading, reload } = useFetch(
    () =>
      api.sessions({ agent, skills, versions, since, until, sort: sortTokens ? "tokens" : undefined }),
    key,
    10_000,
  );

  // Facets for the checklists come from the whole scoped DB, not the truncated
  // page: the per-skill usage rollup carries every skill and the distinct
  // versions each one ran, so a skill's older versions show up even when no
  // recent session used them. Installed skills + the loaded rows fill any gap.
  const metrics = useFetch(() => api.metricsSkills(), `sessions-metrics:${scopeToken()}`);
  const skillsList = useFetch(() => api.skills(), `sessions-skills:${scopeToken()}`);

  const agentOptions = useMemo(() => {
    const set = new Set(KNOWN_AGENTS);
    data?.forEach((s) => s.agent_name && set.add(s.agent_name));
    return [...set].sort();
  }, [data]);

  const skillOptions = useMemo(() => {
    const set = new Set<string>();
    metrics.data?.skills?.forEach((s) => set.add(s.name));
    skillsList.data?.forEach((s) => set.add(s.name));
    data?.forEach((s) => s.skills?.forEach((n) => set.add(n)));
    return [...set].sort();
  }, [metrics.data, skillsList.data, data]);

  // skill → its distinct versions, from the DB rollup, with anything seen in the
  // loaded rows folded in. "unknown" is dropped — filtering by it matches nothing
  // (skill.version is empty there), so it would be a dead option.
  const skillVersions = useMemo(() => {
    const m = new Map<string, Set<string>>();
    const add = (skill: string, v?: string) => {
      if (!v || v === "unknown") return;
      const set = m.get(skill) ?? new Set<string>();
      set.add(v);
      m.set(skill, set);
    };
    metrics.data?.skills?.forEach((s) => s.versions?.forEach((v) => add(s.name, v)));
    data?.forEach((s) => s.skill_versions?.forEach((v) => add(v.skill, v.version)));
    return m;
  }, [metrics.data, data]);

  // Version options narrow to the selected skills (all skills when none chosen),
  // newest-first.
  const versionOptions = useMemo(() => {
    const scope = skills.length > 0 ? skills : [...skillVersions.keys()];
    const set = new Set<string>();
    scope.forEach((sk) => skillVersions.get(sk)?.forEach((v) => set.add(v)));
    return [...set].sort().reverse();
  }, [skills, skillVersions]);

  // Changing the skill set re-scopes the versions; drop any selected version that
  // no longer belongs to a chosen skill so the filter can't strand a dead value.
  const onSkillsChange = (next: string[]) => {
    setSkills(next);
    setVersions((prev) => {
      if (prev.length === 0) return prev;
      const scope = next.length > 0 ? next : [...skillVersions.keys()];
      const allowed = new Set<string>();
      scope.forEach((sk) => skillVersions.get(sk)?.forEach((v) => allowed.add(v)));
      return prev.filter((v) => allowed.has(v));
    });
  };

  const active = agent || skills.length > 0 || versions.length > 0 || since || until;
  const clear = () => {
    setAgent("");
    setSkills([]);
    setVersions([]);
    setSince("");
    setUntil("");
  };

  return (
    <>
      <PageHead
        title="Sessions"
        sub="Recorded agent sessions, newest first. Named by the first prompt you typed."
        actions={
          <>
            <RefreshButton onClick={reload} busy={loading} />
            <DiscoverButton onDone={reload} />
          </>
        }
      />

      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: 12, marginBottom: 16 }}>
        <Field label="agent">
          <Select value={agent} onChange={(e) => setAgent(e.target.value)}>
            <option value="">all</option>
            {agentOptions.map((a) => (
              <option key={a} value={a}>
                {prettyAgent(a)}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="skills">
          <MultiSelect
            options={skillOptions}
            selected={skills}
            onChange={onSkillsChange}
            noun="skills"
            emptyText="no skills yet"
          />
        </Field>
        <Field label="versions">
          <MultiSelect
            options={versionOptions}
            selected={versions}
            onChange={setVersions}
            noun="versions"
            emptyText={skills.length > 0 ? "no versions for these skills" : "no versions yet"}
          />
        </Field>
        <Field label="from">
          <DateInput value={since} onChange={setSince} />
        </Field>
        <Field label="to">
          <DateInput value={until} onChange={setUntil} />
        </Field>
        {active && (
          <Button variant="ghost" size="sm" onClick={clear}>
            clear
          </Button>
        )}
      </div>

      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && (
        <EmptyState title={active ? "no sessions match" : "no sessions recorded"}>
          {active
            ? "Loosen the filters — nothing in this window."
            : "Skill-using agent sessions appear here. Hit discover (or run qvr audit discover) to back-fill from your agents' own session stores — no agent setup needed."}
        </EmptyState>
      )}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>session</Th>
              <Th>agent</Th>
              <Th>skills</Th>
              <Th>version</Th>
              <Th>score</Th>
              <Th>started</Th>
              <Th>turns</Th>
              <Th>tools</Th>
              <Th>duration</Th>
              <Th onSort={() => setSortTokens((v) => !v)} sortActive={sortTokens}>
                tokens (in / out)
              </Th>
            </tr>
          }
        >
          {data.map((s) => (
            <tr key={s.session_id}>
              <Td title={s.title || undefined}>
                <div
                  style={{
                    maxWidth: "42ch",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  <Link to={`/sessions/${s.session_id}`}>
                    {s.title || <span className="qvr-table__muted">untitled session</span>}
                  </Link>
                </div>
              </Td>
              <Td>
                <Badge tone="info">{prettyAgent(s.agent_name)}</Badge>
              </Td>
              <Td>
                {s.skills && s.skills.length > 0 ? (
                  <span style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
                    {s.skills.map((n) => (
                      <Badge key={n} tone="accent">
                        {n}
                      </Badge>
                    ))}
                  </span>
                ) : (
                  <span className="qvr-table__muted">—</span>
                )}
              </Td>
              <Td>
                {s.skill_versions && s.skill_versions.length > 0 ? (
                  <span style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
                    {s.skill_versions.map((v) => (
                      <Badge key={`${v.skill}@${v.version}`} tone="neutral" title={v.commit || undefined}>
                        {v.version}
                      </Badge>
                    ))}
                  </span>
                ) : (
                  <span className="qvr-table__muted">—</span>
                )}
              </Td>
              <Td>
                <ScoreCell scores={s.scores} />
              </Td>
              <Td muted>{fmtEpochMs(s.started_ms)}</Td>
              <Td muted>{s.turns}</Td>
              <Td muted>{s.tools}</Td>
              <Td muted>{fmtSpan(s.ended_ms - s.started_ms)}</Td>
              <Td muted={s.tokens_in == null && s.tokens_out == null}>
                {fmtTokenPair(s.tokens_in, s.tokens_out)}
              </Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}

// ScoreCell renders a session's BYO-grader verdict in the list. It prefers the
// "score" metric — the one compare's version-cohort rollup aggregates — and falls
// back to whatever metric the session was graded under, so a grade written under a
// custom metric still shows a number (with the metric in its tooltip) rather than
// a misleading dash. Ungraded sessions read "—", matching the version column's
// empty state. The value is the grader's number verbatim; we don't color by a
// pass/fail scale we can't assume.
function ScoreCell({ scores }: { scores?: SessionScore[] }) {
  const list = scores ?? [];
  if (list.length === 0) return <span className="qvr-table__muted">—</span>;
  const metric = list.some((s) => s.metric === "score")
    ? "score"
    : [...list].sort((a, b) => a.metric.localeCompare(b.metric))[0].metric;
  const rows = list
    .filter((s) => s.metric === metric)
    .sort((a, b) => a.skill.localeCompare(b.skill));
  return (
    <span style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
      {rows.map((r) => (
        <Badge
          key={r.skill}
          tone="neutral"
          title={`${r.skill} · ${r.metric}${r.grader ? ` · grader ${r.grader}` : ""}`}
        >
          {r.value.toFixed(2)}
        </Badge>
      ))}
    </span>
  );
}

// DiscoverButton triggers POST /api/discover — scan the agents' native session
// stores for new/changed sessions — then reports the outcome inline and
// reloads the list.
function DiscoverButton({ onDone }: { onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setNote(null);
    try {
      const rep = await api.discover();
      setNote(summarizeDiscover(rep));
      onDone();
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}>
      {note && <span className="qvr-scan__scanner">{note}</span>}
      <Button size="sm" onClick={run} disabled={busy} leftIcon={<RefreshCw size={13} />}>
        {busy ? "scanning…" : "discover"}
      </Button>
    </span>
  );
}

function summarizeDiscover(rep: DiscoverReport): string {
  let ingested = 0;
  let skipped = 0;
  let unchanged = 0;
  rep.agents?.forEach((a) => {
    ingested += a.ingested;
    skipped += a.skipped;
    unchanged += a.unchanged;
  });
  if (ingested === 0 && skipped === 0) return `up to date (${unchanged} unchanged)`;
  const parts = [`${ingested} recorded`];
  if (skipped > 0) parts.push(`${skipped} without skills skipped`);
  return parts.join(", ");
}

function DateInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <span className="qvr-input-wrap">
      <input
        type="date"
        className="qvr-input"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </span>
  );
}
