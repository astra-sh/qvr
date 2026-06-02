import { useEffect, useRef, useState } from "react";
import { api, useFetch, type ProjectSummary, type Scope } from "../api";

// ScopeSwitcher is the project dropdown in the sidebar header. It lists Quiver's
// known projects (from /api/projects) plus a Global entry, and rescopes every
// page when one is selected. The parent owns the active Scope and the remount
// token; this component only renders the menu and reports a new selection.
export default function ScopeSwitcher({
  scope,
  onChange,
}: {
  scope: Scope;
  onChange: (s: Scope) => void;
}) {
  const { data } = useFetch(api.projects, "projects");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click.
  useEffect(() => {
    function onDoc(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, []);

  const projects = data ?? [];
  const label = activeLabel(scope, projects);

  function pick(s: Scope) {
    onChange(s);
    setOpen(false);
  }

  return (
    <div ref={ref} className="relative px-3 pb-3">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center justify-between rounded-lg border border-gray-700 bg-gray-800/60 px-3 py-2 text-left text-sm text-gray-200 hover:bg-gray-800"
      >
        <span className="truncate">
          <span className="text-[0.625rem] uppercase tracking-wide text-gray-500">scope</span>
          <br />
          <span className="font-medium">{label}</span>
        </span>
        <span className="ml-2 shrink-0 text-gray-500">▾</span>
      </button>
      {open && (
        <div className="absolute left-3 right-3 z-10 mt-1 max-h-80 overflow-auto rounded-lg border border-gray-700 bg-gray-900 py-1 shadow-xl">
          {projects.map((p) => {
            const sel: Scope = p.scope === "global" ? { scope: "global" } : { project: p.path };
            return (
              <button
                key={p.scope === "global" ? "__global__" : p.path}
                onClick={() => pick(sel)}
                className={`block w-full px-3 py-2 text-left text-sm hover:bg-gray-800 ${
                  isActive(scope, p) ? "bg-gray-800 text-white" : "text-gray-300"
                }`}
                title={p.path || "all projects"}
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="truncate font-medium">
                    {p.name}
                    {p.current && p.scope === "project" && (
                      <span className="ml-1 text-[0.625rem] text-blue-400">· here</span>
                    )}
                  </span>
                  {!p.hasLock && p.scope === "project" && (
                    <span className="shrink-0 text-[0.625rem] text-gray-500">no lock</span>
                  )}
                </div>
                <div className="mt-0.5 truncate text-[0.6875rem] text-gray-500">
                  {p.scope === "global" ? "all projects" : p.path}
                </div>
                <div className="mt-0.5 text-[0.625rem] text-gray-500">
                  {p.skills} skills · {p.sessions} traces
                </div>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

// isActive reports whether project row p matches the current scope. The default
// scope (empty) resolves to the launch project (p.current).
function isActive(scope: Scope, p: ProjectSummary): boolean {
  if (scope.scope) return p.scope === scope.scope;
  if (scope.project) return p.path === scope.project;
  return p.scope === "project" && p.current;
}

function activeLabel(scope: Scope, projects: ProjectSummary[]): string {
  if (scope.scope === "global") return "Global";
  if (scope.scope === "all") return "All projects";
  const match = projects.find((p) => isActive(scope, p));
  if (match) return match.name;
  return scope.project ? scope.project.split("/").pop() || scope.project : "This project";
}
