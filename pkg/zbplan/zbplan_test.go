package zbplan

import (
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestInvokableOnlyHidesOtherToolInterfaces(t *testing.T) {
	t.Parallel()

	var wrapped tool.BaseTool = invokableOnly{}
	if _, ok := wrapped.(tool.StreamableTool); ok {
		t.Fatal("wrapper exposes StreamableTool")
	}
	if _, ok := wrapped.(tool.EnhancedInvokableTool); ok {
		t.Fatal("wrapper exposes EnhancedInvokableTool")
	}
	if _, ok := wrapped.(tool.EnhancedStreamableTool); ok {
		t.Fatal("wrapper exposes EnhancedStreamableTool")
	}
}
