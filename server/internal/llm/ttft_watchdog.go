package llm

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type providerTTFTWatchdogKey struct{}

// providerTTFTWatchdog is armed by doProviderRequest immediately before the
// first upstream HTTP call. That keeps fallback_ttft_sec scoped to provider API
// request -> first streamed event, excluding RAG retrieval, context assembly and
// local provider payload construction.
type providerTTFTWatchdog struct {
	timeout time.Duration
	cancel  context.CancelFunc
	first   <-chan struct{}
	stalled *atomic.Bool
	done    chan struct{}
	armOnce sync.Once
	endOnce sync.Once
}

func newProviderTTFTWatchdog(timeout time.Duration, cancel context.CancelFunc, first <-chan struct{}, stalled *atomic.Bool) *providerTTFTWatchdog {
	return &providerTTFTWatchdog{
		timeout: timeout,
		cancel:  cancel,
		first:   first,
		stalled: stalled,
		done:    make(chan struct{}),
	}
}

func contextWithProviderTTFTWatchdog(ctx context.Context, w *providerTTFTWatchdog) context.Context {
	if w == nil {
		return ctx
	}
	return context.WithValue(ctx, providerTTFTWatchdogKey{}, w)
}

func armProviderTTFTWatchdog(ctx context.Context) {
	w, _ := ctx.Value(providerTTFTWatchdogKey{}).(*providerTTFTWatchdog)
	if w == nil {
		return
	}
	w.arm()
}

func (w *providerTTFTWatchdog) arm() {
	if w == nil || w.timeout <= 0 || w.cancel == nil || w.stalled == nil {
		return
	}
	w.armOnce.Do(func() {
		go func() {
			timer := time.NewTimer(w.timeout)
			defer timer.Stop()
			select {
			case <-w.first:
			case <-w.done:
			case <-timer.C:
				w.stalled.Store(true)
				w.cancel()
			}
		}()
	})
}

func (w *providerTTFTWatchdog) stop() {
	if w == nil {
		return
	}
	w.endOnce.Do(func() {
		close(w.done)
	})
}
