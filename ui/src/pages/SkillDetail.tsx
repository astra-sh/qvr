import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { api, useFetch } from "../api";
import {
  Card,
  ErrorBox,
  fmtTime,
  Loading,
  Mono,
  PageHeader,
  Pill,
  StatusPill,
} from "../components/ui";

function Row({ label, children }: { label: string; children: ReactNode }) {
  if (children == null || children === "") return null;
  return (
    <div className="flex gap-4 py-1.5 text-sm">
      <div className="w-32 shrink-0 text-gray-500">{label}</div>
      <div className="min-w-0 break-words text-gray-800">{children}</div>
    </div>
  );
}

export default function SkillDetail() {
  const { name = "" } = useParams();
  const { data, error, loading } = useFetch(() => api.skill(name), `skill:${name}`);

  return (
    <>
      <div className="mb-4">
        <Link to="/skills" className="text-sm text-blue-600 hover:underline">
          ← Skills
        </Link>
      </div>
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && (
        <>
          <PageHeader title={data.name} subtitle={data.description} />

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card title="Details">
              <Row label="Registry">{data.registry}</Row>
              <Row label="Ref">{data.ref}</Row>
              <Row label="Commit">{data.commit ? <Mono>{data.commit}</Mono> : null}</Row>
              {data.commitDrift && (
                <Row label="Drift">
                  <span className="text-red-600">
                    <Mono>{data.commitDrift}</Mono> (lock out of date)
                  </span>
                </Row>
              )}
              <Row label="Mode">{data.mode ? <Pill tone="amber">{data.mode}</Pill> : "shared"}</Row>
              <Row label="Source">{data.source}</Row>
              <Row label="License">{data.license}</Row>
              <Row label="Compatibility">{data.compatibility}</Row>
              <Row label="Tools">{data.allowedTools}</Row>
              <Row label="Installed">{data.installedAt ? fmtTime(data.installedAt) : null}</Row>
              <Row label="Worktree">{data.worktree ? <Mono>{data.worktree}</Mono> : null}</Row>
              <Row label="Tree OID">{data.treeOID ? <Mono>{data.treeOID}</Mono> : null}</Row>
              <Row label="Subtree">
                {data.subtreeHash ? <Mono>{data.subtreeHash}</Mono> : null}
              </Row>
            </Card>

            <div className="space-y-6">
              <Card title="Targets">
                {(data.targetDetails ?? []).length === 0 ? (
                  <div className="text-sm text-gray-500">
                    {(data.targets ?? []).join(", ") || "—"}
                  </div>
                ) : (
                  <ul className="space-y-2">
                    {data.targetDetails!.map((t) => (
                      <li key={t.target} className="flex items-center justify-between text-sm">
                        <span className="font-medium">{t.target}</span>
                        <StatusPill value={t.ok ? "success" : "error"} />
                      </li>
                    ))}
                  </ul>
                )}
              </Card>

              {data.files && data.files.length > 0 && (
                <Card title={`Files (${data.files.length})`}>
                  <ul className="max-h-64 space-y-0.5 overflow-y-auto">
                    {data.files.map((f) => (
                      <li key={f}>
                        <Mono>{f}</Mono>
                      </li>
                    ))}
                  </ul>
                </Card>
              )}
            </div>
          </div>
        </>
      )}
    </>
  );
}
