package plantools

import (
	"strings"
	"testing"
)

func TestBoundedLogBufferPreservesEdgesWithinMemoryLimit(t *testing.T) {
	t.Parallel()

	buffer := newBoundedLogBuffer(128)
	input := "begin\n" + strings.Repeat("middle\n", 100) + "fatal: end\n"
	if _, err := buffer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	result := buffer.String()
	if !strings.HasPrefix(result, "begin\n") || !strings.HasSuffix(result, "fatal: end\n") {
		t.Fatalf("bounded log lost diagnostic edges: %q", result)
	}
	if !strings.Contains(result, "build log truncated") {
		t.Fatalf("bounded log omitted truncation notice: %q", result)
	}
	if len(buffer.head)+len(buffer.tail) > 128 {
		t.Fatalf("retained %d bytes, limit is 128", len(buffer.head)+len(buffer.tail))
	}
}
