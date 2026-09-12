package diagnostics

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// Mechanism collects small, in-memory aggregates; it never emits per-event logs.
// Events are attributed to their completion time in this process's experiment window.
type Mechanism struct {
	mu         sync.Mutex
	start, end time.Time
	counts     map[string]uint64
	samples    map[string]Sample
}

type Sample struct {
	Count uint64 `json:"count"`
	Sum   uint64 `json:"sum"`
	Max   uint64 `json:"max"`
	Last  uint64 `json:"last"`
}

type MechanismReport struct {
	Replica       uint64            `json:"replica"`
	WindowSeconds float64           `json:"window_seconds"`
	Counts        map[string]uint64 `json:"counts"`
	Samples       map[string]Sample `json:"samples"`
}

// Begin is called before releasing the local experiment-start barrier.
func (m *Mechanism) Begin(start, end time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.start, m.end = start, end
	m.counts = make(map[string]uint64)
	m.samples = make(map[string]Sample)
}

func (m *Mechanism) active() bool {
	now := time.Now()
	return !m.start.IsZero() && !now.Before(m.start) && now.Before(m.end)
}

func (m *Mechanism) Count(name string, value uint64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active() {
		m.counts[name] += value
	}
}

func (m *Mechanism) Observe(name string, value uint64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active() {
		return
	}
	s := m.samples[name]
	s.Count++
	s.Sum += value
	s.Last = value
	if value > s.Max {
		s.Max = value
	}
	m.samples[name] = s
}

func (m *Mechanism) Network(kind string, local, success bool, bytes uint64) {
	if m == nil {
		return
	}
	// Setup and finish-barrier traffic is not protocol traffic.
	if kind == "BenchmarkReady" || kind == "BenchmarkStart" || kind == "BenchmarkFinish" {
		return
	}
	group := "protocol"
	if kind == "Transaction" {
		group = "transaction"
	}
	if kind == "LocalOrder" {
		group = "local_order"
	}
	prefix := "network_" + group
	if local {
		prefix = "self_" + group
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.active() {
		return
	}
	if success {
		m.counts[prefix+"_messages"]++
	} else {
		m.counts[prefix+"_failures"]++
	}
	// Includes bytes accepted by Write on failed encodes; excludes TCP/IP headers.
	m.counts[prefix+"_bytes"] += bytes
}

func (m *Mechanism) Report(replica uint64) MechanismReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := MechanismReport{Replica: replica, WindowSeconds: m.end.Sub(m.start).Seconds(), Counts: make(map[string]uint64), Samples: make(map[string]Sample)}
	for k, v := range m.counts {
		r.Counts[k] = v
	}
	for k, v := range m.samples {
		r.Samples[k] = v
	}
	return r
}

func PrintJSON(marker string, value interface{}) {
	encoded, err := json.Marshal(value)
	if err == nil {
		log.Printf("BENCHMARK %s %s", marker, encoded)
	}
}
