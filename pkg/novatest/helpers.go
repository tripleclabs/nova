package novatest

import (
	"testing"
	"time"
)

// Eventually polls fn until it returns true or the timeout expires.
// Useful for waiting on async distributed system state changes.
func Eventually(t testing.TB, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("novatest: condition not met within %v", timeout)
}

// Never polls fn for the given duration and fails if it ever returns true.
// Useful for asserting that something does NOT happen (e.g., a partitioned
// node should never become leader).
func Never(t testing.TB, duration time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if fn() {
			t.Fatalf("novatest: condition unexpectedly became true within %v", duration)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
