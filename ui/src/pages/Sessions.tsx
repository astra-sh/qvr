import { Link } from "react-router-dom";
import { api, useFetch } from "../api";
import {
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  PageHeader,
  Pill,
  Table,
  Td,
  Th,
} from "../components/ui";

export default function Sessions() {
  const { data, error, loading } = useFetch(api.sessions, "sessions");

  return (
    <>
      <PageHeader
        title="Sessions"
        subtitle="Recorded agent sessions, newest first — the run history."
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && (
        <Empty>
          No sessions recorded. Enable the audit pipeline with{" "}
          <code className="rounded bg-gray-100 px-1.5 py-0.5">qvr audit enable</code>.
        </Empty>
      )}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>Agent</Th>
              <Th>Project</Th>
              <Th>Started</Th>
              <Th>Actions</Th>
              <Th>Errors</Th>
              <Th>Skills</Th>
            </tr>
          }
        >
          {data.map((s) => (
            <tr key={s.id} className="hover:bg-gray-50">
              <Td>
                <Link
                  to={`/sessions/${s.id}`}
                  className="font-medium text-blue-600 hover:underline"
                >
                  {s.agent_name}
                </Link>
              </Td>
              <Td>{s.project_name || "—"}</Td>
              <Td>{fmtTime(s.started_at)}</Td>
              <Td>{s.total_actions}</Td>
              <Td>{s.errors > 0 ? <Pill tone="red">{s.errors}</Pill> : 0}</Td>
              <Td>
                <div className="flex flex-wrap gap-1">
                  {(s.skills_touched ?? []).length === 0
                    ? "—"
                    : s.skills_touched!.map((sk) => (
                        <Pill key={sk} tone="blue">
                          {sk}
                        </Pill>
                      ))}
                </div>
              </Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}
