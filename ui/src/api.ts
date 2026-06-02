// Typed client for the qvr ui JSON API. Types mirror the Go structs exactly
// (cmd/ui_server.go and the reused command structs) so the dashboard stays a
// faithful view of CLI truth — if a field changes in Go, it changes here.

import { useCallback, useEffect, useState } from "react";

export interface Session {
  id: string;
  agent_session_id?: string;
  agent_name: string;
  started_at: string;
  ended_at?: string;
  working_directory?: string;
  project_name?: string;
  total_actions: number;
  files_read: number;
  files_written: number;
  commands_executed: number;
  errors: number;
  sensitive_actions: number;
  blocked_actions: number;
  skills_touched?: string[];
  end_reason?: string;
}

export interface Event {
  id: string;
  session_id: string;
  agent_session_id?: string;
  sequence: number;
  timestamp: string;
  duration_ms?: number;
  agent_name: string;
  working_directory?: string;
  skill_name: string;
  skill_registry?: string;
  skill_commit?: string;
  skill_path?: string;
  action_type: string;
  tool_name?: string;
  result_status: string;
  error_message?: string;
  is_sensitive: boolean;
  // The structured trace the funnel parsed (tool inputs/outputs) and the
  // unmodified hook bytes as the agent emitted them. Both are Go json.RawMessage,
  // so they arrive as inline JSON (object/array/scalar), not strings. Present at
  // logging levels standard/full; null/absent under minimal. These are the
  // foundation for trace-driven skill evolution — keep them lossless.
  payload?: unknown;
  diff_content?: string;
  raw_event?: unknown;
  subagent_id?: string;
  subagent_type?: string;
}

export interface SessionDetail {
  session: Session;
  events: Event[];
}

export interface Overview {
  audit_enabled: boolean;
  // Lens the whole payload (including sessions/events) is taken through:
  // "project" | "global" | "all". project_root is set in project scope.
  scope: string;
  project_root?: string;
  sessions: number;
  events: number;
  skills: number;
  registries: number;
  gate_allowed: number;
  gate_blocked: number;
  gate_unscanned: number;
  recent_sessions: Session[];
}

export interface SkillRow {
  name: string;
  worktree?: string;
  scope?: string;
  registry?: string;
  ref?: string;
  commit?: string;
  source?: string;
  mode?: string;
  disabled?: boolean;
  targets?: string[];
}

export interface TargetDetail {
  target: string;
  path: string;
  ok: boolean;
  error?: string;
}

export interface SkillInfo {
  name: string;
  description?: string;
  license?: string;
  compatibility?: string;
  allowedTools?: string;
  metadata?: Record<string, string>;
  registry?: string;
  ref?: string;
  commit?: string;
  commitDrift?: string;
  worktree?: string;
  source?: string;
  sourceUpstream?: string;
  subtreeHash?: string;
  treeOID?: string;
  mode?: string;
  editPath?: string;
  installedAt?: string;
  targets?: string[];
  targetDetails?: TargetDetail[];
  files?: string[];
}

export interface TreeSkill {
  name: string;
  ref: string;
  commit?: string;
  mode?: string;
  disabled?: boolean;
  targets: string[];
}

export interface TreeGroup {
  scope?: string;
  registry: string;
  skills: TreeSkill[];
}

export interface ProvenanceView {
  name: string;
  source?: string;
  subdirectory?: string;
  requested?: string;
  resolved?: string;
  treeOID?: string;
  subtreeHash?: string;
  signatureStatus: string;
  signer?: string;
  signedRef?: string;
  scanDecision?: string;
  scannerVersion?: string;
  install: string;
  status: string;
}

export interface ScanSummaryRow {
  name: string;
  registry?: string;
  decision?: string;
  scannerVersion?: string;
  mode?: string;
}

// Mirrors model.RegistryStatus (embeds model.Registry). Registries are global —
// configured once at the Quiver home root and shared across every project.
export interface RegistryStatus {
  name: string;
  url: string;
  path?: string;
  skill_count: number;
  skipped_count?: number;
  last_fetched: string;
  default_branch?: string;
  has_upstream_changes?: boolean;
  error?: string;
}

// One row in the project switcher (Go: projectSummary). Sourced from Quiver's
// on-disk project index plus a synthetic Global entry.
export interface ProjectSummary {
  path: string;
  name: string;
  scope: "project" | "global";
  lockPath?: string;
  hasLock: boolean;
  current: boolean;
  skills: number;
  sessions: number;
  events: number;
  lastSeen?: string;
}

export interface Finding {
  check: string;
  rule_id?: string;
  category?: string;
  severity: string;
  confidence?: number;
  file?: string;
  line?: number;
  message: string;
  remediation?: string;
}

export interface ScanResult {
  path: string;
  skill: string;
  scanned_at?: string;
  checks: string[];
  findings: Finding[];
  summary: {
    critical: number;
    error: number;
    warning: number;
    info: number;
  };
}

// Lock-scale severity counts (matches model.SeverityCounts / the recorded
// gate's verification.scan.counts), so a live re-scan compares 1:1.
export interface SeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  info: number;
}

// Live re-scan verdict, computed under the recorded gate's block_severity
// policy and reported on the lock's 5-rung scale (issue #140).
export interface ScanRunGate {
  decision: string; // allowed | blocked
  threshold: string;
  counts: SeverityCounts;
}

export interface ScanRunResult extends ScanResult {
  gate: ScanRunGate;
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body?.error) msg = body.error;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

async function postJSON<T>(url: string): Promise<T> {
  const res = await fetch(url, { method: "POST" });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body?.error) msg = body.error;
    } catch {
      /* ignore */
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

// ---- scope (project switcher) ----------------------------------------------
// The dashboard answers every scoped endpoint through the selected project (or
// Global). The choice lives here as module state, persisted to localStorage, so
// a single running `qvr ui` can browse any project on the machine. Registries
// and the project list are global and are NOT scoped.

export interface Scope {
  project?: string; // absolute project root
  scope?: "global" | "all";
}

const SCOPE_STORAGE_KEY = "qvr-ui-scope";

function readStoredScope(): Scope {
  try {
    const raw = localStorage.getItem(SCOPE_STORAGE_KEY);
    if (raw) return JSON.parse(raw) as Scope;
  } catch {
    /* ignore malformed/absent storage */
  }
  return {};
}

let currentScope: Scope = readStoredScope();

export function getScope(): Scope {
  return currentScope;
}

export function setScope(s: Scope): void {
  currentScope = s;
  try {
    localStorage.setItem(SCOPE_STORAGE_KEY, JSON.stringify(s));
  } catch {
    /* non-fatal: scope just won't persist across reloads */
  }
}

// scopeToken is a stable string for useFetch keys / Routes remount, so switching
// scope re-runs every loader.
export function scopeToken(s: Scope = currentScope): string {
  if (s.scope) return s.scope;
  if (s.project) return `p:${s.project}`;
  return "default";
}

function scopeQuery(): string {
  const p = new URLSearchParams();
  if (currentScope.scope) p.set("scope", currentScope.scope);
  else if (currentScope.project) p.set("project", currentScope.project);
  const q = p.toString();
  return q ? `?${q}` : "";
}

export const api = {
  // Scoped endpoints carry the active project/scope.
  overview: () => getJSON<Overview>(`/api/overview${scopeQuery()}`),
  sessions: () => getJSON<Session[]>(`/api/sessions${scopeQuery()}`),
  session: (id: string) => getJSON<SessionDetail>(`/api/sessions/${id}`),
  skills: () => getJSON<SkillRow[]>(`/api/skills${scopeQuery()}`),
  skill: (name: string) =>
    getJSON<SkillInfo>(`/api/skills/${encodeURIComponent(name)}${scopeQuery()}`),
  tree: () => getJSON<TreeGroup[]>(`/api/tree${scopeQuery()}`),
  provenance: () => getJSON<ProvenanceView[]>(`/api/provenance${scopeQuery()}`),
  scanSummary: () => getJSON<ScanSummaryRow[]>(`/api/scan${scopeQuery()}`),
  runScan: (name: string) =>
    postJSON<ScanRunResult>(`/api/scan/${encodeURIComponent(name)}${scopeQuery()}`),
  // Global endpoints — not scoped.
  registries: () => getJSON<RegistryStatus[]>("/api/registries"),
  projects: () => getJSON<ProjectSummary[]>("/api/projects"),
};

export interface AsyncState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  reload: () => void;
}

// useFetch runs an async loader on mount (and when `key` changes) and tracks
// loading/error state. Returns a reload() for manual refresh.
export function useFetch<T>(loader: () => Promise<T>, key: string): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    loader()
      .then((d) => {
        if (!cancelled) setData(d);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // loader identity changes per render; key is the real dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, nonce]);

  return { data, error, loading, reload };
}
