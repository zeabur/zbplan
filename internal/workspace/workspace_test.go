package workspace

import (
	"os"
	"path/filepath"
	"strings"
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

func TestIgnoreMatcherSkipsOnlyInvalidPatterns(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secrets.json\n[\n*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored := IgnoreMatcher(dir)
	for _, path := range []string{"secrets.json", "logs/app.log", "node_modules/pkg/index.js"} {
		if !ignored(path, false) {
			t.Errorf("%s should stay ignored next to an invalid pattern", path)
		}
	}
	if ignored("src/index.js", false) {
		t.Fatal("ordinary file was hidden")
	}
}

func TestIgnoreMatcherKeepsRulesFromOversizedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	content := "secrets.json\n" + strings.Repeat("# padding\n", maxIgnoreFileBytes/10+1) + "late.txt\n"
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ignored := IgnoreMatcher(dir)
	if !ignored("secrets.json", false) {
		t.Fatal("rule within the size bound was dropped")
	}
	if ignored("late.txt", false) {
		t.Fatal("rule beyond the size bound was applied")
	}
}
