package zbplan

import (
	"context"
	"testing"
)

type closeRecorder struct{ closed bool }

func (c *closeRecorder) Write(p []byte) (int, error) { return len(p), nil }
func (c *closeRecorder) Close() error                { c.closed = true; return nil }

func TestRunClosesOCIOutputOnEarlyFailure(t *testing.T) {
	t.Parallel()

	output := &closeRecorder{}
	if _, err := Run(context.Background(), Config{OCIOutput: output}); err == nil {
		t.Fatal("expected missing Model to fail")
	}
	if !output.closed {
		t.Fatal("Run returned without closing OCIOutput")
	}
}
