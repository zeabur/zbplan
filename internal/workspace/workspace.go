// Package workspace decides which project paths are visible to the planning
// agent. The same decision governs agent file tools and the build context sent
// to BuildKit, so a generated Dockerfile cannot reach files the agent's tools
// are denied.
package workspace

import (
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/moby/patternmatcher"
)

const (
	maxIgnoreFileBytes = 256 * 1024
	maxIgnorePatterns  = 2_000
)

// DefaultIgnoredDirs are hidden even when the project has no .gitignore.
var DefaultIgnoredDirs = []string{
	".git",
	".venv",
	"venv",
	"node_modules",
	"__pycache__",
	".mypy_cache",
	".pytest_cache",
	".tox",
	".next",
	".nuxt",
	".cache",
}

// IsSensitive reports whether a slash-separated path relative to the
// workspace root names a credential or secret-bearing file.
func IsSensitive(relPath string) bool {
	lowerPath := filepath.ToSlash(strings.ToLower(relPath))
	base := path.Base(lowerPath)
	switch base {
	case ".env", ".envrc", ".dev.vars", ".npmrc", ".pypirc", ".netrc", "id_rsa", "id_ed25519", ".dockerconfigjson":
		return true
	}
	if strings.HasPrefix(base, ".env.") ||
		lowerPath == ".docker/config.json" ||
		strings.HasSuffix(lowerPath, "/.docker/config.json") {
		return true
	}
	switch path.Ext(base) {
	case ".pem", ".key", ".p12", ".pfx":
		return true
	default:
		return false
	}
}

// IgnoreMatcher returns a predicate for the default ignored directories and
// the bounded root .gitignore of baseDir. Paths are slash-separated and
// relative to baseDir.
func IgnoreMatcher(baseDir string) func(relPath string, isDir bool) bool {
	patterns := make([]string, 0, len(DefaultIgnoredDirs))
	for _, dir := range DefaultIgnoredDirs {
		patterns = append(patterns, gitignorePattern(dir))
	}
	if ignoreFile, err := os.Open(filepath.Join(baseDir, ".gitignore")); err == nil {
		data, readErr := io.ReadAll(io.LimitReader(ignoreFile, maxIgnoreFileBytes+1))
		_ = ignoreFile.Close()
		if readErr == nil && len(data) <= maxIgnoreFileBytes {
			for line := range strings.SplitSeq(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line != "" && !strings.HasPrefix(line, "#") {
					patterns = append(patterns, gitignorePattern(line))
					if len(patterns) >= maxIgnorePatterns {
						break
					}
				}
			}
		}
	}

	pm, err := patternmatcher.New(patterns)
	if err != nil {
		ignored := make(map[string]bool, len(DefaultIgnoredDirs))
		for _, d := range DefaultIgnoredDirs {
			ignored[d] = true
		}
		return func(relPath string, _ bool) bool {
			for part := range strings.SplitSeq(filepath.ToSlash(relPath), "/") {
				if ignored[part] {
					return true
				}
			}
			return false
		}
	}

	return func(relPath string, _ bool) bool {
		if relPath == "." || relPath == "" {
			return false
		}
		matched, _ := pm.MatchesOrParentMatches(filepath.ToSlash(relPath))
		return matched
	}
}

// gitignorePattern converts a .gitignore line to the root-anchored pattern
// syntax of patternmatcher (.dockerignore semantics). In .gitignore, a
// pattern without a slash before its end matches at any depth, while a
// leading slash anchors it to the root.
func gitignorePattern(line string) string {
	negated := strings.HasPrefix(line, "!")
	line = strings.TrimPrefix(line, "!")
	line = strings.TrimSuffix(line, "/")
	if anchored, ok := strings.CutPrefix(line, "/"); ok {
		line = anchored
	} else if !strings.Contains(line, "/") {
		line = "**/" + line
	}
	if negated {
		return "!" + line
	}
	return line
}

// HiddenMatcher returns the complete visibility predicate for baseDir: a path
// is hidden when it, or any parent directory, is ignored or sensitive.
func HiddenMatcher(baseDir string) func(relPath string, isDir bool) bool {
	ignored := IgnoreMatcher(baseDir)
	return func(relPath string, isDir bool) bool {
		relPath = filepath.ToSlash(relPath)
		if relPath == "." || relPath == "" {
			return false
		}
		if ignored(relPath, isDir) {
			return true
		}
		for current := relPath; current != "." && current != "/" && current != ""; current = path.Dir(current) {
			if IsSensitive(current) {
				return true
			}
		}
		return false
	}
}
