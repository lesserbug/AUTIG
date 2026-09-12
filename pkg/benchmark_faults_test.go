package main

import (
	"context"
	"testing"
	"time"
)

func TestByzantineCountIndependentOfFaultBound(t *testing.T) {
	for _, tc := range []struct {
		requested int64
		want      uint64
	}{
		{-1, 2}, {0, 0}, {1, 1}, {2, 2},
	} {
		got, err := resolveByzantineCount(10, 2, tc.requested, 200*time.Millisecond)
		if err != nil || got != tc.want {
			t.Fatalf("requested=%d: count=%d err=%v", tc.requested, got, err)
		}
	}
	for _, requested := range []int64{-2, 3} {
		if _, err := resolveByzantineCount(10, 2, requested, 0); err == nil {
			t.Fatalf("accepted out-of-bound count %d", requested)
		}
	}
	if _, err := resolveByzantineCount(10, 2, 1, -time.Millisecond); err == nil {
		t.Fatal("accepted negative delay")
	}
	if _, err := resolveByzantineCount(10, 10, -1, 0); err == nil {
		t.Fatal("accepted fault bound that would underflow the replica threshold")
	}
}

func TestLocalOrderDelayWaitsAndStops(t *testing.T) {
	const delay = 30 * time.Millisecond
	start := time.Now()
	if !waitForLocalOrder(context.Background(), nil, delay) || time.Since(start) < delay {
		t.Fatal("LO attempt released before its configured delay")
	}
	if !waitForLocalOrder(context.Background(), nil, 0) {
		t.Fatal("zero-delay LO attempt was suppressed")
	}
	for _, finish := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		result := make(chan bool, 1)
		go func() { result <- waitForLocalOrder(ctx, finished, time.Hour) }()
		if finish {
			close(finished)
		} else {
			cancel()
		}
		select {
		case sent := <-result:
			if sent {
				t.Error("cancelled/finished LO attempt was released")
			}
		case <-time.After(time.Second):
			t.Error("shutdown was blocked by the malicious delay")
		}
		cancel()
	}
}
