import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, useFetch, type RegistryVersion } from "../api";
import {
  Card,
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  Mono,
  PageHeader,
  Pill,
  short,
} from "../components/ui";

export default function RegistryDetail() {
  const { name = "" } = useParams();
  const { data, error, loading } = useFetch(
    () => api.registrySkills(name),
    `registry:${name}`,
  );

  return (
    <>
      <div className="mb-4">
        <Link to="/registries" className="text-sm text-blue-600 hover:underline">
          ← Registries
        </Link>
      </div>
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && (
        <>
          <PageHeader
            title={data.registry}
            subtitle={data.url || "Git repository Quiver installs skills from."}
          />
          {data.error && <ErrorBox message={data.error} />}

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            {/* Repo-level version tree: every branch & tag with its commit and
                timestamp. The default branch is marked current. */}
            <Card
              title={`Version tree (${data.versions.length})`}
            >
              {data.versions.length === 0 ? (
                <Empty>No branches or tags found in this registry's clone.</Empty>
              ) : (
                <VersionTree versions={data.versions} />
              )}
              {data.defaultBranch && (
                <p className="mt-3 text-xs text-gray-400">
                  Default branch: <Mono>{data.defaultBranch}</Mono>. Each skill below
                  expands to show which version is installed.
                </p>
              )}
            </Card>

            {/* Skills the registry offers, with install status. */}
            <Card title={`Skills (${data.skills.length})`}>
              {data.skills.length === 0 ? (
                <Empty>This registry has no indexable skills.</Empty>
              ) : (
                <ul className="divide-y divide-gray-100">
                  {data.skills.map((s) => (
                    <SkillItem key={s.name} skill={s} versions={data.versions} />
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

// VersionTree renders refs as a timeline. `currentRef` (the installed/default
// ref) is highlighted; `currentSha` additionally flags the exact installed
// commit even if it was pinned by SHA rather than ref name.
function VersionTree({
  versions,
  currentRef,
  currentSha,
}: {
  versions: RegistryVersion[];
  currentRef?: string;
  currentSha?: string;
}) {
  return (
    <ul className="space-y-1.5">
      {versions.map((v) => {
        const isCurrent =
          (currentRef && v.ref === currentRef) ||
          (currentSha && v.sha === currentSha) ||
          (!currentRef && !currentSha && v.current);
        return (
          <li
            key={`${v.isTag ? "tag" : "branch"}:${v.ref}`}
            className={`flex flex-wrap items-center gap-2 rounded-md px-2 py-1.5 ${
              isCurrent ? "bg-emerald-50 ring-1 ring-inset ring-emerald-200" : ""
            }`}
          >
            <Pill tone={v.isTag ? "amber" : "blue"}>{v.isTag ? "tag" : "branch"}</Pill>
            <span className={`font-medium ${isCurrent ? "text-emerald-800" : "text-gray-800"}`}>
              {v.ref}
            </span>
            {isCurrent && <Pill tone="green">current</Pill>}
            <Mono title={v.sha}>{short(v.sha)}</Mono>
            {v.time && !v.time.startsWith("0001") && (
              <span className="text-xs text-gray-400">{fmtTime(v.time)}</span>
            )}
            {v.subject && (
              <span className="w-full truncate pl-1 text-xs text-gray-400">{v.subject}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

function SkillItem({
  skill,
  versions,
}: {
  skill: import("../api").RegistrySkillRow;
  versions: RegistryVersion[];
}) {
  const [open, setOpen] = useState(false);
  return (
    <li className="py-2">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 text-left"
      >
        <span className="text-gray-400">{open ? "▾" : "▸"}</span>
        <span className="font-medium text-gray-800">{skill.name}</span>
        {skill.installed ? (
          <Pill tone="green">
            installed{skill.installedRef ? ` @ ${skill.installedRef}` : ""}
          </Pill>
        ) : (
          <Pill tone="gray">available</Pill>
        )}
        {skill.installed && skill.installedCommit && (
          <Mono title={skill.installedCommit}>{short(skill.installedCommit)}</Mono>
        )}
      </button>
      {skill.description && (
        <p className="mt-0.5 pl-6 text-sm text-gray-500">{skill.description}</p>
      )}
      {open && (
        <div className="mt-2 pl-6">
          <div className="mb-1 text-xs font-medium uppercase tracking-wide text-gray-400">
            Versions {skill.installed ? "(installed one highlighted)" : ""}
          </div>
          <VersionTree
            versions={versions}
            currentRef={skill.installedRef}
            currentSha={skill.installedCommit}
          />
        </div>
      )}
    </li>
  );
}
