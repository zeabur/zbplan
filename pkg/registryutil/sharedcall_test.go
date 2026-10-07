package registryutil

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSharedCallsSurviveFirstCallerCancellation(t *testing.T) {
	t.Parallel()

	var calls sharedCalls[string]
	started := make(chan struct{})
	release := make(chan struct{})
	workErr := make(chan error, 1)
	fn := func(ctx context.Context) (string, error) {
		close(started)
		select {
		case <-release:
			return "tags", nil
		case <-ctx.Done():
			workErr <- ctx.Err()
			return "", ctx.Err()
		}
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := calls.Do(firstCtx, "repo", fn)
		firstDone <- err
	}()
	<-started

	secondDone := make(chan string, 1)
	go func() {
		val, err := calls.Do(context.Background(), "repo", func(context.Context) (string, error) {
			t.Error("second caller must join the in-flight call")
			return "", nil
		})
		if err != nil {
			t.Errorf("second caller failed: %v", err)
		}
		secondDone <- val
	}()
	waitForWaiters(t, &calls, "repo", 2)

	cancelFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller error = %v, want context.Canceled", err)
	}
	select {
	case err := <-workErr:
		t.Fatalf("shared work was canceled with a waiter remaining: %v", err)
	default:
	}

	close(release)
	if got := <-secondDone; got != "tags" {
		t.Fatalf("second caller got %q, want tags", got)
	}
}

func TestSharedCallsCancelWorkWhenEveryCallerLeaves(t *testing.T) {
	t.Parallel()

	var calls sharedCalls[int]
	started := make(chan struct{})
	workCanceled := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := calls.Do(ctx, "repo", func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			close(workCanceled)
			return 0, ctx.Err()
		})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller error = %v, want context.Canceled", err)
	}
	select {
	case <-workCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("shared work kept running after every caller left")
	}

	// A new caller starts fresh rather than joining the abandoned call.
	val, err := calls.Do(context.Background(), "repo", func(context.Context) (int, error) { return 7, nil })
	if err != nil || val != 7 {
		t.Fatalf("fresh call = %d, %v; want 7, nil", val, err)
	}
}

func TestSharedCallsRecoverPanics(t *testing.T) {
	t.Parallel()

	var calls sharedCalls[int]
	if _, err := calls.Do(context.Background(), "repo", func(context.Context) (int, error) { panic("boom") }); err == nil {
		t.Fatal("expected panic to become an error")
	}
}

func waitForWaiters[V any](t *testing.T, calls *sharedCalls[V], key string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		calls.mu.Lock()
		call := calls.calls[key]
		got := 0
		if call != nil {
			got = call.waiters
		}
		calls.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d waiters", want)
}
