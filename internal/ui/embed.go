// Package ui embeds the built React dashboard so `qvr ui` can serve it from a
// single binary with zero external assets. The React source lives at the repo
// root under ui/ and Vite builds into internal/ui/dist (see vite.config.ts and
// the `make ui` target).
//
// The build is deliberately decoupled from `go build`: only a committed
// dist/.gitkeep is required for compilation, so contributors and CI without
// Node still build and test. When the real bundle hasn't been built, HasIndex
// reports false and the server falls back to a "run make ui" stub page.
package ui

import (
	"embed"
	"io/fs"
)

// assets holds the built dashboard. The `all:` prefix is required so the
// committed dist/.gitkeep (a dotfile) is matched — a plain //go:embed dist
// excludes dotfiles and would fail to compile against an otherwise-empty dir
// before the first `make ui`.
//
//go:embed all:dist
var assets embed.FS

// Dist returns the embedded filesystem rooted at the dist directory, so callers
// serve "index.html" rather than "dist/index.html".
func Dist() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		// Unreachable: dist is embedded at compile time via the directive
		// above, so fs.Sub always resolves. Return the unscoped FS rather
		// than panicking in a library function.
		return assets
	}
	return sub
}

// HasIndex reports whether a real built bundle is present (index.html exists in
// the embedded dist). False before the first `make ui`, in which case the
// server serves a friendly stub instead of a blank page.
func HasIndex() bool {
	_, err := fs.Stat(Dist(), "index.html")
	return err == nil
}
