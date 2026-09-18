package zbplan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultMaxToolOutputBytes = 12 * 1024
	maxToolOutputReadBytes    = 8 * 1024
)

type toolOutputStore struct {
	mu       sync.RWMutex
	nextRef  uint64
	outputs  map[string]string
	callRefs map[string]string
}

func newToolOutputStore() *toolOutputStore {
	return &toolOutputStore{
		outputs:  make(map[string]string),
		callRefs: make(map[string]string),
	}
}

func (s *toolOutputStore) save(callID, output string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextRef++
	ref := fmt.Sprintf("out-%d", s.nextRef)
	s.outputs[ref] = output
	if callID != "" {
		s.callRefs[callID] = ref
	}
	return ref
}

func (s *toolOutputStore) refForCall(callID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ref, ok := s.callRefs[callID]
	return ref, ok
}

func (s *toolOutputStore) get(ref string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	output, ok := s.outputs[ref]
	return output, ok
}

func newToolOutputMiddleware(store *toolOutputStore, logger *slog.Logger) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				logger.DebugContext(ctx, "tool call", "name", input.Name, "args", input.Arguments)
				out, err := next(ctx, input)
				if err != nil {
					logger.ErrorContext(ctx, "tool error", "name", input.Name, "error", err)
					return nil, err
				}

				ref := store.save(input.CallID, out.Result)
				out.Result = boundedToolOutput(out.Result, ref, defaultMaxToolOutputBytes)
				snippet := boundedPrefix(out.Result, 100)
				if len(snippet) < len(out.Result) {
					snippet += "..."
				}
				logger.DebugContext(ctx, "tool result", "name", input.Name, "result", snippet)
				return out, nil
			}
		},
	}
}

func boundedToolOutput(output, ref string, maxBytes int) string {
	if len(output) <= maxBytes {
		return output
	}

	notice := fmt.Sprintf("\n\n[Tool output truncated: %d bytes total. Full output is %q; use read_tool_output with that ref and a byte offset to inspect more.]\n\n", len(output), ref)
	previewBytes := maxBytes - len(notice)
	if previewBytes <= 0 {
		return boundedPrefix(notice, maxBytes)
	}

	headBytes := previewBytes / 2
	tailBytes := previewBytes - headBytes
	return boundedPrefix(output, headBytes) + notice + boundedSuffix(output, tailBytes)
}

func boundedPrefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func boundedSuffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:]
}

func newToolHistoryRewriter(store *toolOutputStore) react.MessageModifier {
	return func(_ context.Context, messages []*schema.Message) []*schema.Message {
		latestToolRound := len(messages)
		for latestToolRound > 0 && messages[latestToolRound-1].Role == schema.Tool {
			latestToolRound--
		}

		for _, message := range messages[:latestToolRound] {
			if message.Role != schema.Tool || strings.HasPrefix(message.Content, "[Earlier tool output compacted.") {
				continue
			}
			ref, ok := store.refForCall(message.ToolCallID)
			if !ok {
				continue
			}
			message.Content = fmt.Sprintf("[Earlier tool output compacted. Use read_tool_output with ref %q if it is needed again.]", ref)
			message.MultiContent = nil
			message.UserInputMultiContent = nil
		}
		return messages
	}
}

type readToolOutputTool struct {
	store *toolOutputStore
}

func newReadToolOutputTool(store *toolOutputStore) tool.InvokableTool {
	return &readToolOutputTool{store: store}
}

func (t *readToolOutputTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "read_tool_output",
		Desc: "Reads a byte range from a prior tool result after it was truncated or compacted. Use the ref shown in the tool result notice. The returned chunk is capped at 8192 bytes.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"ref":    {Type: schema.String, Desc: "Tool output reference, for example 'out-1'.", Required: true},
			"offset": {Type: schema.Integer, Desc: "Zero-based byte offset. Defaults to 0."},
			"limit":  {Type: schema.Integer, Desc: "Maximum bytes to return. Defaults to and is capped at 8192."},
		}),
	}, nil
}

func (t *readToolOutputTool) InvokableRun(_ context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	var args struct {
		Ref    string `json:"ref"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if argsJSON == "" {
		argsJSON = "{}"
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("unmarshal: %w", err)
	}
	if args.Ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if args.Offset < 0 {
		return "", fmt.Errorf("offset must not be negative")
	}
	if args.Limit < 0 {
		return "", fmt.Errorf("limit must not be negative")
	}
	if args.Limit == 0 || args.Limit > maxToolOutputReadBytes {
		args.Limit = maxToolOutputReadBytes
	}

	output, ok := t.store.get(args.Ref)
	if !ok {
		return "", fmt.Errorf("unknown tool output ref %q", args.Ref)
	}
	if args.Offset >= len(output) {
		return fmt.Sprintf("[%s: offset %d is past the %d-byte output]", args.Ref, args.Offset, len(output)), nil
	}

	start := args.Offset
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	end := min(start+args.Limit, len(output))
	for end > start && end < len(output) && !utf8.RuneStart(output[end]) {
		end--
	}

	return fmt.Sprintf("[%s bytes %d:%d of %d]\n%s", args.Ref, start, end, len(output), output[start:end]), nil
}
