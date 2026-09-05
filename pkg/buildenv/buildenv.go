// Package buildenv makes platform build variables available to RUN without
// turning them into image environment defaults or Docker build arguments.
package buildenv

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
)

// A per-process key keeps cache identifiers opaque, even for low-entropy values.
// The same worker can reuse a result for identical inputs; changing a value
// changes the secret ID and invalidates the affected RUN's cache. A new worker
// starts a new cache namespace. Neither values nor unkeyed value hashes enter
// the Dockerfile, LLB or image metadata.
var cacheKey = sync.OnceValue(func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return key
})

type Prepared struct {
	Dockerfile string
	Session    []session.Attachable
	// BuildArgs contains only inputs explicitly referenced by Dockerfile
	// metadata/file instructions. Like a user-declared ARG, such inputs are
	// public build parameters, not secrets. RUN-only inputs never go here.
	BuildArgs          map[string]string
	useBundledFrontend bool
}

// FrontendAttrs selects the Dockerfile and supplies only explicitly requested
// public parameters. Old standard Dockerfile frontends predate secret-env
// mounts; use the worker's compatible bundled frontend for those builds.
func (p Prepared) FrontendAttrs() map[string]string {
	attrs := map[string]string{"filename": "Dockerfile"}
	for key, value := range p.BuildArgs {
		attrs["build-arg:"+key] = value
	}
	if p.useBundledFrontend {
		// cmdline marks an already-selected frontend and prevents the bundled
		// dockerfile.v0 frontend from forwarding to the old # syntax image.
		attrs["cmdline"] = "dockerfile.v0"
	}
	return attrs
}

type variables map[string]string

type expression struct {
	source, alias, value string
	references           map[string]struct{}
}

func (v variables) Get(key string) (string, bool) { value, ok := v[key]; return value, ok }
func (v variables) Keys() []string                { return slices.Sorted(maps.Keys(v)) }

// Prepare preserves the source text (including heredocs and parser directives)
// and only changes instruction headers. Dockerfile ENV assignments override
// platform inputs until the next FROM, matching the previous stage injection.
// Explicit references outside RUN retain normal Dockerfile ARG semantics; the
// caller must treat those inputs as public, since Docker records their use.
func Prepare(ctx context.Context, dockerfile string, values map[string]string) (Prepared, error) {
	result := Prepared{Dockerfile: dockerfile, BuildArgs: make(map[string]string)}
	if len(values) == 0 {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return Prepared{}, err
	}
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "=\x00\r\n\t\v\f ,$\"'`\\") || strings.ContainsRune(value, '\x00') {
			return Prepared{}, fmt.Errorf("invalid build environment variable name or value")
		}
		if len(value) > secretsprovider.MaxSecretSize {
			return Prepared{}, fmt.Errorf("build variable exceeds BuildKit secret size limit")
		}
	}
	ast, err := parser.Parse(strings.NewReader(dockerfile))
	if err != nil {
		return Prepared{}, err
	}
	lines := strings.Split(dockerfile, "\n")
	skipLines := map[int]bool{}
	active := variables{}
	declared := map[string]bool{}
	secrets := map[string][]byte{}
	lex := shell.NewLex(ast.EscapeToken)
	for _, node := range ast.AST.Children {
		if strings.EqualFold(node.Value, "from") {
			active = maps.Clone(values)
			declared = map[string]bool{}
			continue
		}
		command, err := instructions.ParseCommand(node)
		if err != nil {
			return Prepared{}, err
		}
		needed := map[string]bool{}
		var expressions []expression
		_, isRun := command.(*instructions.RunCommand)
		header := strings.Join(lines[node.StartLine-1:headerEndLine(node)], "\n")
		captureWith := func(lexer *shell.Lex, word string) (string, error) {
			match, err := lexer.ProcessWordWithMatches(word, active)
			if err != nil {
				return "", err
			}
			// Materialize a fully known configuration expression, not its
			// private inputs: ENV READY=${TOKEN:+yes} should expose only "yes".
			needle := word
			if node.Attributes["json"] {
				encoded, _ := json.Marshal(word)
				needle = string(encoded)
			}
			if !isRun && !lexer.SkipProcessQuotes && len(match.Matched) > 0 && len(match.Unmatched) == 0 && strings.Contains(header, needle) {
				hash := sha256.Sum256([]byte(word))
				expressions = append(expressions, expression{needle, "ZEABUR_DOCKERFILE_EXPR_" + hex.EncodeToString(hash[:]), match.Result, match.Matched})
				return word, nil
			}
			for key := range match.Matched {
				needed[key] = true
			}
			return word, nil
		}
		capture := func(word string) (string, error) { return captureWith(lex, word) }
		if expandable, ok := command.(instructions.SupportsSingleWordExpansion); ok {
			if err := expandable.Expand(capture); err != nil {
				return Prepared{}, err
			}
		}
		if expandable, ok := command.(instructions.SupportsSingleWordExpansionRaw); ok {
			rawLex := shell.NewLex('\\')
			rawLex.SkipProcessQuotes = true
			if err := expandable.ExpandRaw(func(word string) (string, error) { return captureWith(rawLex, word) }); err != nil {
				return Prepared{}, err
			}
		}
		if expose, ok := command.(*instructions.ExposeCommand); ok {
			for _, port := range expose.Ports {
				if _, err := capture(port); err != nil {
					return Prepared{}, err
				}
			}
		}
		var prefix strings.Builder
		materializedHeader, used := materializeExpressions(header, expressions, ast.EscapeToken)
		for _, expr := range expressions {
			if !used[expr.alias] {
				for key := range expr.references {
					needed[key] = true
				}
				continue
			}
			if !declared[expr.alias] {
				prefix.WriteString("ARG " + expr.alias + "\n")
				declared[expr.alias] = true
				result.BuildArgs[expr.alias] = expr.value
			}
		}
		bindings := map[string]string{}
		for _, key := range slices.Sorted(maps.Keys(needed)) {
			if !validArgName(key) {
				return Prepared{}, fmt.Errorf("build variable referenced by Dockerfile instruction is not a valid ARG name")
			}
			alias := "ZEABUR_DOCKERFILE_" + hex.EncodeToString([]byte(key))
			bindings[key] = alias
			if !declared[key] {
				prefix.WriteString("ARG " + alias + "\n")
				declared[key] = true
				result.BuildArgs[alias] = values[key]
			}
		}
		if run, ok := command.(*instructions.RunCommand); ok {
			// Rewrite only Dockerfile mount options, never the RUN script. Shell
			// references must keep their names so export/unset still work.
			if len(bindings) > 0 {
				// Flags are already unquoted by the Dockerfile parser. Requote
				// their contents and preserve the parsed shell/exec body exactly.
				// This also handles flags split over physical continuation lines.
				flags := make([]string, 0, len(node.Flags))
				for _, flag := range node.Flags {
					rewritten := rewriteReferences(flag, bindings, 0, true)
					flags = append(flags, "--"+strconv.Quote(strings.TrimPrefix(rewritten, "--")))
				}
				body := strings.Join(run.CmdLine, " ")
				if !run.PrependShell {
					encoded, _ := json.Marshal(run.CmdLine)
					body = string(encoded)
				}
				lines[node.StartLine-1] = "RUN " + strings.Join(flags, " ") + " " + body
				for i := node.StartLine; i < headerEndLine(node); i++ {
					skipLines[i] = true
				}
			}
			// An explicitly requested secret-env mount takes precedence over an
			// image ENV in BuildKit, so do not insert a duplicate for that name.
			explicit := map[string]bool{}
			for _, mount := range instructions.GetMounts(run) {
				if mount.Env != nil {
					key, _, err := lex.ProcessWord(*mount.Env, active)
					if err != nil {
						return Prepared{}, err
					}
					explicit[key] = true
				}
			}
			var flags strings.Builder
			for _, key := range active.Keys() {
				if explicit[key] {
					continue
				}
				id := secretID(key, active[key])
				secrets[id] = []byte(active[key])
				fmt.Fprintf(&flags, " --mount=type=secret,id=%s,env=%s,required=true", id, key)
			}
			line := lines[node.StartLine-1]
			start := len(line) - len(strings.TrimLeft(line, " \t"))
			end := start + len(node.Value)
			lines[node.StartLine-1] = prefix.String() + line[:end] + flags.String() + line[end:]
		} else {
			// A distinct ARG name avoids base-image ENV taking precedence over
			// an explicitly referenced platform input in Dockerfile expansion.
			headerEnd := headerEndLine(node)
			original := materializedHeader
			rewritten := strings.Split(rewriteReferences(original, bindings, ast.EscapeToken, false), "\n")
			copy(lines[node.StartLine-1:headerEnd], rewritten)
			cursor := headerEnd
			for _, doc := range node.Heredocs {
				count := strings.Count(doc.Content, "\n")
				if doc.Expand {
					contents := strings.Join(lines[cursor:cursor+count], "\n")
					copy(lines[cursor:cursor+count], strings.Split(rewriteReferences(contents, bindings, '\\', true), "\n"))
				}
				cursor += count + 1
			}
			lines[node.StartLine-1] = prefix.String() + lines[node.StartLine-1]
		}
		if env, ok := command.(*instructions.EnvCommand); ok {
			for _, pair := range env.Env {
				key, _, err := lex.ProcessWord(pair.Key, active)
				if err != nil {
					return Prepared{}, err
				}
				delete(active, key)
			}
		}
	}
	output := make([]string, 0, len(lines))
	for i, line := range lines {
		if !skipLines[i] {
			output = append(output, line)
		}
	}
	result.Dockerfile = strings.Join(output, "\n")
	if len(secrets) != 0 {
		result.Session = []session.Attachable{secretsprovider.FromMap(secrets)}
		result.useBundledFrontend = oldStandardFrontend(dockerfile)
	}
	return result, nil
}

// Only replace complete lexer words, never a substring of another reference or
// a quoted literal elsewhere in the instruction (e.g. X=$A Y='$A' Z=$AB).
func materializeExpressions(source string, expressions []expression, escape rune) (string, map[string]bool) {
	var out strings.Builder
	used := map[string]bool{}
	var quote byte
	boundary := func(ch byte) bool { return strings.ContainsRune(" \t\r\n=,[]", rune(ch)) }
	for i := 0; i < len(source); {
		ch := source[i]
		if rune(ch) == escape && quote != '\'' && i+1 < len(source) {
			out.WriteString(source[i : i+2])
			i += 2
			continue
		}
		var match *expression
		if quote == 0 && (i == 0 || boundary(source[i-1])) {
			for j := range expressions {
				expr := &expressions[j]
				end := i + len(expr.source)
				if strings.HasPrefix(source[i:], expr.source) && (end == len(source) || boundary(source[end])) && (match == nil || len(expr.source) > len(match.source)) {
					match = expr
				}
			}
		}
		if match != nil {
			out.WriteString("\"${" + match.alias + "}\"")
			used[match.alias] = true
			i += len(match.source)
			continue
		}
		if ch == '\'' || ch == '"' {
			switch quote {
			case 0:
				quote = ch
			case ch:
				quote = 0
			}
		}
		out.WriteByte(ch)
		i++
	}
	return out.String(), used
}

func headerEndLine(node *parser.Node) int {
	end := node.EndLine
	for _, doc := range node.Heredocs {
		end -= strings.Count(doc.Content, "\n") + 1
	}
	return end
}

func oldStandardFrontend(dockerfile string) bool {
	ref, _, _, ok := parser.DetectSyntax([]byte(dockerfile))
	if !ok {
		return false
	}
	ref = strings.TrimPrefix(ref, "docker.io/")
	ref = strings.TrimPrefix(ref, "index.docker.io/")
	tag, ok := strings.CutPrefix(ref, "docker/dockerfile:")
	if !ok {
		return false
	}
	tag, _, _ = strings.Cut(tag, "@")
	parts := strings.Split(tag, ".")
	if len(parts) < 2 || parts[0] != "1" {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	return err == nil && minor < 10 && !strings.Contains(tag, "-")
}

// Rename variable references without evaluating expressions or changing shell
// quoting. In particular ${K:-fallback}, escaped dollars, and single-quoted
// literals retain their original meaning in the Dockerfile frontend.
func rewriteReferences(source string, bindings map[string]string, escape rune, raw bool) string {
	var out strings.Builder
	var single, double bool
	for i := 0; i < len(source); {
		ch := source[i]
		if rune(ch) == escape && !single && i+1 < len(source) {
			out.WriteString(source[i : i+2])
			i += 2
			continue
		}
		if ch == '\'' && !double && !raw {
			single = !single
		}
		if ch == '"' && !single && !raw {
			double = !double
		}
		if ch != '$' || single {
			out.WriteByte(ch)
			i++
			continue
		}
		start := i + 1
		braced := start < len(source) && source[start] == '{'
		if braced {
			start++
		}
		end := start
		for end < len(source) && (source[end] == '_' || source[end] >= 'a' && source[end] <= 'z' || source[end] >= 'A' && source[end] <= 'Z' || source[end] >= '0' && source[end] <= '9') {
			end++
		}
		if alias, ok := bindings[source[start:end]]; ok {
			if braced {
				out.WriteString("${" + alias)
			} else {
				out.WriteString("${" + alias + "}")
			}
			i = end
		} else {
			out.WriteByte(ch)
			i++
		}
	}
	return out.String()
}

func secretID(key, value string) string {
	h := hmac.New(sha256.New, cacheKey())
	h.Write([]byte(key))
	h.Write([]byte{0})
	h.Write([]byte(value))
	return "zeabur-env-" + hex.EncodeToString(h.Sum(nil))
}

func validArgName(name string) bool {
	for i, r := range name {
		valid := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9'
		if !valid {
			return false
		}
	}
	return name != ""
}
