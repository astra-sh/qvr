package cmd

import (
	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/registry"
)

// newRegistryManager wires a Manager with the configured cache TTL applied.
// All cmd-layer callers should use this instead of registry.NewManager so the
// `cache.index_ttl` config setting actually takes effect on Index reads
// (issue #46). Unparseable / unset TTLs silently fall back to the default —
// surfacing a config error here would force every command to handle it,
// which would be noise for a knob with a sane default.
func newRegistryManager(gc git.GitClient) *registry.Manager {
	mgr := registry.NewManager(gc)
	if cfg, err := config.Load(); err == nil {
		if ttl, perr := config.ParseCacheTTL(cfg.Cache.IndexTTL); perr == nil {
			mgr.CacheTTL = ttl
		}
	}
	return mgr
}

// refreshAllIndexes invalidates the cached index for every configured
// registry. The next read goes through Manager.Index, which rebuilds from
// the bare clone and writes a fresh cache file back. Used by `--refresh`
// on the read commands so users can force a local rebuild without going
// to the network (which `qvr registry update` would).
//
// Errors on individual invalidations are swallowed — the next read will
// rebuild regardless, so a failed delete just means the cache might still
// be served (one more time) before the rebuild fires from the TTL/commit
// pin path.
func refreshAllIndexes() {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	for name := range cfg.Registries {
		_ = registry.Invalidate(name)
	}
}
