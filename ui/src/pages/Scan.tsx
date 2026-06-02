import { useState } from "react";
import { api, useFetch, type ScanRunResult } from "../api";
import {
  Empty,
  ErrorBox,
  Loading,
  Mono,
  PageHeader,
  Pill,
  StatusPill,
  Table,
  Td,
  Th,
} from "../components/ui";

export default function Scan() {
  const { data, error, loading } = useFetch(api.scanSummary, "scan-summary");
  // Per-skill live scan state, keyed by skill name.
  const [running, setRunning] = useState<Record<string, boolean>>({});
  const [results, setResults] = useState<Record<string, ScanRunResult>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function runScan(name: string) {
    setRunning((r) => ({ ...r, [name]: true }));
    setErrors((e) => ({ ...e, [name]: "" }));
    try {
      const res = await api.runScan(name);
      setResults((r) => ({ ...r, [name]: res }));
    } catch (e) {
      setErrors((er) => ({ ...er, [name]: e instanceof Error ? e.message : String(e) }));
    } finally {
      setRunning((r) => ({ ...r, [name]: false }));
    }
  }

  return (
    <>
      <PageHeader
        title="Scan"
        subtitle="Install-time gate decisions, with on-demand live re-scan per skill."
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && <Empty>No installed skills to scan.</Empty>}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>Skill</Th>
              <Th>Registry</Th>
              <Th>Recorded gate</Th>
              <Th>Scanner</Th>
              <Th>Live scan</Th>
            </tr>
          }
        >
          {data.map((row) => {
            const res = results[row.name];
            const err = errors[row.name];
            return (
              <tr key={row.name} className="align-top hover:bg-gray-50">
                <Td>
                  <span className="font-medium">{row.name}</span>
                  {row.mode && (
                    <Pill tone="amber">
                      <span className="ml-1">{row.mode}</span>
                    </Pill>
                  )}
                </Td>
                <Td>{row.registry || "—"}</Td>
                <Td>
                  <StatusPill value={row.decision || "unscanned"} />
                </Td>
                <Td>{row.scannerVersion || "—"}</Td>
                <Td className="min-w-[18rem]">
                  <button
                    onClick={() => runScan(row.name)}
                    disabled={running[row.name]}
                    className="rounded-md bg-gray-900 px-3 py-1 text-xs font-medium text-white hover:bg-gray-700 disabled:opacity-50"
                  >
                    {running[row.name] ? "Scanning…" : "Run scan"}
                  </button>
                  {err && <div className="mt-2 text-xs text-red-600">{err}</div>}
                  {res && <ScanFindings result={res} recorded={row.decision} />}
                </Td>
              </tr>
            );
          })}
        </Table>
      )}
    </>
  );
}

function ScanFindings({
  result,
  recorded,
}: {
  result: ScanRunResult;
  recorded?: string;
}) {
  // Go marshals an empty slice / zero struct as null, so coalesce defensively.
  const s = result.summary ?? { critical: 0, error: 0, warning: 0, info: 0 };
  const findings = result.findings ?? [];
  const live = result.gate?.decision;
  // Drift = the recorded install-time verdict no longer matches a fresh scan of
  // the bytes on disk under the same policy. This is the one thing the Scan
  // page exists to surface (issue #140).
  const rec = recorded || "unscanned";
  const drifted = !!live && rec !== "unscanned" && live !== rec;
  return (
    <div className="mt-2">
      {live && (
        <div className="mb-2">
          <div className="flex items-center gap-2 text-xs">
            <span className="text-gray-500">live gate</span>
            <StatusPill value={live} />
            {result.gate?.threshold && (
              <span className="text-gray-400">≥ {result.gate.threshold}</span>
            )}
          </div>
          {drifted && (
            <div className="mt-1 rounded border border-amber-300 bg-amber-50 px-2 py-1 text-xs text-amber-800">
              ⚠ drift — recorded <Mono>{rec}</Mono> at install, now <Mono>{live}</Mono> on a live scan.
            </div>
          )}
        </div>
      )}
      <div className="flex flex-wrap gap-1">
        {s.critical > 0 && <Pill tone="red">critical {s.critical}</Pill>}
        {s.error > 0 && <Pill tone="red">error {s.error}</Pill>}
        {s.warning > 0 && <Pill tone="amber">warning {s.warning}</Pill>}
        {s.info > 0 && <Pill tone="gray">info {s.info}</Pill>}
        {s.critical + s.error + s.warning + s.info === 0 && (
          <Pill tone="green">no findings</Pill>
        )}
      </div>
      {findings.length > 0 && (
        <ul className="mt-2 space-y-1.5">
          {findings.map((f, i) => (
            <li key={i} className="rounded border border-gray-100 bg-gray-50 px-2 py-1.5">
              <div className="flex items-center gap-2">
                <StatusPill value={f.severity} />
                {f.category && <span className="text-xs text-gray-500">{f.category}</span>}
              </div>
              <div className="mt-0.5 text-xs text-gray-700">{f.message}</div>
              {f.file && (
                <div className="text-[0.6875rem] text-gray-400">
                  <Mono>
                    {f.file}
                    {f.line ? `:${f.line}` : ""}
                  </Mono>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
