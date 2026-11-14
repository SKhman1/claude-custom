package admission

import (
	"context"
	"testing"
	"time"
)

func TestLimiterWindow(t *testing.T) {
	l := New(2, 150*time.Millisecond, 1)
	ctx := context.Background()
	start := time.Now()
	for i := 0; i < 2; i++ {
		release, err := l.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	release, err := l.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("third acquire returned too early: %v", elapsed)
	}
}

func TestLimiterConcurrency(t *testing.T) {
	l := New(100, time.Second, 1)
	release, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(ctx); err == nil {
		t.Fatal("expected timeout waiting for concurrency slot")
	}
	release()
}

func TestLimiterWaiterCap(t *testing.T) {
	l := New(1, time.Second, 1)
	release, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < l.maxWaiters; i++ {
		l.reserveWaiter()
	}
	if _, err := l.Acquire(context.Background()); err != ErrOverloaded {
		t.Fatalf("err = %v, want ErrOverloaded", err)
	}
	release()
}
