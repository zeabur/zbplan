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

func TestToolHistoryRewriterDiscardsEvictedLargePreview(t *testing.T) {
	store := newToolOutputStore(defaultMaxToolOutputBytes + 128)
	largeResult := strings.Repeat("x", defaultMaxToolOutputBytes+1)
	oldRef := store.save("old-call", largeResult)
	if oldRef == "" {
		t.Fatal("old large result was not initially retained")
	}
	if ref := store.save("new-call", largeResult); ref == "" {
		t.Fatal("new large result was not retained")
	}
	if _, ok := store.get(oldRef); ok {
		t.Fatal("old large result was not evicted")
	}

	messages := []*schema.Message{
		{Role: schema.User, Content: "plan"},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "old-call"}}},
		{Role: schema.Tool, ToolCallID: "old-call", Content: boundedToolOutput(largeResult, oldRef, defaultMaxToolOutputBytes)},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "new-call"}}},
		{Role: schema.Tool, ToolCallID: "new-call", Content: boundedToolOutput(largeResult, "", defaultMaxToolOutputBytes)},
	}

	rewritten := newToolHistoryRewriter(store)(context.Background(), messages)
	if rewritten[2].Content != "[Earlier tool output discarded after its retention budget was exhausted.]" {
		t.Fatalf("evicted large preview was not discarded: %q", rewritten[2].Content)
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

func TestReadToolOutputLimitBelowRuneStillAdvances(t *testing.T) {
	store := newToolOutputStore()
	ref := store.save("call-1", "界a")
	result, err := newReadToolOutputTool(store).InvokableRun(
		context.Background(),
		`{"ref":"`+ref+`","offset":0,"limit":1}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result, "["+ref+" bytes 0:3 of 4]\n界") {
		t.Fatalf("page did not advance past the first rune: %q", result)
	}
}

func TestReadToolOutputPagesDoNotEvictSource(t *testing.T) {
	const sourceCallID = "source-call"
	payload := strings.Repeat("0123456789", 2000)
	store := newToolOutputStore(len(payload) + 128)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	middleware := newToolOutputMiddleware(store, logger)
	source := middleware.Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: payload}, nil
	})
	if _, err := source(context.Background(), &compose.ToolInput{Name: "read", CallID: sourceCallID, Arguments: "{}"}); err != nil {
		t.Fatal(err)
	}
	ref, ok := store.refForCall(sourceCallID)
	if !ok {
		t.Fatal("source output was not retained")
	}
	reader := newReadToolOutputTool(store)
	page := middleware.Invokable(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		result, err := reader.InvokableRun(ctx, input.Arguments)
		return &compose.ToolOutput{Result: result}, err
	})
	var recovered strings.Builder
	var messages []*schema.Message
	for offset := 0; offset < len(payload); offset += maxToolOutputReadBytes {
		callID := fmt.Sprintf("page-%d", offset)
		result, err := page(context.Background(), &compose.ToolInput{
			Name:      "read_tool_output",
			CallID:    callID,
			Arguments: fmt.Sprintf(`{"ref":%q,"offset":%d}`, ref, offset),
		})
		if err != nil {
			t.Fatalf("read page at offset %d: %v", offset, err)
		}
		_, content, ok := strings.Cut(result.Result, "\n")
		if !ok {
			t.Fatalf("page lacks content: %q", result.Result)
		}
		recovered.WriteString(content)
		messages = append(messages,
			&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: callID}}},
			&schema.Message{Role: schema.Tool, ToolCallID: callID, Content: result.Result},
		)
	}
	if recovered.String() != payload {
		t.Fatal("paging did not recover the complete source output")
	}
	messages = append(messages, &schema.Message{Role: schema.Assistant, Content: "done"})
	rewritten := newToolHistoryRewriter(store)(context.Background(), messages)
	recovered.Reset()
	for _, message := range rewritten {
		if message.Role == schema.Tool {
			_, content, _ := strings.Cut(message.Content, "\n")
			recovered.WriteString(content)
		}
	}
	if recovered.String() != payload {
		t.Fatal("paged knowledge was lost from later model input")
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

func TestToolOutputCatalogKeepsNewestEntriesWithinBudget(t *testing.T) {
	store := newToolOutputStore()
	oldRef := store.saveCall("call-old", "read", `{"path":"old"}`, strings.Repeat("o", defaultMaxToolOutputBytes+1))
	newRef := store.saveCall("call-new", "read", `{"path":"new"}`, strings.Repeat("n", defaultMaxToolOutputBytes+1))

	catalog := store.catalog(len("- " + newRef + ": read {\"path\":\"new\"}\n"))
	if !strings.Contains(catalog, newRef) || strings.Contains(catalog, oldRef) {
		t.Fatalf("catalog should keep only the newest entry, got %q", catalog)
	}
}

func TestToolOutputStoreForgetsReusedCallIDWhenNewResultIsTooLarge(t *testing.T) {
	store := newToolOutputStore(defaultMaxToolOutputBytes * 2)
	if ref := store.saveCall("call-1", "read", "{}", strings.Repeat("a", defaultMaxToolOutputBytes+1)); ref == "" {
		t.Fatal("first result was not retained")
	}
	if ref := store.saveCall("call-1", "read", "{}", strings.Repeat("b", defaultMaxToolOutputBytes*3)); ref != "" {
		t.Fatal("oversized result was retained")
	}
	if _, ok := store.callRefs["call-1"]; ok {
		t.Fatal("reused call ID still points at the earlier result")
	}
}
