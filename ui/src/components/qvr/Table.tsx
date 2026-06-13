import type { ReactNode } from "react";

// Table — mono data table: hairline row dividers, uppercase micro-headers,
// raised-row hover. Same head/children seam as the old shell so pages convert
// mechanically.
export function Table({ head, children }: { head: ReactNode; children: ReactNode }) {
  return (
    <div className="qvr-card" style={{ overflowX: "auto" }}>
      <table className="qvr-table">
        <thead>{head}</thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// Th — column header; pass onSort to make it clickable (a button in the same
// uppercase micro-header style), with active="desc" rendering the ↓ marker.
export function Th({
  children,
  onSort,
  sortActive = false,
}: {
  children?: ReactNode;
  onSort?: () => void;
  sortActive?: boolean;
}) {
  if (!onSort) return <th scope="col">{children}</th>;
  return (
    <th scope="col" aria-sort={sortActive ? "descending" : "none"}>
      <button type="button" className="qvr-table__sort" onClick={onSort}>
        {children}
        {sortActive ? " ↓" : ""}
      </button>
    </th>
  );
}

export function Td({
  children,
  className,
  title,
  muted = false,
}: {
  children: ReactNode;
  className?: string;
  title?: string;
  muted?: boolean;
}) {
  const cls = [muted ? "qvr-table__muted" : "", className ?? ""].filter(Boolean).join(" ");
  return (
    <td title={title} className={cls || undefined}>
      {children}
    </td>
  );
}
