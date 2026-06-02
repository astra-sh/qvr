import { Link } from "react-router-dom";
import { api, useFetch } from "../api";
import { Empty, ErrorBox, Loading, Mono, PageHeader, Pill, short } from "../components/ui";

export default function Tree() {
  const { data, error, loading } = useFetch(api.tree, "tree");

  return (
    <>
      <PageHeader
        title="Tree"
        subtitle="Registry → skill → target. The uv-tree-style home screen."
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && <Empty>No installed skills.</Empty>}
      {data && data.length > 0 && (
        <div className="space-y-6">
          {data.map((g, gi) => (
            <div
              key={`${g.scope ?? ""}-${g.registry}-${gi}`}
              className="rounded-xl border border-gray-200 bg-white p-5 shadow-sm"
            >
              <div className="mb-3 flex items-center gap-2">
                {g.scope && <Pill tone="gray">{g.scope}</Pill>}
                <span className="font-semibold text-gray-900">{g.registry}</span>
              </div>
              <ul className="space-y-2">
                {g.skills.map((s) => (
                  <li key={s.name} className="border-l-2 border-gray-100 pl-4">
                    <div className="flex flex-wrap items-center gap-2">
                      <Link
                        to={`/skills/${encodeURIComponent(s.name)}`}
                        className="font-medium text-blue-600 hover:underline"
                      >
                        {s.name}
                      </Link>
                      <span className="text-xs text-gray-400">
                        @{s.ref} <Mono>({short(s.commit)})</Mono>
                      </span>
                      {s.mode && <Pill tone="amber">{s.mode}</Pill>}
                      {s.disabled && <Pill tone="gray">disabled</Pill>}
                    </div>
                    <div className="mt-1 flex flex-wrap gap-1 pl-3">
                      {s.targets.length === 0 ? (
                        <span className="text-xs text-gray-400">no targets</span>
                      ) : (
                        s.targets.map((t) => (
                          <Pill key={t} tone="blue">
                            {t}
                          </Pill>
                        ))
                      )}
                    </div>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </>
  );
}
