package zbplan

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

func TestToolOutputMiddlewareBoundsAndPreservesLargeResult(t *testing.T) {
	store := newToolOutputStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	middleware := newToolOutputMiddleware(store, logger)
	largeOutput := "begin\n" + strings.Repeat("0123456789", 2000) + "\nend"
	endpoint := middleware.Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: largeOutput}, nil
	})

	result, err := endpoint(context.Background(), &compose.ToolInput{Name: "large", CallID: "call-1", Arguments: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Result) > defaultMaxToolOutputBytes {
		t.Fatalf("bounded output has %d bytes, limit is %d", len(result.Result), defaultMaxToolOutputBytes)
	}
	if !strings.Contains(result.Result, "use read_tool_output") || !strings.Contains(result.Result, "begin") || !strings.Contains(result.Result, "end") {
		t.Fatalf("bounded output does not preserve a useful preview: %q", result.Result)
	}

	ref, ok := store.refForCall("call-1")
	if !ok {
		t.Fatal("large output was not retained")
	}
	stored, ok := store.get(ref)
	if !ok || stored != largeOutput {
		t.Fatal("retained output differs from the original")
	}
}

func TestToolOutputStoreEvictsWithinByteBudget(t *testing.T) {
	store := newToolOutputStore(16)
	first := store.save("a", "12345678")
	second := store.save("b", "abcdefgh")

	if first == "" || second == "" {
		t.Fatalf("expected retained refs, got %q and %q", first, second)
	}
	if _, ok := store.get(first); ok {
		t.Fatal("oldest output was not evicted")
	}
	if got, ok := store.get(second); !ok || got != "abcdefgh" {
		t.Fatalf("latest output = %q, %v", got, ok)
	}
	if store.size() > 16 {
		t.Fatalf("store retained %d bytes, limit is 16", store.size())
	}
	if ref := store.save("oversized", "01234567890123456789"); ref != "" {
		t.Fatalf("oversized output received ref %q", ref)
	}
}

func TestToolHistoryRewriterCompactsOnlyLargeEarlierResults(t *testing.T) {
	store := newToolOutputStore()
	largeResult := strings.Repeat("large", defaultMaxToolOutputBytes)
	largeRef := store.save("large-call", largeResult)
	store.save("small-call", "small manifest")
	store.save("page-call", "paged output")
	store.save("new-call", "new full result")
	messages := []*schema.Message{
		{Role: schema.User, Content: "plan"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "large-call"}, {ID: "small-call"}, {ID: "page-call"}}},
		{Role: schema.Tool, ToolCallID: "large-call", Content: boundedToolOutput(largeResult, largeRef, defaultMaxToolOutputBytes)},
		{Role: schema.Tool, ToolCallID: "small-call", Content: "small manifest"},
		{Role: schema.Tool, ToolCallID: "page-call", Content: "paged output"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "new-call"}}},
		{Role: schema.Tool, ToolCallID: "new-call", Content: "new full result"},
	}

	rewritten := newToolHistoryRewriter(store)(context.Background(), messages)
	if !strings.Contains(rewritten[2].Content, largeRef) {
		t.Fatalf("large earlier result was not replaced with its retrievable ref: %q", rewritten[2].Content)
	}
	if rewritten[3].Content != "small manifest" {
		t.Fatalf("small earlier result was unexpectedly compacted: %q", rewritten[3].Content)
	}
	if rewritten[4].Content != "paged output" {
		t.Fatalf("paged earlier result was unexpectedly compacted: %q", rewritten[4].Content)
	}
	if rewritten[6].Content != "new full result" {
		t.Fatalf("latest tool result was unexpectedly compacted: %q", rewritten[6].Content)
	}
}

func TestReadToolOutputReturnsBoundedUTF8Range(t *testing.T) {
	store := newToolOutputStore()
	ref := store.save("call-1", strings.Repeat("界", 4000))
	result, err := newReadToolOutputTool(store).InvokableRun(
		context.Background(),
		`{"ref":"`+ref+`","offset":1,"limit":20000}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(result) {
		t.Fatal("read_tool_output returned invalid UTF-8")
	}
	if len(result) > maxToolOutputReadBytes+100 {
		t.Fatalf("read_tool_output returned %d bytes", len(result))
	}
}

func TestReActAgentCompactsToolOutputBetweenModelCalls(t *testing.T) {
	store := newToolOutputStore()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chatModel := &scriptedToolCallingModel{}
	agent, err := react.NewAgent(context.Background(), &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools:               []tool.BaseTool{&largeResultTool{}},
			ToolCallMiddlewares: []compose.ToolMiddleware{newToolOutputMiddleware(store, logger)},
		},
		MessageRewriter: newToolHistoryRewriter(store),
		MaxStep:         10,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := agent.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" {
		t.Fatalf("unexpected final response %q", result.Content)
	}

	var toolResults []string
	for _, message := range chatModel.lastInput {
		if message.Role == schema.Tool {
			toolResults = append(toolResults, message.Content)
		}
	}
	if len(toolResults) != 2 {
		t.Fatalf("expected two tool results in final model input, got %d", len(toolResults))
	}
	if !strings.HasPrefix(toolResults[0], "[Earlier tool output compacted.") {
		t.Fatalf("earlier result was sent back in full: %q", toolResults[0])
	}
	if !strings.Contains(toolResults[1], "[Tool output truncated:") {
		t.Fatalf("latest result was not bounded: %q", toolResults[1])
	}
}

type scriptedToolCallingModel struct {
	calls     int
	lastInput []*schema.Message
}

func (m *scriptedToolCallingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *scriptedToolCallingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	m.lastInput = input
	if m.calls <= 2 {
		callID := fmt.Sprintf("call-%d", m.calls)
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: callID,
				Function: schema.FunctionCall{
					Name:      "large_result",
					Arguments: "{}",
				},
			}},
		}, nil
	}
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *scriptedToolCallingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("unexpected streaming call")
}

type largeResultTool struct{}

func (t *largeResultTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:        "large_result",
		Desc:        "Returns a large result.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{}),
	}, nil
}

func (t *largeResultTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "begin\n" + strings.Repeat("large output\n", 2000) + "end", nil
}

func TestBoundedBuildLogsKeepsFailureTail(t *testing.T) {
	logs := "build started\n" + strings.Repeat("progress\n", 4000) + "fatal: package missing\n"
	bounded := boundedBuildLogs(logs, 1024)
	if len(bounded) > 1024 {
		t.Fatalf("bounded logs have %d bytes", len(bounded))
	}
	if !strings.HasPrefix(bounded, "build started") || !strings.HasSuffix(bounded, "fatal: package missing\n") {
		t.Fatalf("bounded logs lost diagnostic edges: %q", bounded)
	}
	if !strings.Contains(bounded, "truncated from") {
		t.Fatalf("bounded logs do not explain truncation: %q", bounded)
	}
}
