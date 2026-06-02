import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, useFetch, type Event } from "../api";
import {
  Card,
  CodeBlock,
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  Mono,
  PageHeader,
  Pill,
  prettyJSON,
  short,
  StatCard,
  StatusPill,
  Table,
  Td,
  Th,
} from "../components/ui";

export default function SessionDetail() {
  const { id = "" } = useParams();
  const { data, error, loading } = useFetch(() => api.session(id), `session:${id}`);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [showRaw, setShowRaw] = useState(false);

  const toggle = (eid: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      next.has(eid) ? next.delete(eid) : next.add(eid);
      return next;
    });

  return (
    <>
      <div className="mb-4">
        <Link to="/sessions" className="text-sm text-blue-600 hover:underline">
          ← Sessions
        </Link>
      </div>
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && (
        <>
          <PageHeader
            title={data.session.agent_name}
            subtitle={`${data.session.project_name || "no project"} · started ${fmtTime(
              data.session.started_at,
            )}`}
          />

          <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
            <StatCard label="Actions" value={data.session.total_actions} />
            <StatCard label="Files read" value={data.session.files_read} />
            <StatCard label="Files written" value={data.session.files_written} />
            <StatCard label="Commands" value={data.session.commands_executed} />
            <StatCard label="Errors" value={data.session.errors} />
            <StatCard label="Blocked" value={data.session.blocked_actions} />
            <StatCard label="Sensitive" value={data.session.sensitive_actions} />
            <StatCard
              label="Ended"
              value={data.session.ended_at ? fmtTime(data.session.ended_at) : "open"}
            />
          </div>

          {data.session.working_directory && (
            <div className="mt-4">
              <Card>
                <div className="text-xs text-gray-500">Working directory</div>
                <Mono>{data.session.working_directory}</Mono>
              </Card>
            </div>
          )}

          <div className="mt-8 mb-3 flex items-center justify-between">
            <h2 className="text-sm font-semibold text-gray-800">
              Event timeline ({data.events.length})
            </h2>
            <button
              type="button"
              onClick={() => setShowRaw((v) => !v)}
              className="rounded border border-gray-200 px-2 py-1 text-xs text-gray-600 hover:bg-gray-50"
            >
              {showRaw ? "Hide raw trace" : "Show raw trace"}
            </button>
          </div>

          {/* Full-session JSON trace — the lossless record the audit pipeline
              stored, surfaced verbatim so it can be copied into an eval or
              skill-evolution run. Folded by default to keep the timeline readable. */}
          {showRaw && (
            <div className="mb-4">
              <Card title="Raw session trace (JSON)">
                <CodeBlock value={prettyJSON(data.events)} label="audit_events" />
              </Card>
            </div>
          )}

          {data.events.length === 0 ? (
            <Empty>No events recorded for this session.</Empty>
          ) : (
            <Table
              head={
                <tr>
                  <Th> </Th>
                  <Th>#</Th>
                  <Th>Time</Th>
                  <Th>Action</Th>
                  <Th>Skill</Th>
                  <Th>Tool</Th>
                  <Th>Result</Th>
                </tr>
              }
            >
              {data.events.map((e) => {
                const open = expanded.has(e.id);
                return (
                  <EventRows key={e.id} e={e} open={open} onToggle={() => toggle(e.id)} />
                );
              })}
            </Table>
          )}
        </>
      )}
    </>
  );
}

// EventRows renders a single timeline row plus, when expanded, a detail row
// carrying that event's raw trace: the parsed payload, the unmodified hook
// bytes (raw_event), and any diff content. A clickable row toggles the detail.
function EventRows({ e, open, onToggle }: { e: Event; open: boolean; onToggle: () => void }) {
  const hasDetail =
    e.payload !== undefined ||
    e.raw_event !== undefined ||
    (e.diff_content ?? "") !== "";

  return (
    <>
      <tr
        className="cursor-pointer hover:bg-gray-50"
        role="button"
        tabIndex={0}
        onClick={onToggle}
        onKeyDown={(ev) => {
          if (ev.key === "Enter" || ev.key === " ") {
            ev.preventDefault();
            onToggle();
          }
        }}
        aria-expanded={open}
      >
        <Td className="text-gray-400">{hasDetail ? (open ? "▾" : "▸") : ""}</Td>
        <Td>{e.sequence}</Td>
        <Td>{fmtTime(e.timestamp)}</Td>
        <Td>
          <Mono>{e.action_type}</Mono>
        </Td>
        <Td>
          {e.skill_name}
          {e.skill_commit ? (
            <span className="ml-1 text-xs text-gray-400">({short(e.skill_commit)})</span>
          ) : null}
        </Td>
        <Td>{e.tool_name || "—"}</Td>
        <Td>
          <div className="flex items-center gap-1">
            <StatusPill value={e.result_status} />
            {e.is_sensitive && <Pill tone="amber">sensitive</Pill>}
          </div>
          {e.error_message && (
            <div className="mt-1 text-xs text-red-600">{e.error_message}</div>
          )}
        </Td>
      </tr>
      {open && (
        <tr className="bg-gray-50/60">
          <Td className="p-0"> </Td>
          <td colSpan={6} className="px-4 pb-4 pt-0">
            {!hasDetail && (
              <div className="py-2 text-xs text-gray-400">
                No payload captured for this event (logging level may be{" "}
                <Mono>minimal</Mono>).
              </div>
            )}
            {e.payload !== undefined && (
              <CodeBlock value={prettyJSON(e.payload)} label="payload" />
            )}
            {e.raw_event !== undefined && (
              <CodeBlock value={prettyJSON(e.raw_event)} label="raw_event (hook bytes)" />
            )}
            {(e.diff_content ?? "") !== "" && (
              <CodeBlock value={e.diff_content as string} label="diff" />
            )}
          </td>
        </tr>
      )}
    </>
  );
}
