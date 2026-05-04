package plantools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/moby/patternmatcher"
	"github.com/yargevad/filepathx"
)

var defaultIgnoredDirs = []string{
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

var errPathEscapesBase = errors.New("path escapes base directory")

func buildShouldIgnore(baseDir string) func(string, bool) bool {
	patterns := make([]string, len(defaultIgnoredDirs))
	copy(patterns, defaultIgnoredDirs)

	if data, err := os.ReadFile(filepath.Join(baseDir, ".gitignore")); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				patterns = append(patterns, line)
			}
		}
	}

	pm, err := patternmatcher.New(patterns)
	if err != nil {
		ignored := make(map[string]bool, len(defaultIgnoredDirs))
		for _, d := range defaultIgnoredDirs {
			ignored[d] = true
		}
		return func(filePath string, isDir bool) bool {
			base := filepath.Base(filePath)
			return base != "." && ignored[base]
		}
	}

	return func(filePath string, isDir bool) bool {
		if filePath == "." {
			return false
		}
		matched, _ := pm.MatchesOrParentMatches(filePath)
		return matched
	}
}

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

func secureGlobMatch(absBase, realBase, match string) (string, bool) {
	absMatch, err := filepath.Abs(match)
	if err != nil || !isPathInBase(absBase, absMatch) {
		return "", false
	}
	realMatch, err := filepath.EvalSymlinks(absMatch)
	if err != nil || !isPathInBase(realBase, realMatch) {
		return "", false
	}
	return absMatch, true
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

func (t *globTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
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
	if args.Limit == 0 {
		args.Limit = 100
	}

	_, absPattern, err := secureToolPath(t.baseDir, args.Pattern)
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
	shouldIgnore := buildShouldIgnore(absBase)
	prefix := absBase + string(filepath.Separator)

	var allMatches []string
	if strings.Contains(args.Pattern, "**") {
		allMatches, err = filepathx.Glob(absPattern)
	} else {
		allMatches, err = filepath.Glob(absPattern)
	}
	if err != nil {
		return "", fmt.Errorf("glob: %w", err)
	}

	var filtered []string
	for _, m := range allMatches {
		absMatch, ok := secureGlobMatch(absBase, realBase, m)
		if !ok {
			continue
		}
		rel := relFromBase(absBase, absMatch)
		if strings.HasPrefix(rel, "../") || rel == ".." || filepath.IsAbs(rel) {
			rel = filepath.ToSlash(strings.TrimPrefix(absMatch, prefix))
			rel = strings.TrimPrefix(rel, "/")
		}
		info, statErr := os.Stat(absMatch)
		if statErr != nil {
			continue
		}
		isDir := info.IsDir()
		if !shouldIgnore(rel, isDir) {
			if isDir {
				rel += "/"
			}
			filtered = append(filtered, rel)
			if len(filtered) >= args.Limit {
				break
			}
		}
	}

	if len(filtered) == 0 {
		return "no matches found", nil
	}
	return strings.Join(filtered, "\n"), nil
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

func (t *grepTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
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
	shouldIgnore := buildShouldIgnore(absBase)
	var results []string

	err = filepath.Walk(absBase, func(absPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel := relFromBase(absBase, absPath)

		if shouldIgnore(rel, info.IsDir()) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if len(results) >= args.Limit {
			return filepath.SkipAll
		}
		if args.Glob != "" {
			matched, matchErr := filepath.Match(args.Glob, rel)
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

		realPath, realErr := filepath.EvalSymlinks(absPath)
		if realErr != nil || !isPathInBase(realBase, realPath) {
			return nil
		}
		f, openErr := os.Open(realPath)
		if openErr != nil {
			return nil
		}
		defer func() {
			_ = f.Close()
		}()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				results = append(results, fmt.Sprintf("%s:%d: %s", rel, lineNum, line))
				if len(results) >= args.Limit {
					return nil
				}
			}
		}
		return scanner.Err()
	})
	if err != nil {
		return "", fmt.Errorf("walk: %w", err)
	}

	if len(results) == 0 {
		return "no matches found", nil
	}
	return strings.Join(results, "\n"), nil
}

// --- read tool ---

type readTool struct{ baseDir string }

func NewReadTool(baseDir string) tool.InvokableTool { return &readTool{baseDir: baseDir} }

func (t *readTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "read",
		Desc: "Reads the contents of a file. If the path is a directory, reports that instead of failing. Supports offset (skip lines) and limit (max lines to return).",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path":   {Type: schema.String, Desc: "The path of the file to read.", Required: true},
			"offset": {Type: schema.Integer, Desc: "Number of lines to skip from the start. Defaults to 0."},
			"limit":  {Type: schema.Integer, Desc: "Maximum number of lines to return. Defaults to first 200 lines."},
		}),
	}, nil
}

func (t *readTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
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
	if args.Limit == 0 {
		args.Limit = 200
	}

	_, absPath, err := secureExistingToolPath(t.baseDir, args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if info.IsDir() {
		return "is a directory", nil
	}

	f, err := os.Open(absPath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	scanner := bufio.NewScanner(f)
	var lines []string
	lineNum := 0
	for scanner.Scan() {
		if lineNum < args.Offset {
			lineNum++
			continue
		}
		if args.Limit > 0 && len(lines) >= args.Limit {
			break
		}
		lines = append(lines, scanner.Text())
		lineNum++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan file: %w", err)
	}
	if len(lines) == 0 {
		if args.Offset > 0 {
			return "no lines in requested range", nil
		}
		return "empty file", nil
	}
	return strings.Join(lines, "\n"), nil
}

// --- list tool ---

type listTool struct{ baseDir string }

func NewListTool(baseDir string) tool.InvokableTool { return &listTool{baseDir: baseDir} }

func (t *listTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list",
		Desc: "Lists directory contents. Directories have a trailing '/'. Returns 'is a file' if the path is a file.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"path": {Type: schema.String, Desc: "The directory path to list.", Required: true},
		}),
	}, nil
}

func (t *listTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Path string `json:"path"`
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

	relPath, absPath, err := secureExistingToolPath(t.baseDir, args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "is a file", nil
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		return "", fmt.Errorf("read dir: %w", err)
	}
	if len(entries) == 0 {
		return "empty directory", nil
	}

	shouldIgnore := buildShouldIgnore(t.baseDir)
	var names []string
	for _, entry := range entries {
		entryRel := filepath.ToSlash(filepath.Join(relPath, entry.Name()))
		if shouldIgnore(entryRel, entry.IsDir()) {
			continue
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}

	if len(names) == 0 {
		return "empty directory", nil
	}
	return strings.Join(names, "\n"), nil
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

func (t *treeTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
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

	_, rootAbs, err := secureExistingToolPath(t.baseDir, args.Path)
	if err != nil {
		return "", err
	}
	absBase, err := filepath.Abs(t.baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	shouldIgnore := buildShouldIgnore(absBase)

	const maxEntries = 500
	var lines []string
	truncated := false

	err = filepath.Walk(rootAbs, func(absPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		relFromBasePath := relFromBase(absBase, absPath)

		if shouldIgnore(relFromBasePath, info.IsDir()) {
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
		result += "\n... (truncated)"
	}
	return result, nil
}
