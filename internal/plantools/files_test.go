package plantools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestReadToolListsDirectory(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(baseDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"src"}`)
	if err != nil {
		t.Fatalf("read directory returned error: %v", err)
	}
	if result != "main.go" {
		t.Fatalf("expected directory listing, got %q", result)
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
	if result != "[README.md: empty file]" {
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
	if result != "[README.md: no lines after offset 10]" {
		t.Fatalf("expected out-of-range notice, got %q", result)
	}
}

func TestReadToolRejectsParentTraversal(t *testing.T) {
	baseDir := testBaseWithOutsideFile(t)

	_, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"../outside.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "path escapes base directory") {
		t.Fatalf("expected path escape error, got %v", err)
	}
}

func TestReadToolRejectsSymlinkEscape(t *testing.T) {
	baseDir, outsideFile := testBaseAndOutsideFile(t)
	if err := os.Symlink(outsideFile, filepath.Join(baseDir, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	_, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"link.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "path escapes base directory") {
		t.Fatalf("expected path escape error, got %v", err)
	}
}

func TestReadToolRejectsSensitiveSymlinkTarget(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(baseDir, "config.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"config.txt"}`); err == nil {
		t.Fatal("expected sensitive symlink target rejection")
	}
}

func TestReadToolRejectsNegativeLimitAndSensitiveFiles(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, ".env"), []byte("TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"README.md","limit":-1}`); err == nil {
		t.Fatal("expected negative limit rejection")
	}
	if _, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":".env"}`); err == nil {
		t.Fatal("expected sensitive path rejection")
	}
}

func TestReadToolReturnsLineNumbersAndContinuation(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "README.md"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"README.md","limit":2}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "[README.md lines 1-2; next_offset=2]") || !strings.Contains(result, "1:one\n2:two") {
		t.Fatalf("unexpected bounded read result: %q", result)
	}
}

func TestReadToolPagesThroughLongLinesWithoutLoss(t *testing.T) {
	baseDir := t.TempDir()
	long := strings.Repeat(`{"名前":"値",`, 15_000) // multi-byte runes, ~200 KiB on one line
	content := "head\n" + long + "\ntail\n"
	if err := os.WriteFile(filepath.Join(baseDir, "data.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	statusRE := regexp.MustCompile(`^\[data\.json lines \d+-\d+; (?:complete|next_offset=(\d+)|line \d+ continues; next_offset=(\d+) next_column=(\d+))\]\n`)
	lineRE := regexp.MustCompile(`^(\d+):(…?)(.*?)(…?)$`)
	read := NewReadTool(baseDir)
	offset, column := 0, 0
	rebuilt := map[int]string{}
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("pagination did not terminate")
		}
		result, err := read.InvokableRun(context.Background(), fmt.Sprintf(`{"path":"data.json","offset":%d,"column":%d}`, offset, column))
		if err != nil {
			t.Fatal(err)
		}
		if len(result) > maxReadBytes+256 {
			t.Fatalf("page is %d bytes, above the read budget", len(result))
		}
		header := statusRE.FindStringSubmatch(result)
		if header == nil {
			t.Fatalf("unexpected header: %.200q", result)
		}
		for line := range strings.SplitSeq(strings.TrimPrefix(result, header[0]), "\n") {
			m := lineRE.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("unexpected line: %.200q", line)
			}
			var n int
			_, _ = fmt.Sscan(m[1], &n)
			rebuilt[n] += m[3]
		}
		switch {
		case header[3] != "":
			_, _ = fmt.Sscan(header[2], &offset)
			_, _ = fmt.Sscan(header[3], &column)
		case header[1] != "":
			_, _ = fmt.Sscan(header[1], &offset)
			column = 0
		default:
			if rebuilt[1] != "head" || rebuilt[2] != long || rebuilt[3] != "tail" {
				t.Fatalf("reassembled file differs: line 2 has %d of %d bytes", len(rebuilt[2]), len(long))
			}
			return
		}
	}
}

func TestReadToolRejectsColumnBeyondLine(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(baseDir, "a.txt"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReadTool(baseDir).InvokableRun(context.Background(), `{"path":"a.txt","column":4}`); err == nil {
		t.Fatal("expected column rejection")
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

func TestGlobToolReturnsNoMatchesForMissingLiteralRoot(t *testing.T) {
	baseDir := t.TempDir()
	for _, pattern := range []string{"src/**/*.go", "src/main.go", "nested/src/*.go"} {
		t.Run(pattern, func(t *testing.T) {
			result, err := NewGlobTool(baseDir).InvokableRun(context.Background(), `{"pattern":"`+pattern+`"}`)
			if err != nil {
				t.Fatalf("missing glob root returned error: %v", err)
			}
			if result != "no matches found" {
				t.Fatalf("missing glob root returned %q", result)
			}
		})
	}
}

func TestGlobToolRejectsParentTraversal(t *testing.T) {
	baseDir := testBaseWithOutsideFile(t)

	_, err := NewGlobTool(baseDir).InvokableRun(context.Background(), `{"pattern":"../*.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "path escapes base directory") {
		t.Fatalf("expected path escape error, got %v", err)
	}
}

func TestGlobToolSkipsSymlinkEscape(t *testing.T) {
	baseDir, outsideFile := testBaseAndOutsideFile(t)
	if err := os.WriteFile(filepath.Join(baseDir, "inside.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(baseDir, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result, err := NewGlobTool(baseDir).InvokableRun(context.Background(), `{"pattern":"*"}`)
	if err != nil {
		t.Fatalf("glob returned error: %v", err)
	}
	if containsLine(strings.Split(result, "\n"), "link.txt") {
		t.Fatalf("expected symlink escape to be skipped, got %q", result)
	}
	if !containsLine(strings.Split(result, "\n"), "inside.txt") {
		t.Fatalf("expected inside file in glob result, got %q", result)
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

func TestGrepToolSkipsSymlinkEscape(t *testing.T) {
	baseDir, outsideFile := testBaseAndOutsideFile(t)
	if err := os.WriteFile(filepath.Join(baseDir, "inside.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(baseDir, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	result, err := NewGrepTool(baseDir).InvokableRun(context.Background(), `{"pattern":"secret"}`)
	if err != nil {
		t.Fatalf("grep returned error: %v", err)
	}
	if result != "no matches found" {
		t.Fatalf("expected symlink escape to be skipped, got %q", result)
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

func TestListToolRejectsParentTraversal(t *testing.T) {
	baseDir := testBaseWithOutsideFile(t)

	_, err := NewListTool(baseDir).InvokableRun(context.Background(), `{"path":".."}`)
	if err == nil || !strings.Contains(err.Error(), "path escapes base directory") {
		t.Fatalf("expected path escape error, got %v", err)
	}
}

func TestTreeToolRejectsParentTraversal(t *testing.T) {
	baseDir := testBaseWithOutsideFile(t)

	_, err := NewTreeTool(baseDir).InvokableRun(context.Background(), `{"path":".."}`)
	if err == nil || !strings.Contains(err.Error(), "path escapes base directory") {
		t.Fatalf("expected path escape error, got %v", err)
	}
}

func testBaseWithOutsideFile(t *testing.T) string {
	t.Helper()
	baseDir, _ := testBaseAndOutsideFile(t)
	return baseDir
}

func testBaseAndOutsideFile(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	if err := os.Mkdir(baseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return baseDir, outsideFile
}

func containsLine(lines []string, want string) bool {
	return slices.Contains(lines, want)
}
