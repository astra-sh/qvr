import { useEffect, useRef, useState } from "react";
import { Check, ChevronDown } from "lucide-react";

// MultiSelect — a checklist filter in DS chrome: a select-styled trigger that
// opens a popover of checkboxes. Selection is a string[]; an empty array means
// "no filter" (the trigger reads the placeholder, e.g. "all"). Closes on outside
// click or Escape. Used for the Sessions screen's skill + version filters, where
// several values OR together server-side.
export function MultiSelect({
  options,
  selected,
  onChange,
  placeholder = "all",
  noun = "selected",
  emptyText = "no options",
}: {
  options: string[];
  selected: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
  // Pluralised in the summary chip, e.g. "3 versions".
  noun?: string;
  emptyText?: string;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click / Escape while open.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDoc);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const toggle = (opt: string) =>
    onChange(selected.includes(opt) ? selected.filter((s) => s !== opt) : [...selected, opt]);

  const label =
    selected.length === 0
      ? placeholder
      : selected.length === 1
        ? selected[0]
        : `${selected.length} ${noun}`;

  return (
    <div className="qvr-multi" ref={ref}>
      <button
        type="button"
        className={"qvr-multi__trigger" + (selected.length ? " qvr-multi__trigger--active" : "")}
        onClick={() => setOpen((v) => !v)}
        aria-haspopup="true"
        aria-expanded={open}
      >
        <span className="qvr-multi__label">{label}</span>
        <ChevronDown size={14} />
      </button>
      {open && (
        <div className="qvr-multi__pop">
          {selected.length > 0 && (
            <button type="button" className="qvr-multi__clear" onClick={() => onChange([])}>
              clear ({selected.length})
            </button>
          )}
          <div className="qvr-multi__list" role="group" aria-label={`${noun} filter`}>
            {options.length === 0 ? (
              <p className="qvr-multi__empty">{emptyText}</p>
            ) : (
              options.map((opt) => (
                <label key={opt} className="qvr-check qvr-multi__opt">
                  <input
                    type="checkbox"
                    checked={selected.includes(opt)}
                    onChange={() => toggle(opt)}
                  />
                  <span className="qvr-check__box">
                    <Check />
                  </span>
                  <span className="qvr-multi__opt-label" title={opt}>
                    {opt}
                  </span>
                </label>
              ))
            )}
          </div>
        </div>
      )}
    </div>
  );
}
