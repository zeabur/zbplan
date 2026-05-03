package builder_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zeabur/zbplan/lib/builder"
)

var validArgNameRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func TestEncodeArgName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantSame bool // if true, expect output == input
	}{
		{"simple name", "FOO", true},
		{"underscore name", "FOO_BAR", true},
		{"leading underscore", "_FOO", true},
		{"dotted name", "auth.jwt.secret", false},
		{"hyphenated name", "my-var", false},
		{"starts with digit", "1abc", false},
		{"spaces", "my var", false},
		{"complex dots", "spring..datasource_url", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := builder.EncodeArgName(tt.input)
			if tt.wantSame {
				assert.Equal(t, tt.input, got)
			} else {
				t.Logf("encoded name %q is different from input %q", got, tt.input)
				assert.NotEqual(t, tt.input, got)
			}
			assert.Regexp(t, validArgNameRegex, got, "encoded name %q must be a valid ARG name", got)
		})
	}
}

func TestEncodeArgName_Deterministic(t *testing.T) {
	name := "auth.jwt.secret"
	a := builder.EncodeArgName(name)
	b := builder.EncodeArgName(name)
	assert.Equal(t, a, b)
}

func TestEncodeArgName_UniqueForDifferentInputs(t *testing.T) {
	a := builder.EncodeArgName("auth.jwt.secret")
	b := builder.EncodeArgName("auth.jwt.key")
	assert.NotEqual(t, a, b)
}

func TestPutToEveryLayer(t *testing.T) {
	tests := []struct {
		name       string
		dockerfile string
		content    string
		want       string
	}{
		{
			name: "single FROM statement",
			dockerfile: `FROM golang:1.20
RUN go build -o app`,
			content: "ENV FOO=bar",
			want: `FROM golang:1.20
ENV FOO=bar
RUN go build -o app
`,
		},
		{
			name: "multiple FROM statements (multi-stage build)",
			dockerfile: `FROM golang:1.20 AS builder
RUN go build -o app

FROM alpine:latest
COPY --from=builder /app /app`,
			content: "ENV FOO=bar",
			want: `FROM golang:1.20 AS builder
ENV FOO=bar
RUN go build -o app

FROM alpine:latest
ENV FOO=bar
COPY --from=builder /app /app
`,
		},
		{
			name: "FROM with lowercase",
			dockerfile: `from golang:1.20
RUN go build`,
			content: "ENV FOO=bar",
			want: `from golang:1.20
ENV FOO=bar
RUN go build
`,
		},
		{
			name: "multiple lines of content",
			dockerfile: `FROM golang:1.20
RUN go build`,
			content: "ENV FOO=bar\nARG VERSION=1.0",
			want: `FROM golang:1.20
ENV FOO=bar
ARG VERSION=1.0
RUN go build
`,
		},
		{
			name:       "empty dockerfile",
			dockerfile: "",
			content:    "ENV FOO=bar",
			want:       "",
		},
		{
			name:       "dockerfile without FROM",
			dockerfile: "RUN echo hello",
			content:    "ENV FOO=bar",
			want:       "RUN echo hello\n",
		},
		{
			name:       "Bad instruction (e.g. FROMED)",
			dockerfile: `FROMED golang:1.20`,
			content:    "ENV FOO=bar",
			want:       "FROMED golang:1.20\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := builder.PutToEveryLayer(tt.dockerfile, tt.content)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEnvProcessor_Process(t *testing.T) {
	tests := []struct {
		name       string
		variables  []string
		dockerfile string
		want       string
		wantCheck  func(t *testing.T, got string)
	}{
		{
			name:       "single variable",
			variables:  []string{"FOO"},
			dockerfile: "FROM alpine:latest\nRUN echo hello",
			want:       "FROM alpine:latest\nARG ZEABUR_ENV_FOO\nENV FOO=${ZEABUR_ENV_FOO}\n\nRUN echo hello\n",
		},
		{
			name:       "multiple variables",
			variables:  []string{"FOO", "BAR"},
			dockerfile: "FROM alpine:latest\nRUN echo hello",
			want:       "FROM alpine:latest\nARG ZEABUR_ENV_FOO ZEABUR_ENV_BAR\nENV FOO=${ZEABUR_ENV_FOO} BAR=${ZEABUR_ENV_BAR}\n\nRUN echo hello\n",
		},
		{
			name:       "variable with dots",
			variables:  []string{"auth.jwt.secret"},
			dockerfile: "FROM alpine:latest\nRUN echo hello",
			wantCheck: func(t *testing.T, got string) {
				assert.Contains(t, got, "ENV auth.jwt.secret=${ZEABUR_ENV_")
				assert.NotContains(t, got, "ZEABUR_ENV_auth.jwt.secret")
				// Extract ARG name and verify it's valid
				for line := range strings.SplitSeq(got, "\n") {
					if after, ok := strings.CutPrefix(line, "ARG "); ok {
						assert.Regexp(t, validArgNameRegex, after)
					}
				}
			},
		},
		{
			name:       "mixed valid and dotted variables",
			variables:  []string{"FOO", "auth.jwt.secret"},
			dockerfile: "FROM alpine:latest\nRUN echo hello",
			wantCheck: func(t *testing.T, got string) {
				assert.Contains(t, got, "ZEABUR_ENV_FOO")
				assert.Contains(t, got, "ENV FOO=${ZEABUR_ENV_FOO}")
				assert.Contains(t, got, "ENV FOO=${ZEABUR_ENV_FOO} auth.jwt.secret=${ZEABUR_ENV_")
				assert.NotContains(t, got, "ZEABUR_ENV_auth.jwt.secret")
			},
		},
		{
			name:       "no variables",
			variables:  []string{},
			dockerfile: "FROM alpine:latest\nRUN echo hello",
			want:       "FROM alpine:latest\n\nRUN echo hello\n",
		},
		{
			name:       "empty dockerfile",
			variables:  []string{"FOO"},
			dockerfile: "",
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &builder.EnvProcessor{Variables: tt.variables}
			got, err := p.Process(context.Background(), tt.dockerfile)
			assert.NoError(t, err)
			if tt.wantCheck != nil {
				tt.wantCheck(t, got)
			} else {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

type errorProcessor struct{}

func (e *errorProcessor) Process(ctx context.Context, dockerfile string) (string, error) {
	return "", assert.AnError
}

func TestPipelineProcessor_Process(t *testing.T) {
	t.Run("multiple processors", func(t *testing.T) {
		p1 := &builder.EnvProcessor{Variables: []string{"FOO"}}
		p2 := &builder.EnvProcessor{Variables: []string{"BAR"}}
		pipeline := builder.NewPipelineProcessor(p1, p2)
		dockerfile := "FROM alpine:latest\nRUN echo hello"
		got, err := pipeline.Process(context.Background(), dockerfile)
		assert.NoError(t, err)
		want := "FROM alpine:latest\nARG ZEABUR_ENV_BAR\nENV BAR=${ZEABUR_ENV_BAR}\n\nARG ZEABUR_ENV_FOO\nENV FOO=${ZEABUR_ENV_FOO}\n\nRUN echo hello\n"
		assert.Equal(t, want, got)
	})

	t.Run("single processor", func(t *testing.T) {
		p1 := &builder.EnvProcessor{Variables: []string{"FOO"}}
		pipeline := builder.NewPipelineProcessor(p1)
		dockerfile := "FROM alpine:latest\nRUN echo hello"
		got, err := pipeline.Process(context.Background(), dockerfile)
		assert.NoError(t, err)
		want := "FROM alpine:latest\nARG ZEABUR_ENV_FOO\nENV FOO=${ZEABUR_ENV_FOO}\n\nRUN echo hello\n"
		assert.Equal(t, want, got)
	})

	t.Run("no processors", func(t *testing.T) {
		pipeline := builder.NewPipelineProcessor()
		dockerfile := "FROM alpine:latest\nRUN echo hello"
		got, err := pipeline.Process(context.Background(), dockerfile)
		assert.NoError(t, err)
		assert.Equal(t, dockerfile, got)
	})

	t.Run("processor returns error", func(t *testing.T) {
		pipeline := builder.NewPipelineProcessor(&errorProcessor{})
		_, err := pipeline.Process(context.Background(), "FROM alpine")
		assert.Error(t, err)
	})
}
