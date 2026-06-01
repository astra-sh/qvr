package ops

import "sort"

// HookInstaller is the optional second capability an adapter can
// implement: wiring Quiver into an agent's native hook mechanism (and
// tearing it back out). It is deliberately kept separate from Adapter so
// the hot parse path (Adapter.ParseEvent) carries no install/detect
// machinery, and so install-free adapters like `generic` are unaffected.
//
// Adapters that target a real agent implement BOTH ops.Adapter (for the
// `qvr _hook` funnel) and ops.HookInstaller (for `qvr audit install-hooks`)
// on the same struct — Name() is shared. The install commands enumerate
// installers via ListInstallers / GetInstaller, which type-assert over the
// adapter registry, so an adapter that only implements Adapter is simply
// skipped by the install tooling.
type HookInstaller interface {
	// Name returns the same dispatch key as Adapter.Name() — the two
	// must match so `qvr _hook <name>` and `qvr audit install-hooks
	// --agent <name>` address the same adapter.
	Name() string

	// DisplayName is the human-facing label (e.g. "Claude Code").
	DisplayName() string

	// Detect reports whether the agent is installed on this machine and
	// where its config lives. Detected=false is not an error.
	Detect() (DetectionResult, error)

	// Install wires `qvr _hook <name> <type>` into the agent's hook
	// config, backing up the original first. Idempotent: re-installing
	// an already-installed agent is a no-op unless Force is set.
	Install(opts InstallOptions) (InstallResult, error)

	// Uninstall removes Quiver's hooks, restoring from the newest backup
	// when available.
	Uninstall(opts UninstallOptions) (UninstallResult, error)

	// Status reports whether Quiver's hooks are currently present and
	// well-formed.
	Status() (HookStatus, error)
}

// DetectionResult is what Detect reports about an agent on this machine.
type DetectionResult struct {
	Detected   bool   `json:"detected"`
	ConfigPath string `json:"config_path,omitempty"`
	Version    string `json:"version,omitempty"`
	Message    string `json:"message,omitempty"`
}

// InstallOptions tunes a hook install.
type InstallOptions struct {
	DryRun bool `json:"dry_run,omitempty"`
	Force  bool `json:"force,omitempty"`
}

// InstallResult summarises what an install did.
type InstallResult struct {
	BackupPath string   `json:"backup_path,omitempty"`
	HooksAdded []string `json:"hooks_added,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// UninstallOptions tunes a hook uninstall.
type UninstallOptions struct {
	DryRun bool `json:"dry_run,omitempty"`
	Force  bool `json:"force,omitempty"`
}

// UninstallResult summarises what an uninstall did.
type UninstallResult struct {
	Restored     bool     `json:"restored"`
	HooksRemoved []string `json:"hooks_removed,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

// HookStatus reports the live state of an agent's Quiver hooks.
type HookStatus struct {
	Installed bool     `json:"installed"`
	Hooks     []string `json:"hooks,omitempty"`
	Valid     bool     `json:"valid"`
	Issues    []string `json:"issues,omitempty"`
}

// GetInstaller returns the registered adapter for name if it also
// implements HookInstaller. The bool is false when name is unregistered
// or registered but install-incapable (e.g. the generic adapter).
func GetInstaller(name string) (HookInstaller, bool) {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	a, ok := adapters[name]
	if !ok {
		return nil, false
	}
	inst, ok := a.(HookInstaller)
	return inst, ok
}

// ListInstallers returns every registered adapter that implements
// HookInstaller, sorted by name. Used by `qvr audit install-hooks` (no
// --agent) and `qvr audit status` to enumerate installable agents.
func ListInstallers() []HookInstaller {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	// Sort names first so output is deterministic.
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]HookInstaller, 0, len(names))
	for _, name := range names {
		if inst, ok := adapters[name].(HookInstaller); ok {
			out = append(out, inst)
		}
	}
	return out
}
