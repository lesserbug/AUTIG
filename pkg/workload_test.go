package main

import (
	"SpeedFair_simplify/pkg/network"
	"SpeedFair_simplify/pkg/types"
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"
)

func TestTransactionTarget(t *testing.T) {
	for _, tt := range []struct {
		rate    int
		elapsed time.Duration
		want    int64
	}{
		{0, time.Second, 0},
		{1000, -time.Second, 0},
		{1000, time.Millisecond - 1, 0},
		{1000, time.Millisecond, 1},
		{700, 1500 * time.Millisecond, 1050},
		{1000, 12345 * time.Microsecond, 12},
		{10000, 24 * time.Hour, 864000000},
	} {
		if got := transactionTarget(tt.rate, tt.elapsed); got != tt.want {
			t.Errorf("rate=%d elapsed=%s: got %d, want %d", tt.rate, tt.elapsed, got, tt.want)
		}
	}
}

// An occasional blocking send must not permanently erase the arrivals due
// during that pause. This deliberately loses many ticker events, without
// demanding sub-millisecond scheduling accuracy from the test host.
func TestSubmissionCatchesUpAfterSendPause(t *testing.T) {
	for _, tt := range []struct{ rate, nodes int }{
		{700, 5}, {1000, 5}, {1000, 10}, {1000, 20}, {1000, 50}, {5000, 5},
	} {
		t.Run(fmt.Sprintf("rate%d/nodes%d", tt.rate, tt.nodes), func(t *testing.T) {
			start := time.Now()
			const duration = 500 * time.Millisecond
			deadline := start.Add(duration)
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			var submitted int32
			var failed int64
			var sends int
			var current *types.Transaction
			net := &adapterTestNetwork{send: func(m network.Message) bool {
				tx := m.Payload.(*types.Transaction)
				sequence := sends/tt.nodes + 1
				if m.Type != "Transaction" || m.From != 0 || m.To != uint64(sends%tt.nodes) {
					t.Fatalf("unexpected fanout message: %+v", m)
				}
				if m.To == 0 {
					current = tx
					if len(tx.CanonicalBytes) != 512 || types.TransactionID(tx.CanonicalBytes) != tx.ID || binary.BigEndian.Uint64(tx.CanonicalBytes[:8]) != uint64(sequence) {
						t.Fatal("invalid transaction identity or size")
					}
					if tx.SubmissionTime.Before(start) || !tx.SubmissionTime.Before(deadline) || tx.SubmissionTime.After(time.Now()) {
						t.Fatal("submission timestamp outside the real measurement window")
					}
					if int64(sequence) > transactionTarget(tt.rate, tx.SubmissionTime.Sub(start)) {
						t.Fatal("generator ran ahead of the cumulative target")
					}
				} else if tx != current {
					t.Fatal("replicas did not receive the same transaction")
				}
				if sends == 0 {
					time.Sleep(100 * time.Millisecond)
				}
				sends++
				return true
			}}
			submitTransactions(ctx, net, 0, uint64(tt.nodes), tt.rate, 512, &submitted, &failed, start)
			target := transactionTarget(tt.rate, duration)
			if int64(submitted) < target*95/100 || int64(submitted) > target {
				t.Fatalf("did not recover after pause: submitted=%d target=%d", submitted, target)
			}
			if sends != int(submitted)*tt.nodes || failed != 0 {
				t.Fatalf("sends=%d submitted=%d failed=%d", sends, submitted, failed)
			}
			t.Logf("actual offered rate %.2f tx/s after a 100ms send pause", float64(submitted)/duration.Seconds())
		})
	}
}

func TestSubmissionUsesMeasurementStartAndHonorsCancellation(t *testing.T) {
	invoked := time.Now()
	// Simulate a generator goroutine that was scheduled late.
	start := invoked.Add(-time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), invoked.Add(250*time.Millisecond))
	defer cancel()
	var submitted int32
	var failed int64
	sends := 0
	net := &adapterTestNetwork{send: func(m network.Message) bool {
		sends++
		if m.Payload.(*types.Transaction).SubmissionTime.Before(invoked) {
			t.Fatal("catch-up backdated the submission timestamp")
		}
		if submitted == 500 && m.To == 0 {
			cancel() // Finish this fanout, but do not start transaction 501.
		}
		return m.To != 1
	}}
	submitTransactions(ctx, net, 0, 5, 1000, 16, &submitted, &failed, start)
	if submitted != 500 || sends != 2500 || failed != 500 {
		t.Fatalf("submitted=%d sends=%d failed=%d", submitted, sends, failed)
	}
}

func TestSubmissionDoesNotFabricateLoadWhenSendingIsSaturated(t *testing.T) {
	start := time.Now()
	deadline := start.Add(80 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	var submitted int32
	var failed int64
	sends := 0
	net := &adapterTestNetwork{send: func(m network.Message) bool {
		if !m.Payload.(*types.Transaction).SubmissionTime.Before(deadline) {
			t.Fatal("generated after cutoff to make up the missing load")
		}
		if m.To == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		sends++
		return true
	}}
	submitTransactions(ctx, net, 0, 5, 1000, 16, &submitted, &failed, start)
	if submitted == 0 || submitted >= 40 || sends != int(submitted)*5 || failed != 0 {
		t.Fatalf("submitted=%d sends=%d failed=%d", submitted, sends, failed)
	}
}

func TestSubmissionZeroRateAndExpiredWindow(t *testing.T) {
	for _, rate := range []int{0, 1000} {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		var submitted int32
		var failed int64
		net := &adapterTestNetwork{send: func(network.Message) bool {
			t.Fatal("unexpected send")
			return false
		}}
		submitTransactions(ctx, net, 0, 5, rate, 16, &submitted, &failed, time.Now().Add(-2*time.Second))
		cancel()
		if submitted != 0 || failed != 0 {
			t.Fatal("counted submissions outside the workload window")
		}
	}
}
