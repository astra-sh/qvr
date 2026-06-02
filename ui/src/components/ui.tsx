import { useState, type ReactNode } from "react";

// Shared presentational primitives: status pills, stat cards, mono text, table
// shells, loading/error/empty states. Kept tiny and dependency-free.

type Tone = "green" | "red" | "amber" | "gray" | "blue";

const toneClasses: Record<Tone, string> = {
  green: "bg-emerald-100 text-emerald-800 ring-emerald-600/20",
  red: "bg-red-100 text-red-800 ring-red-600/20",
  amber: "bg-amber-100 text-amber-800 ring-amber-600/20",
  gray: "bg-gray-100 text-gray-700 ring-gray-500/20",
  blue: "bg-blue-100 text-blue-800 ring-blue-600/20",
};

// toneFor maps the various status vocabularies (result_status, scan decision,
// signature status, enabled/disabled) onto a colour.
export function toneFor(value?: string): Tone {
  switch ((value ?? "").toLowerCase()) {
    case "success":
    case "allowed":
    case "verified":
    case "enabled":
    case "passed":
      return "green";
    case "critical":
    case "error":
    case "blocked":
    case "invalid":
    case "failed":
      return "red";
    case "warning":
      return "amber";
    case "skipped":
    case "none":
    case "unscanned":
    case "disabled":
    case "":
      return "gray";
    default:
      return "blue";
  }
}

export function Pill({ children, tone }: { children: ReactNode; tone?: Tone }) {
  return (
    <span
      className={`inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${
        toneClasses[tone ?? "gray"]
      }`}
    >
      {children}
    </span>
  );
}

export function StatusPill({ value }: { value?: string }) {
  return <Pill tone={toneFor(value)}>{value || "—"}</Pill>;
}

export function StatCard({
  label,
  value,
  sub,
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
}) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white px-5 py-4 shadow-sm">
      <div className="text-xs font-medium uppercase tracking-wide text-gray-500">{label}</div>
      <div className="mt-1 text-2xl font-semibold text-gray-900">{value}</div>
      {sub != null && <div className="mt-0.5 text-xs text-gray-500">{sub}</div>}
    </div>
  );
}

export function Mono({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span title={title} className="font-mono text-[0.8125rem] text-gray-700">
      {children}
    </span>
  );
}

export function short(sha?: string, n = 7): string {
  if (!sha) return "—";
  const body = sha.startsWith("sha256:") ? sha.slice(7) : sha;
  return body.length > n ? body.slice(0, n) : body;
}

export function Card({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <div className="rounded-xl border border-gray-200 bg-white shadow-sm">
      {title && (
        <div className="border-b border-gray-100 px-5 py-3 text-sm font-semibold text-gray-800">
          {title}
        </div>
      )}
      <div className="p-5">{children}</div>
    </div>
  );
}

export function Table({
  head,
  children,
}: {
  head: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white shadow-sm">
      <table className="min-w-full divide-y divide-gray-200 text-sm">
        <thead className="bg-gray-50 text-left text-xs font-semibold uppercase tracking-wide text-gray-500">
          {head}
        </thead>
        <tbody className="divide-y divide-gray-100">{children}</tbody>
      </table>
    </div>
  );
}

export function Th({ children }: { children: ReactNode }) {
  return <th className="px-4 py-2.5 font-semibold">{children}</th>;
}

export function Td({
  children,
  className,
  title,
}: {
  children: ReactNode;
  className?: string;
  title?: string;
}) {
  return (
    <td title={title} className={`px-4 py-2.5 align-top text-gray-700 ${className ?? ""}`}>
      {children}
    </td>
  );
}

export function Loading() {
  return <div className="py-12 text-center text-sm text-gray-400">Loading…</div>;
}

export function ErrorBox({ message }: { message: string }) {
  return (
    <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
      {message}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="py-12 text-center text-sm text-gray-400">{children}</div>;
}

export function PageHeader({ title, subtitle }: { title: string; subtitle?: string }) {
  return (
    <div className="mb-6">
      <h1 className="text-xl font-semibold text-gray-900">{title}</h1>
      {subtitle && <p className="mt-1 text-sm text-gray-500">{subtitle}</p>}
    </div>
  );
}

// CopyButton copies value to the clipboard and flashes "copied" briefly. Used
// alongside raw-trace blocks so a session's JSON can be lifted out for an eval
// or skill-evolution pipeline without a round-trip through the DB.
export function CopyButton({ value }: { value: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        });
      }}
      className="rounded border border-gray-200 px-2 py-0.5 text-xs text-gray-500 hover:bg-gray-50"
    >
      {done ? "copied" : "copy"}
    </button>
  );
}

// CodeBlock renders preformatted text (typically pretty-printed JSON) in a
// scrollable mono panel, with an optional label row carrying a copy button.
export function CodeBlock({ value, label }: { value: string; label?: string }) {
  return (
    <div className="mt-2">
      {label && (
        <div className="mb-1 flex items-center justify-between">
          <span className="text-xs font-medium uppercase tracking-wide text-gray-400">{label}</span>
          <CopyButton value={value} />
        </div>
      )}
      <pre className="max-h-96 overflow-auto whitespace-pre rounded-lg border border-gray-200 bg-gray-50 p-3 font-mono text-xs leading-relaxed text-gray-700">
        {value}
      </pre>
    </div>
  );
}

// prettyJSON stringifies an arbitrary JSON value with 2-space indent, falling
// back to String() on the rare circular/unstringifiable input.
export function prettyJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

export function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}
