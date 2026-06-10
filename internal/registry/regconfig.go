package registry

import (
	"fmt"
	"path"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
)

// RegistryFile is the parsed shape of a repo's registry manifest: the
// [registry] table in the repo's top-level qvr.toml. qvr.toml is dual-intent
// — [project]/[skills] declare what the repo consumes, [registry] declares
// how the repo is indexed when published as a registry.
//
// When present, skill discovery is scoped to SkillsDir (plus Ignore
// filtering) instead of walking the whole tree, so fixtures, vendored code,
// and test data never reach the consumer surface (#244). Parsing is
// deliberately loose — the table is read from untrusted remote HEADs, so
// only these keys are decoded and anything else is accepted and ignored.
//
// The manifest sits at the bottom of qvr's artifact hierarchy: qvr.lock is
// the only file reproducibility needs, qvr.toml is the optional intent
// layer, and [registry] is optional within that — read only when other
// people's qvr indexes this repo as a registry.
type RegistryFile struct {
	Name string `toml:"name"`
	// SkillsDir is the repo-relative directory skills live under. Defaults to
	// "skills". The repo root itself ("." ) is always allowed too, so a
	// single-skill root-layout repo can still carry a manifest.
	SkillsDir string `toml:"skills-dir"`
	// Ignore lists path.Match globs evaluated against each candidate skill
	// directory (repo-relative, slash-separated); matches are skipped.
	Ignore []string `toml:"ignore"`
}

// normalize applies the manifest's defaults.
func (rf *RegistryFile) normalize() {
	if rf.SkillsDir == "" {
		rf.SkillsDir = "skills"
	}
	rf.SkillsDir = strings.Trim(path.Clean(rf.SkillsDir), "/")
}

// loadRegistryManifest resolves the repo's registry manifest — the
// [registry] table of qvr.toml at the bare repo's HEAD. Returns (nil, skips)
// when no scope is defined — whole-tree discovery applies. An unparsable
// qvr.toml never silently mis-scopes: the parse failure comes back as an
// informational skip and discovery stays whole-tree.
func loadRegistryManifest(gc git.GitClient, repoPath string) (*RegistryFile, []SkippedSkill) {
	var skipped []SkippedSkill
	rf, err := loadRegistryFromProjectFile(gc, repoPath)
	if err != nil {
		skipped = append(skipped, SkippedSkill{
			Name:   model.ProjectFileName,
			Path:   model.ProjectFileName,
			Reason: fmt.Sprintf("unparsable — any [registry] table not applied, whole-tree discovery used: %v", err),
		})
		return nil, skipped
	}
	return rf, skipped
}

// loadRegistryFromProjectFile reads the [registry] table from the repo's
// top-level qvr.toml at HEAD. Returns (nil, nil) when the file is absent or
// carries no [registry] table — a consumer-only qvr.toml is inert here, so
// the projects that commit one purely to declare installed skills never
// index differently for having it.
func loadRegistryFromProjectFile(gc git.GitClient, repoPath string) (*RegistryFile, error) {
	data, ok := readManifestBlob(gc, repoPath, model.ProjectFileName)
	if !ok {
		return nil, nil
	}
	// Decode only the [registry] table — qvr.toml comes from an untrusted
	// remote HEAD, so the consumer-side schema ([project], [skills], …) is
	// deliberately not parsed here.
	var doc struct {
		Registry *RegistryFile `toml:"registry"`
	}
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", model.ProjectFileName, err)
	}
	if doc.Registry == nil {
		return nil, nil
	}
	doc.Registry.normalize()
	return doc.Registry, nil
}

// readManifestBlob fetches a root-level manifest blob at HEAD. A missing file
// or unreadable repo state reads as absent — BuildIndex already tolerates
// unreadable repos, and absence just means the next resolution step applies.
func readManifestBlob(gc git.GitClient, repoPath, name string) ([]byte, bool) {
	data, err := gc.ReadBlob(repoPath, "HEAD", name)
	if err != nil {
		return nil, false
	}
	return data, true
}

// applyRegistryScope filters candidate skill dirs to the manifest's SkillsDir
// (the repo root "." stays eligible) and drops Ignore-glob matches. Excluded
// dirs come back as informational skips so `registry add` never reports a
// silent 0-skill mystery.
func applyRegistryScope(rf *RegistryFile, skillDirs []string) (kept []string, skipped []SkippedSkill) {
	for _, d := range skillDirs {
		switch {
		case ignoreGlobMatch(rf.Ignore, d):
			skipped = append(skipped, SkippedSkill{
				Name:   path.Base(d),
				Path:   d,
				Reason: "ignored by a qvr.toml [registry] ignore pattern",
			})
		case rf.SkillsDir == "." || d == "." || d == rf.SkillsDir || strings.HasPrefix(d, rf.SkillsDir+"/"):
			kept = append(kept, d)
		default:
			skipped = append(skipped, SkippedSkill{
				Name:   path.Base(d),
				Path:   d,
				Reason: fmt.Sprintf("outside skills-dir %q (qvr.toml [registry])", rf.SkillsDir),
			})
		}
	}
	return kept, skipped
}

// ignoreGlobMatch reports whether dir matches any of the manifest's ignore
// globs, evaluated with path.Match against the repo-relative directory.
func ignoreGlobMatch(globs []string, dir string) bool {
	for _, g := range globs {
		if ok, err := path.Match(g, dir); err == nil && ok {
			return true
		}
	}
	return false
}

// fixturePathSegments are directory names that mark test fixtures rather than
// consumable skills. Skill dirs under any of these segments are always
// excluded from indexing — a repo's scanner fixtures (deliberately malicious
// SKILL.md files) must never show up in search/install (#244).
var fixturePathSegments = map[string]bool{
	"testdata": true,
	"fixtures": true,
}

// excludeFixturePaths drops skill dirs that live under a testdata/ or
// fixtures/ path segment at any depth, surfacing each as an informational
// skip.
func excludeFixturePaths(skillDirs []string) (kept []string, skipped []SkippedSkill) {
	for _, d := range skillDirs {
		if seg := fixtureSegmentIn(d); seg != "" {
			skipped = append(skipped, SkippedSkill{
				Name:   path.Base(d),
				Path:   d,
				Reason: fmt.Sprintf("under %s/ — test fixtures are excluded from indexing", seg),
			})
			continue
		}
		kept = append(kept, d)
	}
	return kept, skipped
}

// fixtureSegmentIn returns the first fixture-marking path segment in dir, or
// "" when dir is a regular skill location.
func fixtureSegmentIn(dir string) string {
	for seg := range strings.SplitSeq(dir, "/") {
		if fixturePathSegments[seg] {
			return seg
		}
	}
	return ""
}
