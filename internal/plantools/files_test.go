package plantools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToolReturnsDirectoryNotice(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(baseDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"src"}`)
	if err != nil {
		t.Fatalf("read directory returned error: %v", err)
	}
	if result != "is a directory" {
		t.Fatalf("expected directory notice, got %q", result)
	}
}

func TestReadToolReturnsEmptyFileNotice(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "README.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"README.md"}`)
	if err != nil {
		t.Fatalf("read empty file returned error: %v", err)
	}
	if result != "empty file" {
		t.Fatalf("expected empty file notice, got %q", result)
	}
}

func TestReadToolReturnsOutOfRangeNotice(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"README.md","offset":10}`)
	if err != nil {
		t.Fatalf("read out-of-range offset returned error: %v", err)
	}
	if result != "no lines in requested range" {
		t.Fatalf("expected out-of-range notice, got %q", result)
	}
}

func TestGlobToolMarksDirectoriesWithTrailingSlash(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(baseDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewGlobTool(baseDir).InvokableRun(context.Background(), `{"pattern":"*"}`)
	if err != nil {
		t.Fatalf("glob returned error: %v", err)
	}

	lines := strings.Split(result, "\n")
	if !containsLine(lines, "src/") {
		t.Fatalf("expected src/ in glob result, got %q", result)
	}
	if !containsLine(lines, "README.md") {
		t.Fatalf("expected README.md in glob result, got %q", result)
	}
}

func TestRecursiveGlobRootMatchHasNoLeadingSlash(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "pyproject.toml"), []byte("[project]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewGlobTool(baseDir).InvokableRun(context.Background(), `{"pattern":"**/pyproject.toml"}`)
	if err != nil {
		t.Fatalf("glob returned error: %v", err)
	}
	if result != "pyproject.toml" {
		t.Fatalf("expected relative root match, got %q", result)
	}
}

func TestListAndTreeToolsMarkDirectoriesWithTrailingSlash(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, "src", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	listResult, err := NewListTool(baseDir).InvokableRun(context.Background(), `{"path":"."}`)
	if err != nil {
		t.Fatalf("list returned error: %v", err)
	}
	if !containsLine(strings.Split(listResult, "\n"), "src/") {
		t.Fatalf("expected src/ in list result, got %q", listResult)
	}

	treeResult, err := NewTreeTool(baseDir).InvokableRun(context.Background(), `{"path":".","depth":3}`)
	if err != nil {
		t.Fatalf("tree returned error: %v", err)
	}
	if !containsLine(strings.Split(treeResult, "\n"), "src/") {
		t.Fatalf("expected src/ in tree result, got %q", treeResult)
	}
	if !containsLine(strings.Split(treeResult, "\n"), "  pkg/") {
		t.Fatalf("expected pkg/ in tree result, got %q", treeResult)
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
