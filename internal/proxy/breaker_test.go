package proxy

import "testing"

func TestRecordNeutralDoesNotOpenCircuit(t *testing.T) {
	breaker := newCircuitBreaker()
	for i := 0; i < breakerThreshold*3; i++ {
		breaker.recordNeutral()
	}
	if !breaker.allow() {
		t.Fatal("neutral outcomes (rate limits) opened the circuit for a healthy provider")
	}
}

func TestRepeatedFailuresOpenCircuit(t *testing.T) {
	breaker := newCircuitBreaker()
	for i := 0; i < breakerThreshold; i++ {
		breaker.record(false)
	}
	if breaker.allow() {
		t.Fatal("circuit stayed closed after repeated failures")
	}
	if breaker.openRemaining() <= 0 {
		t.Fatal("open circuit has no cooldown")
	}
	breaker.record(true)
	if !breaker.allow() {
		t.Fatal("success did not reset the circuit")
	}
}
