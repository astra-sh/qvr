import type { ActivitySummary, SkillUsageRow } from "../api";
import { Card } from "./qvr";
import { fmtCount, fmtTok } from "../lib/format";

// TokenBand is the overview's Module A: where the project's tokens go. Left, a
// spend panel — the headline total plus an in/out dual meter. Right, a
// 100%-stacked share strip of the top skills by token volume (a monochrome lime
// ramp, never rainbow). Tokens are the source of truth; no dollar cost is shown
// (it can't be attributed cleanly across API vs subscription plans). The share
// is exposure, not exclusive: a session firing several skills counts toward each.

const TOP_N = 5;
// Opacity ramp for the top-N share segments; the remainder uses a border tone.
const RAMP = [1, 0.74, 0.55, 0.4, 0.3];
const segColor = (i: number) =>
  i < RAMP.length
    ? `color-mix(in srgb, var(--brand-600) ${RAMP[i] * 100}%, var(--surface-inset))`
    : "var(--border-strong)";

interface ShareSeg {
  name: string;
  tokens: number;
  pct: number;
  color: string;
}

function shareSegments(skills: SkillUsageRow[]): { segs: ShareSeg[]; total: number; topShare: number } {
  const withTokens = skills
    .map((s) => ({ name: s.name, tokens: (s.tokensIn ?? 0) + (s.tokensOut ?? 0) }))
    .filter((s) => s.tokens > 0)
    .sort((a, b) => b.tokens - a.tokens);
  const total = withTokens.reduce((acc, s) => acc + s.tokens, 0);
  if (total === 0) return { segs: [], total: 0, topShare: 0 };

  const top = withTokens.slice(0, TOP_N);
  const restTokens = withTokens.slice(TOP_N).reduce((acc, s) => acc + s.tokens, 0);
  const segs: ShareSeg[] = top.map((s, i) => ({
    name: s.name,
    tokens: s.tokens,
    pct: (s.tokens / total) * 100,
    color: segColor(i),
  }));
  if (restTokens > 0) {
    segs.push({
      name: `+${withTokens.length - top.length} more`,
      tokens: restTokens,
      pct: (restTokens / total) * 100,
      color: segColor(TOP_N),
    });
  }
  const topShare = top.reduce((acc, s) => acc + s.tokens, 0) / total;
  return { segs, total, topShare };
}

export default function TokenBand({
  summary,
  skills,
}: {
  summary: ActivitySummary;
  skills: SkillUsageRow[];
}) {
  const tin = summary.tokens_in;
  const tout = summary.tokens_out;
  const total = tin + tout;
  const meterMax = Math.max(tin, tout, 1);
  const { segs, topShare } = shareSegments(skills);

  return (
    <Card>
      <div className="ovr-band">
        <div className="ovr-band__spend">
          <div className="ovr-band__big">{fmtTok(total)}</div>
          <div className="ovr-band__cap">tokens</div>
          <div className="ovr-band__sub">
            <b>{fmtCount(summary.turns)}</b> turns · <b>{fmtCount(summary.tools)}</b> tool calls
          </div>
          <div className="ovr-meter">
            <Meter label="in" value={tin} max={meterMax} fill="var(--brand-600)" />
            <Meter label="out" value={tout} max={meterMax} fill="var(--ink-500)" />
          </div>
        </div>

        <div className="ovr-band__share">
          <div className="ovr-lbl">
            <span>token share by skill</span>
            {segs.length > 0 && (
              <span className="ovr-lbl__hint">
                top {Math.min(TOP_N, segs.length)} = {Math.round(topShare * 100)}% of skill tokens
              </span>
            )}
          </div>
          {segs.length === 0 ? (
            <p className="qvr-sub">no skill token usage recorded yet.</p>
          ) : (
            <>
              <div className="ovr-share__bar">
                {segs.map((s) => (
                  <span
                    key={s.name}
                    className="ovr-share__seg"
                    style={{ width: `${s.pct}%`, background: s.color }}
                    title={`${s.name} · ${fmtTok(s.tokens)}`}
                  >
                    {s.pct > 11 ? `${Math.round(s.pct)}%` : ""}
                  </span>
                ))}
              </div>
              <div className="ovr-share__leg">
                {segs.map((s) => (
                  <span key={s.name} className="ovr-sl">
                    <span className="ovr-sl__sw" style={{ background: s.color }} />
                    {s.name} <b>{fmtTok(s.tokens)}</b>
                  </span>
                ))}
              </div>
              <p className="ovr-note">
                a session that fires several skills counts toward each — exposure, not exclusive
                attribution.
              </p>
            </>
          )}
        </div>
      </div>
    </Card>
  );
}

function Meter({
  label,
  value,
  max,
  fill,
}: {
  label: string;
  value: number;
  max: number;
  fill: string;
}) {
  const pct = Math.max((value / max) * 100, value > 0 ? 1.5 : 0);
  return (
    <div className="ovr-meter__row">
      <span className="ovr-meter__k">{label}</span>
      <span className="ovr-meter__track">
        <span className="ovr-meter__fill" style={{ width: `${pct}%`, background: fill }} />
      </span>
      <span className="ovr-meter__v">{fmtTok(value)}</span>
    </div>
  );
}
