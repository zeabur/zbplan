package plantools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/zeabur/zbplan/internal/workspace"
)

var errPathEscapesBase = errors.New("path escapes base directory")

const (
	maxReadLines         = 200
	maxReadBytes         = 64 * 1024
	maxReadableFileBytes = 1 << 20
	maxListEntries       = 200
	maxDirectoryScan     = 10_000
	maxGlobResults       = 200
	maxGlobVisited       = 20_000
	maxGrepResults       = 100
	maxGrepFiles         = 10_000
	maxGrepBytes         = 16 << 20
	maxTreeDepth         = 5
)

func relFromBase(baseDir, absPath string) string {
	rel, err := filepath.Rel(baseDir, absPath)
	if err != nil {
		return filepath.ToSlash(strings.TrimPrefix(absPath, baseDir+string(filepath.Separator)))
	}
	return filepath.ToSlash(rel)
}

func cleanToolPath(path string) (string, error) {
	nativePath := filepath.FromSlash(path)
	if filepath.IsAbs(nativePath) {
		return "", errPathEscapesBase
	}
	if slices.Contains(strings.Split(nativePath, string(filepath.Separator)), "..") {
		return "", errPathEscapesBase
	}
	return filepath.Clean(nativePath), nil
}

func isPathInBase(baseDir, absPath string) bool {
	rel, err := filepath.Rel(baseDir, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func secureToolPath(baseDir, path string) (string, string, error) {
	rel, err := cleanToolPath(path)
	if err != nil {
		return "", "", err
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve base directory: %w", err)
	}
	absPath := filepath.Join(absBase, rel)
	if !isPathInBase(absBase, absPath) {
		return "", "", errPathEscapesBase
	}
	return rel, absPath, nil
}

func secureExistingToolPath(baseDir, path string) (string, string, error) {
	rel, absPath, err := secureToolPath(baseDir, path)
	if err != nil {
		return "", "", err
	}
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve base directory: %w", err)
	}
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", "", fmt.Errorf("resolve base directory: %w", err)
	}

	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve path: %w", err)
	}
	if !isPathInBase(realBase, realPath) {
		return "", "", errPathEscapesBase
	}
	return rel, realPath, nil
}

// unavailableExistingToolPath applies the workspace visibility rule, which
// also checks every parent directory, to both the requested path and its
// resolved symlink target.
func unavailableExistingToolPath(baseDir, requestedRel, resolvedAbs string, isDir bool) (bool, error) {
	hidden := workspace.HiddenMatcher(baseDir)
	if hidden(filepath.ToSlash(requestedRel), isDir) {
		return true, nil
	}

	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return false, fmt.Errorf("resolve base directory: %w", err)
	}
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return false, fmt.Errorf("resolve base directory: %w", err)
	}
	resolvedRel, err := filepath.Rel(realBase, resolvedAbs)
	if err != nil || !isPathInBase(realBase, resolvedAbs) {
		return false, errPathEscapesBase
	}
	return hidden(filepath.ToSlash(resolvedRel), isDir), nil
}

func globWalkRoot(absBase, pattern string) string {
	parts := strings.Split(filepath.FromSlash(pattern), string(filepath.Separator))
	literal := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "**" || strings.ContainsAny(part, "*?[") {
			break
		}
		literal = append(literal, part)
	}
	root := filepath.Join(append([]string{absBase}, literal...)...)
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return root
	}
	return root
}

func matchGlob(pattern, path string) (bool, error) {
	patternParts := strings.Split(filepath.ToSlash(pattern), "/")
	pathParts := strings.Split(filepath.ToSlash(path), "/")
	type state struct{ pattern, path int }
	memo := make(map[state]bool)
	seen := make(map[state]bool)
	var match func(int, int) (bool, error)
	match = func(pi, si int) (bool, error) {
		key := state{pattern: pi, path: si}
		if seen[key] {
			return memo[key], nil
		}
		seen[key] = true
		if pi == len(patternParts) {
			memo[key] = si == len(pathParts)
			return memo[key], nil
		}
		if patternParts[pi] == "**" {
			ok, err := match(pi+1, si)
			if err != nil || ok {
				memo[key] = ok
				return ok, err
			}
			if si < len(pathParts) {
				ok, err = match(pi, si+1)
				memo[key] = ok
				return ok, err
			}
			return false, nil
		}
		if si >= len(pathParts) {
			return false, nil
		}
		ok, err := filepath.Match(patternParts[pi], pathParts[si])
		if err != nil || !ok {
			return false, err
		}
		ok, err = match(pi+1, si+1)
		memo[key] = ok
		return ok, err
	}
	return match(0, 0)
}

// --- glob tool ---

type globTool struct{ baseDir string }

func NewGlobTool(baseDir string) tool.InvokableTool { return &globTool{baseDir: baseDir} }

func (t *globTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "glob",
		Desc: "Finds files and directories by pattern. Directories have a trailing '/'. Supports *, ?, and ** (matches any number of directories). Use ** to enumerate manifests across a monorepo in one call, e.g. '**/pyproject.toml'.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {Type: schema.String, Desc: "Glob pattern. ** recurses into subdirectories.", Required: true},
			"limit":   {Type: schema.Integer, Desc: "Maximum number of results to return. Defaults to 100."},
		}),
	}, nil
}

func (t *globTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit"`
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if len(args.Pattern) > 512 {
		return "", fmt.Errorf("pattern is too long")
	}
	if args.Limit == 0 {
		args.Limit = 100
	}
	if args.Limit < 1 || args.Limit > maxGlobResults {
		return "", fmt.Errorf("limit must be between 1 and %d", maxGlobResults)
	}

	_, _, err := secureToolPath(t.baseDir, args.Pattern)
	if err != nil {
		return "", err
	}
	absBase, err := filepath.Abs(t.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	root := globWalkRoot(absBase, args.Pattern)
	shouldIgnore := workspace.IgnoreMatcher(absBase)
	results := make([]string, 0, args.Limit)
	visited := 0
	truncated := false

	err = filepath.Walk(root, func(absPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		visited++
		if visited > maxGlobVisited {
			truncated = true
			return filepath.SkipAll
		}
		rel := relFromBase(absBase, absPath)
		if shouldIgnore(rel, info.IsDir()) || workspace.IsSensitive(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		realPath, err := filepath.EvalSymlinks(absPath)
		if err != nil || !isPathInBase(realBase, realPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		matched, err := matchGlob(args.Pattern, rel)
		if err != nil {
			return fmt.Errorf("match glob: %w", err)
		}
		if !matched {
			return nil
		}
		if info.IsDir() {
			rel += "/"
		}
		results = append(results, rel)
		if len(results) >= args.Limit {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk: %w", err)
	}
	if len(results) == 0 {
		if truncated {
			return fmt.Sprintf("no matches found [glob truncated after %d visited entries]", visited), nil
		}
		return "no matches found", nil
	}
	result := strings.Join(results, "\n")
	if truncated {
		result += fmt.Sprintf("\n[glob truncated after %d results or %d visited entries]", len(results), visited)
	}
	return result, nil
}

// --- grep tool ---

type grepTool struct{ baseDir string }

func NewGrepTool(baseDir string) tool.InvokableTool { return &grepTool{baseDir: baseDir} }

func (t *grepTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "grep",
		Desc: "Searches for a regular expression pattern in file contents. Walks all files unless a glob filter is provided.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"pattern": {Type: schema.String, Desc: "Regular expression pattern to search for.", Required: true},
			"glob":    {Type: schema.String, Desc: "Optional glob pattern (supports * and ?) to filter which files are searched."},
			"limit":   {Type: schema.Integer, Desc: "Maximum number of matching lines to return. Defaults to 50."},
		}),
	}, nil
}

func (t *grepTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
		Limit   int    `json:"limit"`
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if args.Limit == 0 {
		args.Limit = 50
	}
	if len(args.Pattern) > 512 {
		return "", fmt.Errorf("pattern is too long")
	}
	if args.Limit < 1 || args.Limit > maxGrepResults {
		return "", fmt.Errorf("limit must be between 1 and %d", maxGrepResults)
	}
	if len(args.Glob) > 512 {
		return "", fmt.Errorf("glob is too long")
	}

	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("compile pattern: %w", err)
	}
	absBase, err := filepath.Abs(t.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	realBase, err := filepath.EvalSymlinks(absBase)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	shouldIgnore := workspace.IgnoreMatcher(absBase)
	results := make([]string, 0, args.Limit)
	filesScanned := 0
	bytesScanned := int64(0)
	truncated := false
	visitedEntries := 0

	err = filepath.Walk(absBase, func(absPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		visitedEntries++
		if visitedEntries > maxGlobVisited {
			truncated = true
			return filepath.SkipAll
		}
		rel := relFromBase(absBase, absPath)
		if shouldIgnore(rel, info.IsDir()) || workspace.IsSensitive(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		if len(results) >= args.Limit || filesScanned >= maxGrepFiles || bytesScanned >= maxGrepBytes {
			truncated = true
			return filepath.SkipAll
		}
		if args.Glob != "" {
			matched, matchErr := matchGlob(args.Glob, rel)
			if matchErr != nil {
				return fmt.Errorf("match glob: %w", matchErr)
			}
			if !matched {
				matched, matchErr = filepath.Match(args.Glob, info.Name())
				if matchErr != nil || !matched {
					return matchErr
				}
			}
		}
		if info.Size() > maxReadableFileBytes || bytesScanned+info.Size() > maxGrepBytes {
			truncated = true
			return nil
		}
		realPath, realErr := filepath.EvalSymlinks(absPath)
		if realErr != nil || !isPathInBase(realBase, realPath) {
			return nil
		}

		f, openErr := os.Open(realPath)
		if openErr != nil {
			return nil
		}
		filesScanned++
		bytesScanned += info.Size()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), maxReadableFileBytes)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				if len(line) > 2048 {
					line = safeTextPrefix(line, 2048) + "…"
				}
				results = append(results, fmt.Sprintf("%s:%d: %s", rel, lineNum, line))
				if len(results) >= args.Limit {
					truncated = true
					break
				}
			}
		}
		scanErr := scanner.Err()
		_ = f.Close()
		if scanErr != nil {
			truncated = true
		}
		if len(results) >= args.Limit {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk: %w", err)
	}
	if len(results) == 0 {
		if truncated {
			return fmt.Sprintf("no matches found [search truncated after %d files and %d bytes]", filesScanned, bytesScanned), nil
		}
		return "no matches found", nil
	}
	result := strings.Join(results, "\n")
	if truncated {
		result += fmt.Sprintf("\n[grep truncated after %d matches, %d files, and %d bytes]", len(results), filesScanned, bytesScanned)
	}
	return result, nil
}

// --- read tool ---

type readTool struct{ baseDir string }

func NewReadTool(baseDir string) tool.InvokableTool { return &readTool{baseDir: baseDir} }

func (t *readTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "read",
		Desc: "Reads a bounded, line-numbered range from a regular file. Directories are listed directly. Ignored and sensitive paths are unavailable. Results report exact continuation offsets.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":   {Type: schema.String, Desc: "The path of the file to read.", Required: true},
			"offset": {Type: schema.Integer, Desc: "Number of lines to skip from the start. Defaults to 0."},
			"column": {Type: schema.Integer, Desc: "Byte position within the first requested line to resume from. Pass the next_column reported for a split long line; defaults to 0."},
			"limit":  {Type: schema.Integer, Desc: "Maximum number of lines to return. Defaults to first 200 lines."},
		}),
	}, nil
}

func (t *readTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Column int    `json:"column"`
		Limit  int    `json:"limit"`
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if args.Offset < 0 {
		return "", fmt.Errorf("offset must not be negative")
	}
	if args.Column < 0 {
		return "", fmt.Errorf("column must not be negative")
	}
	if args.Limit == 0 {
		args.Limit = maxReadLines
	}
	if args.Limit < 1 || args.Limit > maxReadLines {
		return "", fmt.Errorf("limit must be between 1 and %d", maxReadLines)
	}

	relPath, absPath, err := secureExistingToolPath(t.baseDir, args.Path)
	if err != nil {
		return "", err
	}
	relPath = filepath.ToSlash(relPath)
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	unavailable, err := unavailableExistingToolPath(t.baseDir, relPath, absPath, info.IsDir())
	if err != nil {
		return "", err
	}
	if unavailable {
		return "", fmt.Errorf("path is unavailable because it is ignored or sensitive")
	}
	if info.IsDir() {
		return listDirectory(ctx, t.baseDir, relPath, absPath, maxListEntries)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path is not a regular file")
	}
	if info.Size() > maxReadableFileBytes {
		return "", fmt.Errorf("file is %d bytes; maximum readable size is %d", info.Size(), maxReadableFileBytes)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxReadableFileBytes)
	lines := make([]string, 0, args.Limit)
	lineNum := 0
	contentBytes := 0
	hasMore := false
	// A line longer than the remaining byte budget is split. The cursor then
	// stays on that line and advances by column, so pagination never skips
	// the undisclosed suffix of a long line such as minified JSON.
	splitColumn := -1
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		lineNum++
		if lineNum <= args.Offset {
			continue
		}
		if len(lines) >= args.Limit {
			hasMore = true
			break
		}
		text := scanner.Text()
		prefix := fmt.Sprintf("%d:", lineNum)
		column := 0
		if lineNum == args.Offset+1 && args.Column > 0 {
			if args.Column > len(text) {
				return "", fmt.Errorf("column %d exceeds the %d-byte length of line %d", args.Column, len(text), lineNum)
			}
			column = args.Column
			for column < len(text) && !utf8.RuneStart(text[column]) {
				column++
			}
			text = text[column:]
			prefix += "…"
		}
		line := prefix + text
		if contentBytes+len(line)+1 > maxReadBytes {
			remaining := maxReadBytes - contentBytes - len(prefix) - len("…") - 1
			if part := safeTextPrefix(text, max(remaining, 0)); part != "" {
				lines = append(lines, prefix+part+"…")
				splitColumn = column + len(part)
			}
			hasMore = true
			break
		}
		lines = append(lines, line)
		contentBytes += len(line) + 1
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan file: %w", err)
	}
	if len(lines) == 0 {
		if args.Offset > 0 {
			return fmt.Sprintf("[%s: no lines after offset %d]", relPath, args.Offset), nil
		}
		return fmt.Sprintf("[%s: empty file]", relPath), nil
	}

	endLine := args.Offset + len(lines)
	status := "complete"
	switch {
	case splitColumn >= 0:
		status = fmt.Sprintf("line %d continues; next_offset=%d next_column=%d", endLine, endLine-1, splitColumn)
	case hasMore:
		status = fmt.Sprintf("next_offset=%d", endLine)
	}
	return fmt.Sprintf("[%s lines %d-%d; %s]\n%s", relPath, args.Offset+1, endLine, status, strings.Join(lines, "\n")), nil
}

// --- list tool ---

type listTool struct{ baseDir string }

func NewListTool(baseDir string) tool.InvokableTool { return &listTool{baseDir: baseDir} }

func (t *listTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list",
		Desc: "Lists bounded directory contents. Directories have a trailing '/'. Reports truncation explicitly.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":  {Type: schema.String, Desc: "The directory path to list.", Required: true},
			"limit": {Type: schema.Integer, Desc: "Maximum entries to return. Defaults to and is capped at 200."},
		}),
	}, nil
}

func (t *listTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Path  string `json:"path"`
		Limit int    `json:"limit"`
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if args.Limit == 0 {
		args.Limit = maxListEntries
	}
	if args.Limit < 1 || args.Limit > maxListEntries {
		return "", fmt.Errorf("limit must be between 1 and %d", maxListEntries)
	}

	relPath, absPath, err := secureExistingToolPath(t.baseDir, args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	unavailable, err := unavailableExistingToolPath(t.baseDir, relPath, absPath, info.IsDir())
	if err != nil {
		return "", err
	}
	if unavailable {
		return "", fmt.Errorf("path is unavailable because it is ignored or sensitive")
	}
	if !info.IsDir() {
		return "is a file", nil
	}
	return listDirectory(ctx, t.baseDir, filepath.ToSlash(relPath), absPath, args.Limit)
}

func listDirectory(ctx context.Context, baseDir, relPath, absPath string, limit int) (string, error) {
	dir, err := os.Open(absPath)
	if err != nil {
		return "", fmt.Errorf("open directory: %w", err)
	}
	defer func() { _ = dir.Close() }()

	entries, err := dir.ReadDir(maxDirectoryScan + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read directory: %w", err)
	}
	truncated := len(entries) > maxDirectoryScan
	if truncated {
		entries = entries[:maxDirectoryScan]
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	shouldIgnore := workspace.IgnoreMatcher(baseDir)
	names := make([]string, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		entryRel := filepath.ToSlash(filepath.Join(relPath, entry.Name()))
		if shouldIgnore(entryRel, entry.IsDir()) || workspace.IsSensitive(entryRel) {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
		if len(names) >= limit {
			truncated = true
			break
		}
	}
	if len(names) == 0 {
		return "empty directory", nil
	}
	result := strings.Join(names, "\n")
	if truncated {
		result += fmt.Sprintf("\n[list truncated after %d entries]", len(names))
	}
	return result, nil
}

func safeTextPrefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

// --- tree tool ---

type treeTool struct{ baseDir string }

func NewTreeTool(baseDir string) tool.InvokableTool { return &treeTool{baseDir: baseDir} }

func (t *treeTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "tree",
		Desc: "Returns a depth-limited directory tree in one call. Prefer this over multiple list calls to understand overall project structure.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":  {Type: schema.String, Desc: "The directory path to tree. Defaults to '.' (project root)."},
			"depth": {Type: schema.Integer, Desc: "Maximum directory depth to recurse. Defaults to 3."},
		}),
	}, nil
}

func (t *treeTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Path  string `json:"path"`
		Depth int    `json:"depth"`
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Path == "" {
		args.Path = "."
	}
	if args.Depth == 0 {
		args.Depth = 3
	}
	if args.Depth < 1 || args.Depth > maxTreeDepth {
		return "", fmt.Errorf("depth must be between 1 and %d", maxTreeDepth)
	}

	_, rootAbs, err := secureExistingToolPath(t.baseDir, args.Path)
	if err != nil {
		return "", err
	}
	absBase, err := filepath.Abs(t.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	shouldIgnore := workspace.IgnoreMatcher(absBase)

	const maxEntries = 500
	lines := make([]string, 0, maxEntries)
	truncated := false

	err = filepath.Walk(rootAbs, func(absPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relFromBasePath := relFromBase(absBase, absPath)
		if shouldIgnore(relFromBasePath, info.IsDir()) || workspace.IsSensitive(relFromBasePath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		relFromRoot, _ := filepath.Rel(rootAbs, absPath)
		relFromRoot = filepath.ToSlash(relFromRoot)
		if relFromRoot == "." {
			return nil
		}
		depth := strings.Count(relFromRoot, "/")
		if depth >= args.Depth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if len(lines) >= maxEntries {
			truncated = true
			return filepath.SkipAll
		}

		indent := strings.Repeat("  ", depth)
		name := info.Name()
		if info.IsDir() {
			name += "/"
		}
		lines = append(lines, indent+name)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk: %w", err)
	}
	if len(lines) == 0 {
		return "empty directory", nil
	}
	result := strings.Join(lines, "\n")
	if truncated {
		result += "\n[tree truncated after 500 entries]"
	}
	return result, nil
}
