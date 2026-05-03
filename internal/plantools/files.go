package plantools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"

	"github.com/moby/patternmatcher"
	"github.com/zendev-sh/goai"
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

func buildShouldIgnore(fsys fs.FS) func(filePath string, isDir bool) bool {
	patterns := make([]string, len(defaultIgnoredDirs))
	copy(patterns, defaultIgnoredDirs)

	if data, err := fs.ReadFile(fsys, ".gitignore"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
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
			base := path.Base(filePath)
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

func NewGlobTool(fsys fs.FS) goai.Tool {
	type Args struct {
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit"`
	}

	return goai.Tool{
		Name:        "glob",
		Description: "Finds files based on pattern matching. Supports * and ? wildcards but not **.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Glob pattern to match files against."
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of results to return. Defaults to 100.",
					"default": 100
				}
			},
			"required": ["pattern"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.Pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			if args.Limit == 0 {
				args.Limit = 100
			}

			matches, err := fs.Glob(fsys, args.Pattern)
			if err != nil {
				return "", fmt.Errorf("glob: %w", err)
			}

			shouldIgnore := buildShouldIgnore(fsys)
			var filtered []string
			for _, m := range matches {
				if !shouldIgnore(m, false) {
					filtered = append(filtered, m)
					if len(filtered) >= args.Limit {
						break
					}
				}
			}

			if len(filtered) == 0 {
				return "no files found", nil
			}

			return strings.Join(filtered, "\n"), nil
		},
	}
}

func NewGrepTool(fsys fs.FS) goai.Tool {
	type Args struct {
		Pattern string `json:"pattern"`
		Glob    string `json:"glob"`
		Limit   int    `json:"limit"`
	}

	return goai.Tool{
		Name:        "grep",
		Description: "Searches for a regular expression pattern in file contents. Walks all files unless a glob filter is provided.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {
					"type": "string",
					"description": "Regular expression pattern to search for."
				},
				"glob": {
					"type": "string",
					"description": "Optional glob pattern (supports * and ?) to filter which files are searched."
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of matching lines to return. Defaults to 50.",
					"default": 50
				}
			},
			"required": ["pattern"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
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

			shouldIgnore := buildShouldIgnore(fsys)
			var results []string
			err = fs.WalkDir(fsys, ".", func(filePath string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if shouldIgnore(filePath, d.IsDir()) {
					if d.IsDir() {
						return fs.SkipDir
					}
					return nil
				}
				if d.IsDir() {
					return nil
				}
				if len(results) >= args.Limit {
					return fs.SkipAll
				}
				if args.Glob != "" {
					matched, matchErr := path.Match(args.Glob, filePath)
					if matchErr != nil {
						return fmt.Errorf("match glob: %w", matchErr)
					}
					if !matched {
						matched, matchErr = path.Match(args.Glob, d.Name())
						if matchErr != nil || !matched {
							return matchErr
						}
					}
				}

				f, openErr := fsys.Open(filePath)
				if openErr != nil {
					return nil
				}
				defer f.Close()

				scanner := bufio.NewScanner(f)
				lineNum := 0
				for scanner.Scan() {
					lineNum++
					line := scanner.Text()
					if re.MatchString(line) {
						results = append(results, fmt.Sprintf("%s:%d: %s", filePath, lineNum, line))
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
		},
	}
}

func NewReadTool(fsys fs.FS) goai.Tool {
	type Args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}

	return goai.Tool{
		Name:        "read",
		Description: "Reads the contents of a file. Supports offset (skip lines) and limit (max lines to return).",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "The path of the file to read."
				},
				"offset": {
					"type": "integer",
					"description": "Number of lines to skip from the start. Defaults to 0."
				},
				"limit": {
					"type": "integer",
					"description": "Maximum number of lines to return. Defaults to first 50 lines.",
					"default": 50
				}
			},
			"required": ["path"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.Path == "" {
				return "", fmt.Errorf("path is required")
			}

			if args.Limit == 0 {
				args.Limit = 50
			}

			f, err := fsys.Open(args.Path)
			if err != nil {
				return "", fmt.Errorf("open file: %w", err)
			}
			defer f.Close()

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

			return strings.Join(lines, "\n"), nil
		},
	}
}

func NewListTool(fsys fs.FS) goai.Tool {
	type Args struct {
		Path string `json:"path"`
	}

	return goai.Tool{
		Name:        "list",
		Description: "Lists directory contents. Directories have a trailing '/'. Returns 'is a file' if the path is a file.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {
					"type": "string",
					"description": "The directory path to list."
				}
			},
			"required": ["path"]
		}`),
		Execute: func(ctx context.Context, input json.RawMessage) (string, error) {
			var args Args
			if err := json.Unmarshal(input, &args); err != nil {
				return "", fmt.Errorf("unmarshal input: %w", err)
			}
			if args.Path == "" {
				return "", fmt.Errorf("path is required")
			}

			info, err := fs.Stat(fsys, args.Path)
			if err != nil {
				return "", fmt.Errorf("stat path: %w", err)
			}
			if !info.IsDir() {
				return "is a file", nil
			}

			entries, err := fs.ReadDir(fsys, args.Path)
			if err != nil {
				return "", fmt.Errorf("read dir: %w", err)
			}

			if len(entries) == 0 {
				return "empty directory", nil
			}

			shouldIgnore := buildShouldIgnore(fsys)
			var names []string
			for _, entry := range entries {
				entryPath := path.Join(args.Path, entry.Name())
				if shouldIgnore(entryPath, entry.IsDir()) {
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
		},
	}
}
