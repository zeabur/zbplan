package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHiddenMatcherCombinesIgnoreAndSensitiveRules(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/dist/\n*.log\n!keep.log\nbuild/out\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hidden := HiddenMatcher(dir)

	for path, isDir := range map[string]bool{
		".env":                      false,
		".env.production":           false,
		"config/.npmrc":             false,
		"certs/server.key":          false,
		"home/.docker/config.json":  false,
		"node_modules":              true,
		"node_modules/pkg/index.js": false,
		"packages/a/node_modules":   true,
		".git/config":               false,
		"dist":                      true,
		"dist/app.js":               false,
		"logs/debug.log":            false,
		"build/out/app":             false,
	} {
		if !hidden(path, isDir) {
			t.Errorf("%s should be hidden", path)
		}
	}
	for path, isDir := range map[string]bool{
		".":                 true,
		"package.json":      false,
		"src/main.go":       false,
		".gitignore":        false,
		"environment.ts":    false,
		"docs/env.md":       false,
		".dockerignore":     false,
		"public/robots.txt": false,
		"keep.log":          false,
		"packages/dist/a":   false,
		"nested/build/out":  true,
	} {
		if hidden(path, isDir) {
			t.Errorf("%s should be visible", path)
		}
	}
}

func TestIgnoreMatcherFallbackHidesNestedDefaultDirs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// An invalid pattern forces the fallback matcher.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("[\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored := IgnoreMatcher(dir)
	if !ignored("node_modules/pkg/index.js", false) {
		t.Fatal("fallback matcher exposed a file inside node_modules")
	}
	if ignored("src/index.js", false) {
		t.Fatal("fallback matcher hid an ordinary file")
	}
}
