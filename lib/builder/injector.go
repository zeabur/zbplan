package builder

import (
	"bufio"
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
)

var validArgNameRegex = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EncodeArgName encodes a variable name into a valid Docker ARG name.
// Docker ARG names must match ^[A-Za-z_][A-Za-z0-9_]*$.
// If the variable name is already valid, it is returned as-is.
// Otherwise, it is hashed using FNV-64a.
func EncodeArgName(name string) string {
	if validArgNameRegex.MatchString(name) {
		return name
	}
	h := fnv.New64a()
	h.Write([]byte(name))
	return fmt.Sprintf("h%x", h.Sum64())
}

type Processor interface {
	Process(ctx context.Context, dockerfile string) (string, error)
}

type PipelineProcessor struct {
	Processors []Processor
}

func NewPipelineProcessor(processors ...Processor) *PipelineProcessor {
	return &PipelineProcessor{
		Processors: processors,
	}
}

func (p *PipelineProcessor) Process(ctx context.Context, dockerfile string) (string, error) {
	var err error
	newDockerfile := dockerfile

	for _, processor := range p.Processors {
		newDockerfile, err = processor.Process(ctx, newDockerfile)
		if err != nil {
			return "", err
		}
	}

	return newDockerfile, nil
}

type EnvProcessor struct {
	Variables []string
}

// Process injects ARG and ENV statements to the Dockerfile.
//
// It will inject the following statements:
//
//	ARG ZEABUR_ENV_XXX ZEABUR_ENV_YYY, ZEABUR_ENV_ZZZ
//	ENV XXX=${ZEABUR_ENV_XXX} YYY=${ZEABUR_ENV_YYY} ZZZ=${ZEABUR_ENV_ZZZ}
//
// So you can pass the following build arguments to the build command:
//
//	ZEABUR_ENV_XXX=xxx ZEABUR_ENV_YYY=yyy ZEABUR_ENV_ZZZ=zzz
//
// And the following environment variables will be set in the container:
//
//	XXX=xxx YYY=yyy ZZZ=zzz
func (p *EnvProcessor) Process(ctx context.Context, dockerfile string) (string, error) {
	sb := strings.Builder{}

	if len(p.Variables) > 0 {
		// args
		sb.WriteString("ARG ")
		for i, variable := range p.Variables {
			sb.WriteString("ZEABUR_ENV_" + EncodeArgName(variable))
			if i != len(p.Variables)-1 {
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n")

		// env
		sb.WriteString("ENV ")
		for i, variable := range p.Variables {
			sb.WriteString(variable)
			sb.WriteString("=${ZEABUR_ENV_")
			sb.WriteString(EncodeArgName(variable))
			sb.WriteString("}")
			if i != len(p.Variables)-1 {
				sb.WriteString(" ")
			}
		}
		sb.WriteString("\n")
	}

	return PutToEveryLayer(dockerfile, sb.String())
}

func PutToEveryLayer(dockerfile string, content string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(dockerfile))
	output := strings.Builder{}

	for scanner.Scan() {
		line := scanner.Text()
		upperLine := strings.ToUpper(line)
		if strings.HasPrefix(upperLine, "FROM ") {
			output.WriteString(line)
			output.WriteString("\n")
			output.WriteString(content)
			output.WriteString("\n")
		} else {
			output.WriteString(line)
			output.WriteString("\n")
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return output.String(), nil
}
