package ofo

import (
	"SpeedFair_simplify/pkg/diagnostics"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// These flags belong only to the Go test binary, never to the production node.
var (
	ablationNodes   = flag.String("ablation-nodes", "10,50", "Logical replica counts")
	ablationFaults  = flag.Uint64("ablation-f", 1, "Use the final main-experiment fault parameter")
	ablationGamma   = flag.Float64("ablation-gamma", .9, "Use the final main-experiment gamma")
	ablationSeeds   = flag.String("ablation-seeds", "1,7,19", "Fixed sample seeds")
	ablationHistory = flag.Int("ablation-history", 480, "Retained transactions in large synthetic cases (at least 6)")
	ablationOrder   = flag.String("ablation-order", "AB", "Branch execution order: AB or BA")
)

var ablationSink any

func ablationIntegers(t testing.TB, value string) []int64 {
	t.Helper()
	var result []int64
	for _, part := range strings.Split(value, ",") {
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, v)
	}
	return result
}

func BenchmarkAblationGraphMaintenance(b *testing.B)     { benchmarkAblation(b, true) }
func BenchmarkAblationFollowerVerification(b *testing.B) { benchmarkAblation(b, false) }

func benchmarkAblation(b *testing.B, graph bool) {
	diagnostics.Enable(false)
	if *ablationHistory < 6 {
		b.Fatal("ablation-history must be at least 6")
	}
	if *ablationOrder != "AB" && *ablationOrder != "BA" {
		b.Fatal("ablation-order must be AB or BA")
	}
	for _, n := range ablationIntegers(b, *ablationNodes) {
		if n < 2 {
			b.Fatal("need at least two logical replicas")
		}
		for _, c := range ablationCases(*ablationHistory) {
			for _, seed := range ablationIntegers(b, *ablationSeeds) {
				name := fmt.Sprintf("n%d_f%d_g%g/%s/seed%d", n, *ablationFaults, *ablationGamma, c.name, seed)
				b.Run(name, func(b *testing.B) {
					x := makeAblationFixture(b, uint64(n), *ablationFaults, *ablationGamma, c, seed)
					// Preflight runs for the exact benchmark-sized sample. All state,
					// graph, certificate and commit assertions precede leaf timers.
					checkAblationGraph(b, x)
					checkAblationVerifier(b, x, x.candidate.fragment, 0, true)
					for _, full := range []bool{false, true} {
						level := "Core"
						if full {
							level = "Full"
						}
						branches := []bool{false, true}
						if *ablationOrder == "BA" {
							branches = []bool{true, false}
						}
						for _, baseline := range branches {
							branch := "Certificate"
							if graph {
								branch = "Incremental"
							}
							if baseline {
								branch = "Recompute"
								if graph {
									branch = "Rebuild"
								}
							}
							b.Run(level+"/"+branch, func(b *testing.B) { benchmarkAblationBranch(b, x, graph, full, baseline) })
						}
					}
				})
			}
		}
	}
}

func benchmarkAblationBranch(b *testing.B, x *ablationFixture, graph, full, baseline bool) {
	s := x.service(graph)
	pre := x.base.committed.clone()
	nodes, pairs, err := applyEvidence(pre, x.orders, s.authContext.Epoch, pre.FragmentSeq+1, s.replicaCount, s.fFaulty, s.gamma, s.loMaxSize, s.auth, s.admission)
	if err != nil {
		b.Fatal(err)
	}
	f := x.candidate.fragment
	b.ReportAllocs()
	b.ResetTimer()
	b.StopTimer()
	for i := 0; i < b.N; i++ {
		// The committed snapshot is immutable; production operations clone it
		// inside their timed call. Only pending/reset scaffolding is untimed.
		s.pending = nil
		var manager *DependencyManager
		var touches map[pairKey]bool
		if graph && !full && !baseline {
			manager = x.base.UtigManager.clone()
			touches = make(map[pairKey]bool, len(pairs))
			for pair := range pairs {
				touches[pair] = true
			}
		}
		var result any
		var opErr error
		accepted := true
		b.StartTimer()
		switch {
		case graph && full:
			if baseline {
				result, opErr = constructCandidateByRebuildForTest(s, x.orders)
			} else {
				result, opErr = s.constructCandidate(x.orders)
			}
		case graph:
			if baseline {
				manager = rebuildDependencyManagerForTest(pre, s.replicaCount, s.fFaulty, s.gamma)
			} else {
				manager.refresh(pre, nodes, touches)
			}
			result = manager
		case full:
			if baseline {
				accepted = verifyCandidateByRecomputeForTest(s, f, 0)
			} else {
				accepted = s.verifyCandidate(f, 0)
			}
			result = s.pending
		default:
			if baseline {
				manager = rebuildDependencyManagerForTest(pre, s.replicaCount, s.fFaulty, s.gamma)
				opErr = verifyRecomputedOutputForTest(pre, manager, f.FinalOrder, f.Certificate)
			} else {
				opErr = verifyStructuralCertificate(pre, f.FinalOrder, f.Certificate, s.replicaCount, s.fFaulty, s.gamma)
			}
		}
		b.StopTimer()
		ablationSink = result
		if opErr != nil || !accepted {
			b.Fatalf("operation failed: accepted=%t err=%v", accepted, opErr)
		}
	}
	for name, value := range x.metrics {
		b.ReportMetric(value, name)
	}
}
