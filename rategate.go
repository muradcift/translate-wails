package main

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// Process-wide rate-limit gate shared by translation and TTS.
//
// The keyless endpoint occasionally answers 429. Previously every chunk retried
// on its own, so one rate limit could fan out into a burst of parallel retries
// (thundering herd) that extended the limit. A 429 now pauses every request for
// a short cooldown; requests that were already about to fire observe the pause
// and wait instead of amplifying the burst.
//
// The gate is invisible on the success path: with no cooldown active wait
// returns immediately and no timer is created.
const (
	rateGateDefaultCooldown = 2 * time.Second
	rateGateMaxCooldown     = 8 * time.Second
	rateGateJitterMax       = 200 * time.Millisecond
	rateGateRetryJitterPct  = 0.2
)

type rateGate struct {
	mu    sync.Mutex
	until time.Time
}

var sharedRateGate rateGate

// pause pushes the cooldown deadline to now+d, but only when that is later
// than the current deadline. The mutex is never held while sleeping: a waiter
// only reads until under the lock and then sleeps on its own timer.
func (g *rateGate) pause(d time.Duration) {
	if d <= 0 {
		return
	}
	if d > rateGateMaxCooldown {
		d = rateGateMaxCooldown
	}
	until := time.Now().Add(d)
	g.mu.Lock()
	if until.After(g.until) {
		g.until = until
	}
	g.mu.Unlock()
}

// wait blocks while a cooldown is active. It reports waited=true when it
// actually had to sleep at least once, which callers use to spread wake-ups
// with extra jitter. A cancelled context aborts the wait and returns its
// error.
//
// The deadline is re-read after every timer fire: another goroutine may have
// called pause while this waiter was sleeping and pushed the cooldown beyond
// the deadline it originally observed.
func (g *rateGate) wait(ctx context.Context) (bool, error) {
	waited := false
	for {
		g.mu.Lock()
		until := g.until
		g.mu.Unlock()

		delay := time.Until(until)
		if delay <= 0 {
			return waited, nil
		}

		t := time.NewTimer(delay)
		select {
		case <-t.C:
			waited = true
		case <-ctx.Done():
			t.Stop()
			return waited, ctx.Err()
		}
	}
}

// rateGateCooldown turns an optional Retry-After value into the pause length:
// the header value when it is positive, otherwise the 2s default.
func rateGateCooldown(retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	return rateGateDefaultCooldown
}

// sleepJitter sleeps a random duration in [0, max], honouring cancellation.
func sleepJitter(ctx context.Context, max time.Duration) error {
	if max <= 0 {
		return nil
	}
	d := time.Duration(rand.Int63n(int64(max) + 1))
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// jitterDuration spreads d by ±pct so retries do not line up in lockstep.
func jitterDuration(d time.Duration, pct float64) time.Duration {
	spread := int64(float64(d) * pct)
	if spread <= 0 || d <= 0 {
		return d
	}
	delta := rand.Int63n(2*spread+1) - spread
	return time.Duration(int64(d) + delta)
}
