package zbplan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

var (
	ErrModelBudgetExceeded = errors.New("model request budget exceeded")
	ErrToolBudgetExceeded  = errors.New("tool call budget exceeded")
)

type modelBudget struct {
	max   int64
	calls atomic.Int64
}

type budgetedModel struct {
	inner  model.ToolCallingChatModel
	budget *modelBudget
}

func newBudgetedModel(inner model.ToolCallingChatModel, maxRequests int) *budgetedModel {
	return &budgetedModel{
		inner:  inner,
		budget: &modelBudget{max: int64(maxRequests)},
	}
}

func (m *budgetedModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	inner, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &budgetedModel{inner: inner, budget: m.budget}, nil
}

func (m *budgetedModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if err := m.take(); err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, input, opts...)
}

func (m *budgetedModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := m.take(); err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, input, opts...)
}

func (m *budgetedModel) take() error {
	for {
		calls := m.budget.calls.Load()
		if calls >= m.budget.max {
			return fmt.Errorf("%w: maximum %d", ErrModelBudgetExceeded, m.budget.max)
		}
		if m.budget.calls.CompareAndSwap(calls, calls+1) {
			return nil
		}
	}
}

func (m *budgetedModel) calls() int {
	return int(m.budget.calls.Load())
}

type toolBudget struct {
	max     int64
	timeout time.Duration
	calls   atomic.Int64
	slots   chan struct{}
}

func newToolBudget(maxCalls, maxParallel int, timeout time.Duration) *toolBudget {
	return &toolBudget{
		max:     int64(maxCalls),
		timeout: timeout,
		slots:   make(chan struct{}, maxParallel),
	}
}

func (b *toolBudget) middleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				ctx, release, err := b.begin(ctx)
				if err != nil {
					return nil, err
				}
				defer release()
				return next(ctx, input)
			}
		},
		Streamable: func(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
				ctx, release, err := b.begin(ctx)
				if err != nil {
					return nil, err
				}
				output, err := next(ctx, input)
				if err != nil {
					release()
					return nil, err
				}
				if output == nil || output.Result == nil {
					release()
					return nil, fmt.Errorf("streaming tool %q returned no result", input.Name)
				}

				reader, writer := schema.Pipe[string](1)
				go func() {
					defer release()
					defer output.Result.Close()
					defer writer.Close()
					for {
						chunk, recvErr := output.Result.Recv()
						if recvErr != nil {
							if !errors.Is(recvErr, io.EOF) {
								writer.Send("", recvErr)
							}
							return
						}
						if writer.Send(chunk, nil) {
							return
						}
					}
				}()
				return &compose.StreamToolOutput{Result: reader}, nil
			}
		},
	}
}

func (b *toolBudget) begin(ctx context.Context) (context.Context, func(), error) {
	for {
		calls := b.calls.Load()
		if calls >= b.max {
			return ctx, func() {}, fmt.Errorf("%w: maximum %d", ErrToolBudgetExceeded, b.max)
		}
		if b.calls.CompareAndSwap(calls, calls+1) {
			break
		}
	}

	select {
	case b.slots <- struct{}{}:
	case <-ctx.Done():
		return ctx, func() {}, ctx.Err()
	}

	if b.timeout <= 0 {
		return ctx, func() { <-b.slots }, nil
	}
	bounded, cancel := context.WithTimeout(ctx, b.timeout)
	return bounded, func() {
		cancel()
		<-b.slots
	}, nil
}

func (b *toolBudget) count() int {
	return int(b.calls.Load())
}
