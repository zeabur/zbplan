package zbplan

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestBudgetedModelEnforcesSharedRequestLimit(t *testing.T) {
	t.Parallel()

	model := newBudgetedModel(&scriptedToolCallingModel{}, 1)
	withTools, err := model.WithTools(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withTools.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "first"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "second"}}); !errors.Is(err, ErrModelBudgetExceeded) {
		t.Fatalf("expected model budget error, got %v", err)
	}
	if model.calls() != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls())
	}
}

func TestToolBudgetEnforcesCallLimitAndTimeout(t *testing.T) {
	t.Parallel()

	budget := newToolBudget(1, 1, 10*time.Millisecond)
	endpoint := budget.middleware().Invokable(func(ctx context.Context, _ *compose.ToolInput) (*compose.ToolOutput, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := endpoint(context.Background(), &compose.ToolInput{Name: "slow"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if _, err := endpoint(context.Background(), &compose.ToolInput{Name: "second"}); !errors.Is(err, ErrToolBudgetExceeded) {
		t.Fatalf("expected tool budget error, got %v", err)
	}
	if budget.count() != 1 {
		t.Fatalf("tool calls = %d, want 1", budget.count())
	}
}

func TestLimitsRejectNegativeValues(t *testing.T) {
	t.Parallel()

	_, err := (Limits{MaxToolCalls: -1}).normalized()
	if err == nil {
		t.Fatal("expected negative limit rejection")
	}
}

func TestToolBudgetHoldsConcurrencySlotForStreamingTool(t *testing.T) {
	t.Parallel()

	finish := make(chan struct{})
	defer close(finish)
	budget := newToolBudget(2, 1, time.Second)
	endpoint := budget.middleware().Streamable(func(context.Context, *compose.ToolInput) (*compose.StreamToolOutput, error) {
		reader, writer := schema.Pipe[string](1)
		go func() {
			defer writer.Close()
			writer.Send("chunk", nil)
			<-finish
		}()
		return &compose.StreamToolOutput{Result: reader}, nil
	})

	first, err := endpoint(context.Background(), &compose.ToolInput{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Result.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := endpoint(ctx, &compose.ToolInput{Name: "blocked"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected second stream to wait for concurrency slot, got %v", err)
	}
}
