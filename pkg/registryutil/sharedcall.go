package registryutil

import (
	"context"
	"fmt"
	"sync"
)

// sharedCalls deduplicates concurrent calls by key, like singleflight, but
// gives the shared work its own lifetime: the work keeps running while any
// caller is still waiting and is canceled only after every caller has left.
// One caller's timeout or cancellation therefore never fails another caller
// whose context is still valid.
type sharedCalls[V any] struct {
	mu    sync.Mutex
	calls map[string]*sharedCall[V]
}

type sharedCall[V any] struct {
	done    chan struct{}
	val     V
	err     error
	waiters int
	cancel  context.CancelFunc
}

// Do runs fn once per key among overlapping callers. fn receives a context
// that keeps the first caller's values but not its deadline or cancellation.
func (g *sharedCalls[V]) Do(ctx context.Context, key string, fn func(context.Context) (V, error)) (V, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*sharedCall[V])
	}
	call, ok := g.calls[key]
	if !ok {
		workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		call = &sharedCall[V]{done: make(chan struct{}), cancel: cancel}
		g.calls[key] = call
		go g.run(workCtx, key, call, fn)
	}
	call.waiters++
	g.mu.Unlock()

	select {
	case <-call.done:
		return call.val, call.err
	case <-ctx.Done():
		g.mu.Lock()
		call.waiters--
		if call.waiters == 0 {
			call.cancel()
			// A later caller must start fresh instead of joining canceled work.
			if g.calls[key] == call {
				delete(g.calls, key)
			}
		}
		g.mu.Unlock()
		var zero V
		return zero, ctx.Err()
	}
}

func (g *sharedCalls[V]) run(ctx context.Context, key string, call *sharedCall[V], fn func(context.Context) (V, error)) {
	defer func() {
		if recovered := recover(); recovered != nil {
			call.err = fmt.Errorf("shared registry call panicked: %v", recovered)
		}
		call.cancel()
		g.mu.Lock()
		if g.calls[key] == call {
			delete(g.calls, key)
		}
		g.mu.Unlock()
		close(call.done)
	}()
	call.val, call.err = fn(ctx)
}
