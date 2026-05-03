package plantools

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/lithammer/fuzzysearch/fuzzy"
)

// TemplateInfo is the public view of a dockerfile template, used by integration
// tests that iterate over all embedded templates.
type TemplateInfo struct {
	Name    string
	Content string
}

// Templates returns all embedded Dockerfile templates.
func Templates() []TemplateInfo {
	result := make([]TemplateInfo, len(allDockerfileTemplates))
	for i, t := range allDockerfileTemplates {
		result[i] = TemplateInfo{Name: t.name, Content: t.content}
	}
	return result
}

// dockerfileTemplate holds a parsed template with metadata for fuzzy matching.
type dockerfileTemplate struct {
	name        string
	terms       []string // name + name-parts + keywords parsed from header
	description string
	content     string // Dockerfile body with metadata header stripped
}

//go:embed dockerfiles/*.Dockerfile
var dockerfilesFS embed.FS

var allDockerfileTemplates = mustLoadTemplates(dockerfilesFS, "dockerfiles")

func mustLoadTemplates(fsys fs.FS, dir string) []dockerfileTemplate {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		panic(fmt.Sprintf("plantools: read dockerfiles dir: %v", err))
	}
	templates := make([]dockerfileTemplate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".Dockerfile") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".Dockerfile")
		f, err := fsys.Open(path.Join(dir, entry.Name()))
		if err != nil {
			panic(fmt.Sprintf("plantools: open %s: %v", entry.Name(), err))
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			panic(fmt.Sprintf("plantools: read %s: %v", entry.Name(), err))
		}
		templates = append(templates, parseDockerfileTemplate(name, string(data)))
	}
	return templates
}

// parseDockerfileTemplate reads the metadata header from the top of the file
// (lines matching "# keywords: ..." or "# description: ..."), strips it from
// the returned content, and builds searchable terms from the template name,
// its hyphen-separated parts, and the parsed keywords.
func parseDockerfileTemplate(name, raw string) dockerfileTemplate {
	var keywords []string
	var description string
	lines := strings.Split(raw, "\n")
	contentStart := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			contentStart = i + 1
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			contentStart = i
			break
		}
		if rest, ok := strings.CutPrefix(trimmed, "# keywords:"); ok {
			keywords = strings.Fields(strings.TrimSpace(rest))
		} else if rest, ok := strings.CutPrefix(trimmed, "# description:"); ok {
			description = strings.TrimSpace(rest)
		}
		contentStart = i + 1
	}

	// terms = name + each hyphen-separated part + explicit keywords
	parts := strings.Split(name, "-")
	terms := make([]string, 0, 1+len(parts)+len(keywords))
	terms = append(terms, name)
	terms = append(terms, parts...)
	terms = append(terms, keywords...)

	content := strings.TrimSpace(strings.Join(lines[contentStart:], "\n"))
	return dockerfileTemplate{
		name:        name,
		terms:       terms,
		description: description,
		content:     content,
	}
}

type templateResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Template    string `json:"template"`
}

type getDockerfileTemplateTool struct{}

// NewGetDockerfileTemplateTool returns the get_dockerfile_template tool.
func NewGetDockerfileTemplateTool() tool.InvokableTool {
	return &getDockerfileTemplateTool{}
}

func (t *getDockerfileTemplateTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_dockerfile_template",
		Desc: "Returns up to 3 Dockerfile templates that best match the given language or framework query. " +
			"Templates follow best practices (multi-stage builds, cache mounts, non-root user). " +
			"Image tags in templates are examples — verify and pin them with list_tags.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "Language or framework to search for, e.g. 'go', 'python uv', 'fastapi', 'bun', 'next.js', 'rust', 'java maven'",
				Required: true,
			},
		}),
	}, nil
}

func (t *getDockerfileTemplateTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Query == "" {
		return "", fmt.Errorf("query is required")
	}

	matched := matchTemplates(args.Query, allDockerfileTemplates, 3)
	if len(matched) == 0 {
		return `[]`, nil
	}

	results := make([]templateResult, len(matched))
	for i, tmpl := range matched {
		results[i] = templateResult{
			Name:        tmpl.name,
			Description: tmpl.description,
			Template:    tmpl.content,
		}
	}
	out, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return string(out), nil
}

type templateCandidate struct {
	tmpl  dockerfileTemplate
	score int
}

// matchTemplates returns up to limit templates whose terms best fuzzy-match
// query, sorted ascending by distance (best match first).
func matchTemplates(query string, templates []dockerfileTemplate, limit int) []dockerfileTemplate {
	var candidates []templateCandidate
	for _, tmpl := range templates {
		best := math.MaxInt
		for _, term := range tmpl.terms {
			if d := fuzzy.RankMatchFold(query, term); d >= 0 && d < best {
				best = d
			}
		}
		if best < math.MaxInt {
			candidates = append(candidates, templateCandidate{tmpl: tmpl, score: best})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score < candidates[j].score
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	result := make([]dockerfileTemplate, len(candidates))
	for i, c := range candidates {
		result[i] = c.tmpl
	}
	return result
}
