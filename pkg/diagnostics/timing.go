// Package diagnostics provides opt-in benchmark timing without changing messages
// or protocol state. A Span belongs to one goroutine; timestamps use its local clock.
package diagnostics

import (
	"encoding/json"
	"log"
	"sync/atomic"
	"time"
)

var enabled atomic.Bool

func Enable(value bool) { enabled.Store(value) }

type Span struct {
	start  time.Time
	fields map[string]interface{}
}

func Start(event string, replica, fragment uint64) *Span {
	if !enabled.Load() {
		return nil
	}
	now := time.Now()
	return &Span{start: now, fields: map[string]interface{}{
		"event": event, "replica": replica, "fragment_seq": fragment,
		"start_unix_ns": now.UnixNano(),
	}}
}

// Mark records microseconds since span start, not an additive stage duration.
func (s *Span) Mark(name string) {
	if s != nil {
		s.fields[name+"_us"] = time.Since(s.start).Microseconds()
	}
}

func (s *Span) Set(name string, value interface{}) {
	if s != nil {
		s.fields[name] = value
	}
}

func (s *Span) Finish() {
	if s == nil {
		return
	}
	s.Mark("total")
	encoded, err := json.Marshal(s.fields)
	if err == nil {
		log.Printf("BENCHMARK STAGE %s", encoded)
	}
}
