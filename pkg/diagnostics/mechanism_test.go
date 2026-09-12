package diagnostics

import (
	"testing"
	"time"
)

func TestMechanismWindowAndTrafficAccounting(t *testing.T) {
	m := &Mechanism{}
	m.Count("lo_fresh", 99) // Setup is excluded.
	now := time.Now()
	m.Begin(now, now.Add(time.Minute))
	m.Count("lo_fresh", 2)
	m.Observe("lo_fresh_ids", 10)
	m.Observe("lo_fresh_ids", 30)
	m.Network("BenchmarkReady", false, true, 999)
	m.Network("LocalOrder", false, true, 100)
	m.Network("LocalOrder", false, false, 7) // Partial failed write still costs bytes.
	m.Network("LocalOrder", true, true, 0)
	m.Network("Transaction", false, true, 200)
	m.Network("AUTIGCandidate", false, true, 300)
	r := m.Report(3)
	if r.Replica != 3 || r.WindowSeconds != 60 || r.Counts["lo_fresh"] != 2 ||
		r.Counts["network_local_order_messages"] != 1 || r.Counts["network_local_order_failures"] != 1 ||
		r.Counts["network_local_order_bytes"] != 107 || r.Counts["self_local_order_messages"] != 1 ||
		r.Counts["network_transaction_bytes"] != 200 || r.Counts["network_protocol_bytes"] != 300 {
		t.Fatalf("incorrect accounting: %+v", r)
	}
	if r.Samples["lo_fresh_ids"] != (Sample{Count: 2, Sum: 40, Max: 30, Last: 30}) {
		t.Fatalf("incorrect samples: %+v", r.Samples)
	}
	// Mutating a returned snapshot must not corrupt subsequent measurements.
	r.Counts["lo_fresh"] = 999
	if m.Report(3).Counts["lo_fresh"] != 2 {
		t.Fatal("snapshot aliases live counters")
	}
	m.mu.Lock()
	m.end = now.Add(-time.Second)
	m.mu.Unlock()
	m.Count("lo_fresh", 100)
	m.Observe("lo_fresh_ids", 100)
	m.Network("LocalOrder", false, true, 100)
	r = m.Report(3)
	if r.Counts["lo_fresh"] != 2 || r.Samples["lo_fresh_ids"].Count != 2 || r.Counts["network_local_order_bytes"] != 107 {
		t.Fatal("post-cutoff events entered the measurement")
	}
}
