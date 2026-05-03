package main

import (
	"flag"
	"strings"
)

type MapFlag map[string]string

var _ flag.Value = (*MapFlag)(nil)

func (m *MapFlag) String() string {
	var sb strings.Builder

	for k, v := range *m {
		sb.WriteString(k)
		sb.WriteRune('=')
		sb.WriteString(v)
		sb.WriteRune(';')
	}

	return sb.String()
}

func (m *MapFlag) Set(text string) error {
	flag := make(map[string]string)

	for line := range strings.SplitSeq(text, ";") {
		if len(line) == 0 {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		if len(kv) != 2 {
			continue
		}

		flag[kv[0]] = kv[1]
	}

	*m = flag
	return nil
}
