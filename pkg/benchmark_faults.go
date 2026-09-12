package main

import (
	"context"
	"fmt"
	"time"
)

func resolveByzantineCount(n, f uint64, requested int64, delay time.Duration) (uint64, error) {
	if n == 0 || f >= n {
		return 0, fmt.Errorf("require 0 <= f < n")
	}
	if delay < 0 {
		return 0, fmt.Errorf("byzantine-lo-delay must be nonnegative")
	}
	if requested == -1 {
		return f, nil
	}
	if requested < 0 || uint64(requested) > f {
		return 0, fmt.Errorf("byzantine-count must be between 0 and f (or -1 to use f)")
	}
	return uint64(requested), nil
}

// Only the LO producer goroutine waits. Message reception, candidate verification
// and commit delivery remain live. Every attempt, including retransmissions,
// gets the same extra wait; no delayed messages or goroutines accumulate.
func waitForLocalOrder(ctx context.Context, finished <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-finished:
		return false
	case <-timer.C:
	}
	// Cancellation/finish may become ready alongside the timer.
	select {
	case <-ctx.Done():
		return false
	case <-finished:
		return false
	default:
		return true
	}
}
