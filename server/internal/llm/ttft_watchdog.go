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
// request -> first response byte, excluding RAG retrieval, context assembly and
// local provider payload construction.
//
// "First byte" (not "first meaningful token") is deliberate: a relay/gateway in
// front of the real model reports its OWN TTFT as time-to-first-byte from the
// true upstream, which can be much earlier than the first non-empty text/
// thinking delta Aivory parses out of the stream (reasoning models in
// particular can go quiet for a long stretch after an early framing byte,
// e.g. `response.created`, before any real content streams). Gating the
// watchdog on parsed content instead of on raw bytes made fallback_ttft_sec
// fire even though the upstream — and the relay's own dashboard — considered
// the connection healthy the whole time. Firing on the first byte lines
// Aivory's measurement up with what an admin can actually verify externally.
type providerTTFTWatchdog struct {
	timeout   time.Duration
	cancel    context.CancelFunc
	first     chan struct{}
	firstOnce sync.Once
	stalled   *atomic.Bool
	done      chan struct{}
	armOnce   sync.Once
	endOnce   sync.Once
}

func newProviderTTFTWatchdog(timeout time.Duration, cancel context.CancelFunc, stalled *atomic.Bool) *providerTTFTWatchdog {
	return &providerTTFTWatchdog{
		timeout: timeout,
		cancel:  cancel,
		first:   make(chan struct{}),
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

// markProviderTTFTFirstByte disarms the watchdog the moment ANY byte of the
// upstream HTTP response body is read — regardless of whether it parses into
// meaningful content. Called from doProviderRequest's response reader, once
// per doProviderRequest call site (primary + the channel-fallback retry), so
// either attempt satisfies it.
func markProviderTTFTFirstByte(ctx context.Context) {
	w, _ := ctx.Value(providerTTFTWatchdogKey{}).(*providerTTFTWatchdog)
	if w == nil {
		return
	}
	w.markFirstByte()
}

// markFirstByte is safe to call multiple times / concurrently; only the first
// call has any effect.
func (w *providerTTFTWatchdog) markFirstByte() {
	if w == nil {
		return
	}
	w.firstOnce.Do(func() { close(w.first) })
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
