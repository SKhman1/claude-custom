package proxy

import (
	"sync"
	"time"
)

const (
	breakerThreshold = 5
	breakerCooldown  = 30 * time.Second
)

type circuitBreaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probing   bool
	now       func() time.Time
}

func newCircuitBreaker() *circuitBreaker {
	return &circuitBreaker{now: time.Now}
}

func (b *circuitBreaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if b.openUntil.IsZero() {
		return true
	}
	if now.Before(b.openUntil) || b.probing {
		return false
	}
	b.probing = true
	return true
}

func (b *circuitBreaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return false
	}
	return b.now().Before(b.openUntil)
}

func (b *circuitBreaker) record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if success {
		b.failures = 0
		b.openUntil = time.Time{}
		b.probing = false
		return
	}
	b.probing = false
	b.failures++
	if b.failures >= breakerThreshold {
		b.openUntil = b.now().Add(breakerCooldown)
	}
}

// recordNeutral ends a probe without changing the failure count. Rate limits,
// token limits and permission gates say nothing about provider health, so they
// must not open a circuit for a healthy provider.
func (b *circuitBreaker) recordNeutral() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probing = false
}

func (b *circuitBreaker) openRemaining() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return 0
	}
	if d := b.openUntil.Sub(b.now()); d > 0 {
		return d
	}
	return 0
}
