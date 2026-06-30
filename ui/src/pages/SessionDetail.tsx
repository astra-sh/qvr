import { useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { ChevronDown, ChevronRight, Dot } from "lucide-react";
import {
  api,
  prettyAgent,
  useFetch,
  type RawTraceView,
  type SessionScore,
  type SpanRow,
} from "../api";
import {
  Badge,
  CodeBlock,
  DetailHeader,
  EmptyState,
  ErrorBox,
  Loading,
  Back,
  Meta,
  MetaItem,
  Select,
  Tabs,
  Tag,
  VersionTag,
} from "../components/qvr";
import {
  fmtCount,
  fmtEpochMs,
  fmtMs,
  fmtTime,
  fmtTokenPair,
  prettyJSON,
  short,
} from "../lib/format";
import { spanKindTone } from "../lib/tones";

type View = "spans" | "raw";

export default function SessionDetail() {
  const { id = "" } = useParams();
  const { data, error, loading } = useFetch(() => api.session(id), `session:${id}`);
  const [view, setView] = useState<View>("spans");

  const session = data?.session;
  const title = session?.title || "untitled session";

  return (
    <>
      <Back to="/sessions" label="Sessions" />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && session && (
        <>
          <DetailHeader
            name={title}
            badges={<Badge tone="info">{prettyAgent(session.agent_name)}</Badge>}
          />
          {/* Row 1 — the session's metrics: timing, turn/tool counts, token
              usage, and the BYO-grader score as the trailing column. */}
          <Meta className="qvr-meta--dense">
            <MetaItem k="started">{fmtEpochMs(session.started_ms)}</MetaItem>
            {session.ended_ms > session.started_ms && (
              <MetaItem k="duration">{fmtMs(session.ended_ms - session.started_ms)}</MetaItem>
            )}
            <MetaItem k="turns">{session.turns}</MetaItem>
            <MetaItem k="tools">{session.tools}</MetaItem>
            <span
              className="qvr-meta__item"
              title={session.tokens_in == null && session.tokens_out == null ? "no usage reported" : undefined}
            >
              <span className="qvr-meta__k">tokens</span>
              <span
                className={
                  "qvr-meta__v" +
                  (session.tokens_in == null && session.tokens_out == null ? " qvr-table__muted" : "")
                }
              >
                {fmtTokenPair(session.tokens_in, session.tokens_out)}
              </span>
            </span>
            {data.scores && data.scores.length > 0 && <ScoresStat scores={data.scores} />}
          </Meta>
          {/* Row 2 — identity & location: model, session id, working dir, branch. */}
          <Meta className="qvr-meta--dense" style={{ marginTop: 4 }}>
            {session.model && <MetaItem k="model">{session.model}</MetaItem>}
            <span className="qvr-meta__item">
              <span className="qvr-meta__k">session</span>
              <Tag title={session.session_id}>{short(session.session_id, 8)}</Tag>
            </span>
            {session.working_directory && (
              <MetaItem k="cwd">{session.working_directory}</MetaItem>
            )}
            {session.git_branch && <MetaItem k="branch">{session.git_branch}</MetaItem>}
          </Meta>

          {/* Toggle between the processed (derived span) view and the lossless
              raw rows — the two representations of the same session. */}
          <div style={{ marginTop: 18 }}>
            <Tabs
              items={[
                { id: "spans", label: "spans", count: data.spans.length },
                { id: "raw", label: "raw", count: data.traces.length },
              ]}
              value={view}
              onChange={(v) => setView(v as View)}
            />
          </div>

          <div style={{ marginTop: 16 }}>
            {view === "spans" ? (
              <SpansView spans={data.spans} />
            ) : (
              <RawView traces={data.traces} />
            )}
          </div>
        </>
      )}
    </>
  );
}

// ScoresStat renders the session's BYO-grader verdicts as a sibling header stat,
// next to the tokens chip. A session can carry several metrics (score, exact,
// rubric, …) — and, rarely, several skills under one metric — so a <select> picks
// the metric and the value is shown per skill. It defaults to "score", the metric
// compare's version-cohort rollup aggregates; the dropdown lists every metric
// present, so a grader who annotated under a custom metric still sees it here.
function ScoresStat({ scores }: { scores: SessionScore[] }) {
  // Distinct metrics, "score" pinned first so the default is the rollup metric.
  const metrics = useMemo(() => {
    const set = new Set(scores.map((s) => s.metric));
    return [...set].sort((a, b) =>
      a === "score" ? -1 : b === "score" ? 1 : a.localeCompare(b),
    );
  }, [scores]);
  const [picked, setPicked] = useState("");
  const metric = picked && metrics.includes(picked) ? picked : metrics[0];

  const rows = useMemo(
    () => scores.filter((s) => s.metric === metric).sort((a, b) => a.skill.localeCompare(b.skill)),
    [scores, metric],
  );
  // Label each value by skill only when the metric spans more than one skill —
  // the common single-skill session stays terse.
  const multiSkill = rows.length > 1;

  return (
    <span className="qvr-meta__item">
      <span className="qvr-meta__k">scores</span>
      {metrics.length > 1 ? (
        <Select
          className="qvr-select qvr-select--sm"
          value={metric}
          onChange={(e) => setPicked(e.target.value)}
          aria-label="score metric"
        >
          {metrics.map((m) => (
            <option key={m} value={m}>
              {m}
            </option>
          ))}
        </Select>
      ) : (
        <Tag>{metric}</Tag>
      )}
      <span style={{ display: "inline-flex", gap: 10, alignItems: "center", flexWrap: "wrap" }}>
        {rows.map((r) => (
          <span
            key={`${r.skill}:${r.metric}`}
            style={{ display: "inline-flex", gap: 5, alignItems: "baseline" }}
            title={r.grader ? `grader: ${r.grader}` : undefined}
          >
            {multiSkill && <span className="qvr-table__muted">{r.skill}</span>}
            <span className="qvr-meta__v" style={{ fontVariantNumeric: "tabular-nums" }}>
              {r.value.toFixed(2)}
            </span>
            {r.grader && !multiSkill && (
              <span className="qvr-scan__scanner">grader {r.grader}</span>
            )}
          </span>
        ))}
      </span>
    </span>
  );
}

// ---- processed spans -------------------------------------------------------

interface ParsedAttrs {
  model?: string;
  // Usage attrs exist only when the agent's store reported them (absence ≠ 0).
  // inTokens is the TOTAL including cache; the cache fields are its sub-split.
  inTokens?: number;
  outTokens?: number;
  cacheRead?: number;
  cacheCreation?: number;
  prompt?: string;
  output?: string;
  toolName?: string;
  toolArgs?: string;
  toolResult?: string;
  toolDesc?: string;
  reasoning?: string;
  skillName?: string;
  skillRegistry?: string;
  skillVersion?: string;
  skillCommit?: string;
  skillActivation?: string;
  error?: string;
  // root-turn trace metadata
  threadId?: string;
  integration?: string;
  runDepth?: number;
  agentType?: string;
}

function parseAttrs(raw: string): ParsedAttrs {
  let a: Record<string, unknown> = {};
  try {
    a = JSON.parse(raw || "{}") as Record<string, unknown>;
  } catch {
    return {};
  }
  const str = (k: string) => (typeof a[k] === "string" ? (a[k] as string) : undefined);
  const num = (k: string) => (typeof a[k] === "number" ? (a[k] as number) : undefined);
  const firstMessage = (k: string): string | undefined => {
    const v = str(k);
    if (!v) return undefined;
    try {
      const msgs = JSON.parse(v) as { content?: string }[];
      return msgs?.map((m) => m.content).filter(Boolean).join("\n") || undefined;
    } catch {
      return v;
    }
  };
  return {
    model: str("gen_ai.request.model"),
    inTokens: num("gen_ai.usage.input_tokens"),
    outTokens: num("gen_ai.usage.output_tokens"),
    cacheRead: num("gen_ai.usage.cache_read_input_tokens"),
    cacheCreation: num("gen_ai.usage.cache_creation_input_tokens"),
    prompt: firstMessage("gen_ai.input.messages"),
    output: firstMessage("gen_ai.output.messages"),
    toolName: str("gen_ai.tool.name"),
    toolArgs: str("gen_ai.tool.call.arguments"),
    toolResult: str("gen_ai.tool.call.result"),
    toolDesc: str("gen_ai.tool.description"),
    reasoning: str("qvr.reasoning"),
    skillName: str("skill.name"),
    skillRegistry: str("skill.registry"),
    skillVersion: str("skill.version"),
    skillCommit: str("skill.commit"),
    skillActivation: str("skill.activation"),
    error: str("error.type"),
    threadId: str("qvr.thread_id"),
    integration: str("qvr.integration"),
    runDepth: num("qvr.run_depth"),
    agentType: str("qvr.agent_type"),
  };
}

// Identity fields exist on a span only when its load path proved which locked
// artifact ran (#146, #149); the VersionTag renders them as the quiet pin —
// or @unknown when the agent's records carried no evidence. The skill NAME
// tag is the loud part: tagging the session is the point, identity is
// supporting metadata.

// SpanNode is the derived-span tree: a span plus its children, linked by
// parent_span_id. The clean tree nests root turn → model → tool/skill, and an
// Agent tool → the subagent's whole subtree, exactly as derived.
interface SpanNode {
  span: SpanRow;
  children: SpanNode[];
}

// responseRow synthesizes the turn's final assistant message as its own row.
// The deriver models output as a gen_ai.output.messages attribute on the model
// span (the OTel-correct place); presenting it as a distinct node under that
// span — ordered after the tool/skill calls — is a pure UI choice, so we lift
// it here rather than emitting a backend span. Returns null when the turn had
// no assistant text (the deriver's "(no text output)" placeholder).
function responseRow(llm: SpanRow): SpanRow | null {
  const out = parseAttrs(llm.attributes).output;
  if (!out || out === "(no text output)") return null;
  return {
    ...llm,
    span_id: `${llm.span_id}::response`,
    parent_span_id: llm.span_id,
    kind: "RESPONSE",
    name: "final response",
    // Stamp it at the model span's end so it sorts after every tool child.
    start_ms: llm.end_ms,
    end_ms: llm.end_ms,
  };
}

// buildSpanTree links spans by parent_span_id into a forest. A span whose parent
// isn't in the set (a resumed/partial session) becomes a root, so nothing is
// dropped. Siblings are ordered by start time. A model span additionally gets a
// synthetic RESPONSE child appended last (see responseRow).
function buildSpanTree(spans: SpanRow[]): SpanNode[] {
  const byId = new Map(spans.map((s) => [s.span_id, s]));
  const childrenOf = new Map<string, SpanRow[]>();
  const roots: SpanRow[] = [];
  for (const s of spans) {
    const pid = s.parent_span_id;
    if (pid && byId.has(pid)) {
      const list = childrenOf.get(pid);
      if (list) list.push(s);
      else childrenOf.set(pid, [s]);
    } else {
      roots.push(s);
    }
  }
  const build = (s: SpanRow): SpanNode => {
    const children = (childrenOf.get(s.span_id) ?? [])
      .sort((a, b) => a.start_ms - b.start_ms)
      .map(build);
    if (s.kind === "LLM") {
      const resp = responseRow(s);
      if (resp) children.push({ span: resp, children: [] });
    }
    return { span: s, children };
  };
  return roots.sort((a, b) => a.start_ms - b.start_ms).map(build);
}

function SpansView({ spans }: { spans: SpanRow[] }) {
  const tree = useMemo(() => buildSpanTree(spans), [spans]);
  if (spans.length === 0) {
    return (
      <EmptyState title="no processed spans" art={false}>
        spans are derived from the transcript — switch to raw to see the captured bytes,
        or run qvr audit rederive.
      </EmptyState>
    );
  }
  return (
    <div style={{ display: "grid", gap: 4 }}>
      {tree.map((n) => (
        <SpanNodeRow key={n.span.span_id} node={n} />
      ))}
    </div>
  );
}

// SpanNodeRow renders one span row in the waterfall plus, indented beneath it,
// its children — so the whole trace reads as one nested tree. The row expands to
// show that span's detail (prompt/output/reasoning for a turn or model call;
// arguments/result for a tool; identity for a skill).
function SpanNodeRow({ node }: { node: SpanNode }) {
  const { span, children } = node;
  const a = parseAttrs(span.attributes);
  const isResponse = span.kind === "RESPONSE";
  const isMsg = span.kind === "LLM" || span.kind === "CHAIN";
  // The final assistant message renders as its own RESPONSE row (output only);
  // the model/turn rows therefore drop the output block to avoid showing it
  // twice — they keep input + reasoning.
  // agentType is intentionally NOT a detail trigger: it has no expansion panel
  // of its own (it shows as the "subagent" badge in the row header), so a CHAIN
  // span carrying only agentType must stay non-expandable rather than open a
  // blank container.
  const hasDetail = isResponse
    ? !!a.output
    : isMsg
      ? !!(a.prompt || a.reasoning)
      : !!(a.toolArgs || a.toolResult);
  // Rows start collapsed so the whole nested tree is visible at a glance (the
  // waterfall); clicking a row reveals that span's detail.
  const [open, setOpen] = useState(false);
  const dur = span.end_ms - span.start_ms;
  return (
    <div>
      <button
        type="button"
        onClick={() => hasDetail && setOpen((v) => !v)}
        style={{
          display: "flex",
          width: "100%",
          alignItems: "center",
          gap: 8,
          padding: "6px 10px",
          background: "none",
          border: "none",
          borderRadius: 6,
          textAlign: "left",
          cursor: hasDetail ? "pointer" : "default",
          color: "var(--text-faint)",
        }}
      >
        {hasDetail ? (
          open ? <ChevronDown size={14} /> : <ChevronRight size={14} />
        ) : (
          <Dot size={14} />
        )}
        <Badge tone={spanKindTone(span.kind)}>{span.kind}</Badge>
        <span
          style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)", color: "var(--text)" }}
        >
          {span.name}
        </span>
        {span.kind === "SKILL" && a.skillName && (
          <>
            <Badge tone="accent" dot>
              {a.skillName}
            </Badge>
            <VersionTag
              refName={a.skillVersion}
              sha={a.skillCommit}
              title={
                a.skillRegistry
                  ? `${a.skillRegistry}@${a.skillVersion}${a.skillCommit ? ` · ${a.skillCommit}` : ""}`
                  : undefined
              }
            />
            {a.skillActivation && a.skillActivation !== "tool" && (
              <Tag title="how the skill load was detected">{a.skillActivation}</Tag>
            )}
          </>
        )}
        {span.kind === "CHAIN" && a.agentType === "subagent" && (
          <Badge tone="warning" dot>
            subagent
          </Badge>
        )}
        {isMsg && <TurnTokens a={a} />}
        {((span.kind === "TOOL" && a.toolDesc) || (isResponse && a.output)) && (
          <span
            className="qvr-scan__scanner"
            style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 }}
          >
            {isResponse ? a.output : a.toolDesc}
          </span>
        )}
        {a.error && <Badge tone="danger">{a.error}</Badge>}
        {dur > 0 && (
          <span className="qvr-scan__scanner" style={{ marginLeft: "auto" }}>
            {fmtMs(dur)}
          </span>
        )}
      </button>
      {open && hasDetail && (
        <div style={{ padding: "2px 10px 10px 30px", display: "grid", gap: 10 }}>
          {isResponse ? (
            a.output && <MessageBlock label="final response" tone="assistant" text={a.output} />
          ) : (
            <>
              {a.prompt && <MessageBlock label="input" tone="user" text={a.prompt} />}
              {a.reasoning && <MessageBlock label="reasoning" tone="assistant" text={a.reasoning} />}
            </>
          )}
          {a.toolArgs && <CodeBlock value={pretty(a.toolArgs)} label="arguments" />}
          {a.toolResult && <CodeBlock value={a.toolResult} label="result" />}
        </div>
      )}
      {children.length > 0 && (
        <div
          style={{
            marginLeft: 16,
            paddingLeft: 8,
            borderLeft: "1px solid var(--border)",
            display: "grid",
            gap: 4,
          }}
        >
          {children.map((c) => (
            <SpanNodeRow key={c.span.span_id} node={c} />
          ))}
        </div>
      )}
    </div>
  );
}

// TurnTokens renders the turn's usage, honest about absence: a store that
// reported nothing reads "tokens n/a" (never 0); a one-sided report (copilot
// records only output per turn) shows "—" on the missing side; the cached
// share of the input rides inline, with cache writes in the tooltip.
function TurnTokens({ a }: { a: ParsedAttrs }) {
  if (a.inTokens == null && a.outTokens == null) {
    return <span className="qvr-scan__scanner qvr-table__muted">tokens n/a</span>;
  }
  const inSide =
    a.inTokens == null
      ? "—"
      : `${fmtCount(a.inTokens)}${a.cacheRead ? ` (${fmtCount(a.cacheRead)} cached)` : ""}`;
  const outSide = a.outTokens == null ? "—" : fmtCount(a.outTokens);
  const title = a.cacheCreation ? `cache writes: ${fmtCount(a.cacheCreation)}` : undefined;
  return (
    <span className="qvr-scan__scanner" title={title}>
      {inSide} in / {outSide} out tok
    </span>
  );
}

function MessageBlock({
  label,
  tone,
  text,
}: {
  label: string;
  tone: "user" | "assistant";
  text: string;
}) {
  const bar = tone === "user" ? "var(--info)" : "var(--success)";
  return (
    <div style={{ borderLeft: `2px solid ${bar}`, paddingLeft: 12 }}>
      <div className="qvr-meta__k" style={{ marginBottom: 2 }}>
        {label}
      </div>
      <div
        style={{
          whiteSpace: "pre-wrap",
          wordBreak: "break-word",
          fontFamily: "var(--font-body)",
          fontSize: "var(--text-sm)",
          lineHeight: "var(--leading-normal)",
          color: "var(--text)",
        }}
      >
        {text}
      </div>
    </div>
  );
}

// pretty re-indents a JSON string when it parses, else returns it unchanged.
function pretty(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// ---- raw traces ------------------------------------------------------------

function RawView({ traces }: { traces: RawTraceView[] }) {
  if (traces.length === 0) {
    return (
      <EmptyState title="no raw rows" art={false}>
        nothing captured for this session.
      </EmptyState>
    );
  }
  return (
    <div style={{ display: "grid", gap: 8 }}>
      {traces.map((t) => (
        <RawRow key={t.seq} trace={t} />
      ))}
    </div>
  );
}

function RawRow({ trace }: { trace: RawTraceView }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="qvr-card">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        style={{
          display: "flex",
          width: "100%",
          alignItems: "center",
          gap: 10,
          padding: "9px 14px",
          background: "none",
          border: "none",
          textAlign: "left",
          cursor: "pointer",
          color: "var(--text-faint)",
        }}
      >
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        <span
          style={{
            width: 40,
            flex: "none",
            fontFamily: "var(--font-mono)",
            fontSize: "var(--text-xs)",
            color: "var(--text-faint)",
          }}
        >
          #{trace.seq}
        </span>
        <Badge tone={trace.source === "transcript" ? "info" : "warning"}>
          {trace.source === "hook_payload" ? "hook" : "transcript"}
        </Badge>
        {trace.hook_type && <Tag>{trace.hook_type}</Tag>}
        <span className="qvr-scan__scanner" style={{ marginLeft: "auto" }}>
          {fmtTime(trace.captured_at)}
        </span>
      </button>
      {open && (
        <div style={{ padding: "0 14px 12px" }}>
          <CodeBlock value={prettyJSON(trace.raw)} label="raw" />
          {trace.source_path && (
            <p className="qvr-scan__scanner" style={{ marginTop: 4 }}>
              {trace.source_path}
            </p>
          )}
        </div>
      )}
    </div>
  );
}
