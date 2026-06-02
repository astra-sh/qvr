import { Link } from "react-router-dom";
import { api, useFetch } from "../api";
import {
  Card,
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  PageHeader,
  Pill,
  StatCard,
} from "../components/ui";

export default function Overview() {
  const { data, error, loading } = useFetch(api.overview, "overview");

  return (
    <>
      <PageHeader
        title="Overview"
        subtitle={
          data
            ? data.scope === "project"
              ? "Scoped to this project — sessions, events, skills, and gate."
              : data.scope === "global"
                ? "Global scope (--global) — every recorded session, event, and skill."
                : "All scopes (--all) — project and global combined."
            : "What Quiver has recorded on this machine."
        }
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && (
        <>
          {!data.audit_enabled && (
            <div className="mb-6 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
              Audit pipeline not enabled — session history is empty. Run{" "}
              <code className="rounded bg-amber-100 px-1.5 py-0.5">qvr audit enable</code> to start
              recording agent sessions.
            </div>
          )}

          <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
            <StatCard label="Sessions" value={data.sessions} />
            <StatCard label="Events" value={data.events} />
            <StatCard label="Skills" value={data.skills} />
            <StatCard label="Registries" value={data.registries} />
          </div>
          {data.project_root && (
            <p className="mt-2 text-xs text-gray-400">
              Sessions and events scoped to{" "}
              <code className="rounded bg-gray-100 px-1 py-0.5">{data.project_root}</code> — run{" "}
              <code className="rounded bg-gray-100 px-1 py-0.5">qvr ui --global</code> for all activity.
            </p>
          )}

          <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card title="Scan gate">
              <div className="flex flex-wrap gap-6">
                <GateStat label="Allowed" value={data.gate_allowed} tone="green" />
                <GateStat label="Blocked" value={data.gate_blocked} tone="red" />
                <GateStat label="Unscanned" value={data.gate_unscanned} tone="gray" />
              </div>
              <p className="mt-4 text-xs text-gray-500">
                Recorded at install time. See the{" "}
                <Link className="text-blue-600 hover:underline" to="/scan">
                  Scan
                </Link>{" "}
                page to run a live scan.
              </p>
            </Card>

            <Card title="Recent sessions">
              {data.recent_sessions.length === 0 ? (
                <Empty>No sessions recorded yet.</Empty>
              ) : (
                <ul className="divide-y divide-gray-100">
                  {data.recent_sessions.map((s) => (
                    <li key={s.id} className="flex items-center justify-between py-2">
                      <Link
                        to={`/sessions/${s.id}`}
                        className="text-sm font-medium text-blue-600 hover:underline"
                      >
                        {s.agent_name}
                        {s.project_name ? ` · ${s.project_name}` : ""}
                      </Link>
                      <span className="text-xs text-gray-400">{fmtTime(s.started_at)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          </div>
        </>
      )}
    </>
  );
}

function GateStat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: "green" | "red" | "gray";
}) {
  return (
    <div>
      <div className="text-2xl font-semibold text-gray-900">{value}</div>
      <Pill tone={tone}>{label}</Pill>
    </div>
  );
}
