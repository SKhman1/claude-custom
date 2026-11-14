package admission

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrOverloaded = errors.New("admission queue is full")

type Limiter struct {
	mu          sync.Mutex
	times       []time.Time
	limit       int
	window      time.Duration
	sem         chan struct{}
	maxWaiters  int
	waiters     int
	waitersLock sync.Mutex
}

func New(limit int, window time.Duration, concurrency int) *Limiter {
	if limit < 1 {
		limit = 1
	}
	if window <= 0 {
		window = time.Second
	}
	if concurrency < 1 {
		concurrency = 1
	}
	return &Limiter{
		limit:      limit,
		window:     window,
		sem:        make(chan struct{}, concurrency),
		maxWaiters: 16,
	}
}

// Acquire blocks until a request slot is available. The returned function must
// be called exactly once.
func (l *Limiter) Acquire(ctx context.Context) (func(), error) {
	if !l.reserveWaiter() {
		return nil, ErrOverloaded
	}
	defer l.releaseWaiter()

	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	releaseSem := func() { <-l.sem }
	if err := l.waitWindow(ctx); err != nil {
		releaseSem()
		return nil, err
	}

	var once sync.Once
	return func() { once.Do(releaseSem) }, nil
}

func (l *Limiter) reserveWaiter() bool {
	l.waitersLock.Lock()
	defer l.waitersLock.Unlock()
	if l.waiters >= l.maxWaiters {
		return false
	}
	l.waiters++
	return true
}

func (l *Limiter) releaseWaiter() {
	l.waitersLock.Lock()
	l.waiters--
	l.waitersLock.Unlock()
}

func (l *Limiter) waitWindow(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		cutoff := now.Add(-l.window)
		kept := l.times[:0]
		for _, t := range l.times {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		l.times = kept
		if len(l.times) < l.limit {
			l.times = append(l.times, now)
			l.mu.Unlock()
			return nil
		}
		wait := l.times[0].Add(l.window).Sub(now)
		l.mu.Unlock()
		if wait < time.Millisecond {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *Limiter) Waiting() int {
	l.waitersLock.Lock()
	defer l.waitersLock.Unlock()
	return l.waiters
}
