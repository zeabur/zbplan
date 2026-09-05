package buildenv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/solver/pb"
)

func TestBuildInputsDoNotBecomeImageDefaultsOrLLBEnvironment(t *testing.T) {
	const value = "FAKE_BUILD_SECRET_80f87ea"
	for _, dockerfile := range []string{
		"FROM scratch\nRUN echo hello\n",
		"FROM scratch\nRUN [\"/bin/check\", \"TOKEN\"]\n",
		"FROM scratch\nRUN <<'EOF'\n# FROM should not start a stage\necho hello\nEOF\n",
		"FROM scratch AS build\nRUN echo first\nFROM build\nRUN echo second\n",
		"FROM scratch\nARG TOKEN=some-default\nRUN echo hello\n",
		"FROM scratch\n",
	} {
		t.Run(dockerfile, func(t *testing.T) {
			prepared, err := Prepare(t.Context(), dockerfile, map[string]string{"TOKEN": value})
			if err != nil {
				t.Fatal(err)
			}
			if len(prepared.BuildArgs) != 0 {
				t.Fatalf("implicit build args: %v", prepared.BuildArgs)
			}
			if strings.Contains(prepared.Dockerfile, value) {
				t.Fatal("value in Dockerfile")
			}
			result, err := dockerfile2llb.Dockerfile2LLB(t.Context(), []byte(prepared.Dockerfile), dockerfile2llb.ConvertOpt{})
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := json.Marshal(result.Image)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(metadata), value) {
				t.Fatal("value in image config/history")
			}
			for _, env := range result.Image.Config.Env {
				if strings.HasPrefix(env, "TOKEN=") {
					t.Fatal("build-only variable in runtime defaults")
				}
			}
			definition, err := result.State.Marshal(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range definition.Def {
				if strings.Contains(string(raw), value) {
					t.Fatal("value in LLB")
				}
				var op pb.Op
				if err := op.UnmarshalVT(raw); err != nil {
					t.Fatal(err)
				}
				if exec := op.GetExec(); exec != nil {
					if len(exec.Secretenv) != 1 || exec.Secretenv[0].Name != "TOKEN" {
						t.Fatal("RUN did not receive TOKEN as a secret")
					}
				}
			}
		})
	}
}

func TestDockerfileDefaultsAndStagePrecedence(t *testing.T) {
	source := "FROM scratch AS base\nRUN first\nENV TOKEN=runtime-default\nRUN second\nFROM base AS final\nRUN third\nENV TOKEN=final-default\n"
	prepared, err := Prepare(t.Context(), source, map[string]string{"TOKEN": "build-only"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(prepared.Dockerfile, "--mount=type=secret"); got != 2 {
		t.Fatalf("mount count=%d, Dockerfile=%s", got, prepared.Dockerfile)
	}
	if !strings.Contains(prepared.Dockerfile, "RUN second") {
		t.Fatal("explicit ENV should override the build input")
	}
	result, err := dockerfile2llb.Dockerfile2LLB(t.Context(), []byte(prepared.Dockerfile), dockerfile2llb.ConvertOpt{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(result.Image.Config.Env, "\n"), "TOKEN=final-default") {
		t.Fatal("lost explicit runtime default")
	}
}

func TestCacheIdentity(t *testing.T) {
	dockerfile := "FROM scratch\nRUN build\n"
	a, err := Prepare(t.Context(), dockerfile, map[string]string{"TOKEN": "a", "EMPTY": ""})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Prepare(t.Context(), dockerfile, map[string]string{"EMPTY": "", "TOKEN": "a"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Prepare(t.Context(), dockerfile, map[string]string{"TOKEN": "b", "EMPTY": ""})
	if err != nil {
		t.Fatal(err)
	}
	if a.Dockerfile != b.Dockerfile {
		t.Fatal("identical inputs must reuse cache")
	}
	if a.Dockerfile == c.Dockerfile {
		t.Fatal("changed input must invalidate cache")
	}
}

func TestExplicitDockerfileReferences(t *testing.T) {
	source := "FROM scratch AS base\nENV PUBLIC_DIR=base-default\nFROM base\nWORKDIR /${PUBLIC_DIR}\nENV MODE=${BUILD_MODE}\nRUN check\n"
	prepared, err := Prepare(t.Context(), source, map[string]string{"PUBLIC_DIR": "app", "BUILD_MODE": "production", "UNUSED_SECRET": "private"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := prepared.BuildArgs["UNUSED_SECRET"]; ok {
		t.Fatal("unreferenced input made public")
	}
	result, err := dockerfile2llb.Dockerfile2LLB(t.Context(), []byte(prepared.Dockerfile), dockerfile2llb.ConvertOpt{Config: dockerui.Config{BuildArgs: prepared.BuildArgs}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Image.Config.WorkingDir != "/app" {
		t.Fatal("lost Dockerfile variable expansion")
	}
	if !strings.Contains(strings.Join(result.Image.Config.Env, "\n"), "MODE=production") {
		t.Fatal("lost explicit runtime configuration")
	}
	metadata, _ := json.Marshal(result.Image)
	if strings.Contains(string(metadata), "private") {
		t.Fatal("unused secret leaked")
	}
}

func TestFrontendCompatibility(t *testing.T) {
	for _, test := range []struct {
		ref     string
		bundled bool
	}{
		{"docker/dockerfile:1.4", true},
		{"docker.io/docker/dockerfile:1.9.0", true},
		{"docker/dockerfile:1.10.0", false},
		{"docker/dockerfile:1", false},
		{"docker/dockerfile:1.4-labs", false},
		{"custom/frontend:1.4", false},
	} {
		t.Run(test.ref, func(t *testing.T) {
			p, err := Prepare(t.Context(), "# syntax="+test.ref+"\nFROM scratch\nRUN true\n", map[string]string{"TOKEN": "value"})
			if err != nil {
				t.Fatal(err)
			}
			if got := p.FrontendAttrs()["cmdline"] == "dockerfile.v0"; got != test.bundled {
				t.Fatal("incorrect frontend selection")
			}
		})
	}
}

func TestPreservesSourceAndExplicitMounts(t *testing.T) {
	source := "# syntax=docker/dockerfile:1\nFROM scratch\n  rUn\t--mount=type=secret,id=user,env=TOKEN true\nRUN <<EOF\nFROM literal\nENV TOKEN=literal\nEOF\n"
	prepared, err := Prepare(t.Context(), source, map[string]string{"TOKEN": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(prepared.Dockerfile, "env=TOKEN") != 2 {
		t.Fatalf("duplicate explicit mount: %s", prepared.Dockerfile)
	}
	if !strings.Contains(prepared.Dockerfile, "FROM literal\nENV TOKEN=literal\nEOF") {
		t.Fatal("changed heredoc body")
	}
	unchanged, err := Prepare(context.Background(), source, nil)
	if err != nil || unchanged.Dockerfile != source {
		t.Fatal("empty inputs should preserve source byte-for-byte")
	}
}

func TestCopyHeredocExpansionAndQuotedLiteral(t *testing.T) {
	source := "FROM scratch\nCOPY <<EOF /expanded\n'${PUBLIC_DIR}'\nEOF\nCOPY <<'LITERAL' /literal\n'${PUBLIC_DIR}'\nLITERAL\n"
	p, err := Prepare(t.Context(), source, map[string]string{"PUBLIC_DIR": "app"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dockerfile2llb.Dockerfile2LLB(t.Context(), []byte(p.Dockerfile), dockerfile2llb.ConvertOpt{Config: dockerui.Config{BuildArgs: p.BuildArgs}})
	if err != nil {
		t.Fatal(err)
	}
	def, err := result.State.Marshal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, raw := range def.Def {
		var op pb.Op
		if err := op.UnmarshalVT(raw); err != nil {
			t.Fatal(err)
		}
		if file := op.GetFile(); file != nil {
			for _, action := range file.Actions {
				if mk := action.GetMkfile(); mk != nil {
					contents = append(contents, string(mk.Data))
				}
			}
		}
	}
	joined := strings.Join(contents, "|")
	if !strings.Contains(joined, "'app'\n") || !strings.Contains(joined, "'${PUBLIC_DIR}'\n") {
		t.Fatalf("heredoc expansion changed: %q", contents)
	}
}

func TestConfigurationPredicateDoesNotDiscloseItsInput(t *testing.T) {
	const secret = "FAKE_PREDICATE_SECRET_894f"
	p, err := Prepare(t.Context(), "FROM scratch\nENV READY=${TOKEN:+yes}\n", map[string]string{"TOKEN": secret})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dockerfile2llb.Dockerfile2LLB(t.Context(), []byte(p.Dockerfile), dockerfile2llb.ConvertOpt{Config: dockerui.Config{BuildArgs: p.BuildArgs}})
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(result.Image)
	if strings.Contains(string(metadata), secret) {
		t.Fatal("predicate input leaked")
	}
	if !strings.Contains(strings.Join(result.Image.Config.Env, "\n"), "READY=yes") {
		t.Fatal("predicate value changed")
	}
}

func TestMaterializationPreservesLiteralAndPrefixNames(t *testing.T) {
	source := "FROM scratch\nENV X=$A Y='$A' Z=$AB Q=\"prefix${A}suffix\"\n"
	p, err := Prepare(t.Context(), source, map[string]string{"A": "first", "AB": "second"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dockerfile2llb.Dockerfile2LLB(t.Context(), []byte(p.Dockerfile), dockerfile2llb.ConvertOpt{Config: dockerui.Config{BuildArgs: p.BuildArgs}})
	if err != nil {
		t.Fatalf("%v\n%s", err, p.Dockerfile)
	}
	got := strings.Join(result.Image.Config.Env, "\n")
	for _, want := range []string{"X=first", "Y=$A", "Z=second", "Q=prefixfirstsuffix"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s: %s", want, got)
		}
	}
}

func TestContinuedMountDoesNotRewriteShellReferences(t *testing.T) {
	source := "FROM scratch\nRUN --mount=type=cache,\\\ntarget=$CACHE_DIR \\\nsh -c 'export CACHE_DIR=local; test \"$CACHE_DIR\" = local'\n"
	p, err := Prepare(t.Context(), source, map[string]string{"CACHE_DIR": "/tmp/cache"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Dockerfile, "export CACHE_DIR=local; test \"$CACHE_DIR\" = local") {
		t.Fatal("modified runtime shell substitution")
	}
	_, err = dockerfile2llb.Dockerfile2LLB(t.Context(), []byte(p.Dockerfile), dockerfile2llb.ConvertOpt{Config: dockerui.Config{BuildArgs: p.BuildArgs}})
	if err != nil {
		t.Fatalf("continued mount: %v\n%s", err, p.Dockerfile)
	}
}
