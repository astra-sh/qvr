package ops

import "sort"

// HookInstaller wires Quiver into an agent's native hook mechanism (and tears
// it back out). It is the sole capability the registry tracks: in the raw-only
// model capture is parser-free (the hook tails the transcript verbatim), so a
// per-agent package only needs to know how to install/remove/detect its hooks.
// The install commands enumerate installers via ListInstallers / GetInstaller.
type HookInstaller interface {
	// Name returns the dispatch key — it must match the `qvr _hook <name>`
	// argument and `qvr audit install-hooks --agent <name>`.
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
func GetInstaller(name string) (HookInstaller, bool) {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	h, ok := installers[name]
	return h, ok
}

// ListInstallers returns every registered installer, sorted by name. Used by
// `qvr audit install-hooks` (no --agent) and `qvr audit status` to enumerate
// installable agents.
func ListInstallers() []HookInstaller {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	names := make([]string, 0, len(installers))
	for name := range installers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]HookInstaller, 0, len(names))
	for _, name := range names {
		out = append(out, installers[name])
	}
	return out
}
