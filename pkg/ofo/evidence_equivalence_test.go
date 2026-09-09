package ofo

import (
	"SpeedFair_simplify/pkg/types"
	"encoding/binary"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

type evidenceFixture struct {
	state     *EvidenceState
	orders    []*types.LocalOrder
	auth      *testAuthenticator
	admission *testAdmission
	n         uint64
	limit     int
}

func makeEvidenceFixture(t testing.TB, n uint64, oldCount, newCount int, seed int64) evidenceFixture {
	t.Helper()
	auth, admission := newTestAuthenticator(t, n), newTestAdmission()
	state := NewEvidenceState(1, n, [32]byte{1})
	rng := rand.New(rand.NewSource(seed))
	ids := make([]types.TxID, oldCount+newCount+1)
	for i := range ids {
		canonical := make([]byte, 512)
		binary.BigEndian.PutUint64(canonical, uint64(i))
		ids[i] = types.TransactionID(canonical)
		if err := admission.Store(types.Transaction{ID: ids[i], CanonicalBytes: canonical}); err != nil {
			t.Fatal(err)
		}
	}
	// Finalized IDs consume LO positions but must not become live or contribute
	// weights again, even when they occur in extensions of several replicas.
	dead := ids[len(ids)-1]
	state.Done[dead] = DoneRecord{FragmentSeq: 0, BatchIndex: 0}
	state.DoneRoot = finalizedSetRoot(state.Done)
	var warm []*types.LocalOrder
	for r := uint64(0); r < n-1; r++ {
		old := append([]types.TxID(nil), ids[:oldCount]...)
		rng.Shuffle(len(old), func(i, j int) { old[i], old[j] = old[j], old[i] })
		// Give replicas different retained positions/visibility as well as order.
		if r%3 == 0 && len(old) > 0 {
			old = old[:len(old)-1]
		}
		warm = append(warm, signedOrder(t, auth, state, r, 1, old...))
	}
	if _, _, err := applyEvidenceReference(state, warm, 1, 1, n, 1, .9, oldCount+1, auth, admission); err != nil {
		t.Fatal(err)
	}
	var orders []*types.LocalOrder
	for r := uint64(0); r < n-1; r++ {
		fresh := append([]types.TxID(nil), ids[oldCount:oldCount+newCount]...)
		rng.Shuffle(len(fresh), func(i, j int) { fresh[i], fresh[j] = fresh[j], fresh[i] })
		if r%2 == 0 {
			fresh = append(fresh, dead)
		}
		orders = append(orders, signedOrder(t, auth, state, r, 2, fresh...))
	}
	return evidenceFixture{state, orders, auth, admission, n, newCount + 1}
}

func TestEvidenceOptimizationMatchesReference(t *testing.T) {
	for _, c := range []struct {
		n        uint64
		old, new int
	}{{5, 0, 30}, {5, 30, 30}, {5, 200, 0}, {5, 200, 1}, {10, 80, 8}, {30, 80, 3}} {
		for seed := int64(1); seed <= 3; seed++ {
			t.Run(fmt.Sprintf("n%d_old%d_new%d_seed%d", c.n, c.old, c.new, seed), func(t *testing.T) {
				f := makeEvidenceFixture(t, c.n, c.old, c.new, seed)
				want := f.state.clone()
				wn, wp, err := applyEvidenceReference(want, f.orders, 1, 2, f.n, 1, .9, f.limit, f.auth, f.admission)
				if err != nil {
					t.Fatal(err)
				}
				for _, touches := range []bool{true, false} {
					got := f.state.clone()
					gn, gp, err := applyEvidenceWithTouches(got, f.orders, 1, 2, f.n, 1, .9, f.limit, f.auth, f.admission, touches)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gn, wn) {
						t.Fatal("evidence state or changed-node set differs from baseline")
					}
					if touches && !reflect.DeepEqual(gp, wp) {
						t.Fatal("graph touch set differs from baseline")
					}
					if !touches && gp != nil {
						t.Fatal("follower allocated graph touches")
					}
					// Rebuild the same predecessor graph, then use each touch set.
					base := NewDependencyManager(f.n, 1, .9)
					allPairs := make(map[pairKey]bool)
					for key := range f.state.weights {
						allPairs[makePair(key.u, key.v)] = true
					}
					base.refresh(f.state, f.state.Live, allPairs)
					wm, gm := base.clone(), base.clone()
					wm.refresh(want, wn, wp)
					// The follower has no graph, so use the equal baseline touches
					// only to compare the derived output from its evidence state.
					if !touches {
						gp = wp
					}
					gm.refresh(got, gn, gp)
					if !reflect.DeepEqual(gm, wm) {
						t.Fatal("graph differs")
					}
					wo, wc, err := wm.buildProposal(want)
					if err != nil {
						t.Fatal(err)
					}
					go_, gc, err := gm.buildProposal(got)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(go_, wo) || !reflect.DeepEqual(gc, wc) {
						t.Fatal("final order or certificate differs")
					}
					if err := verifyStructuralCertificate(got, wo, wc, f.n, 1, .9); err != nil {
						t.Fatal(err)
					}
					wpost, gpost := want.clone(), got.clone()
					finalize(wpost, wm, wo, wc.Part)
					finalize(gpost, gm, go_, gc.Part)
					if !reflect.DeepEqual(gpost, wpost) {
						t.Fatal("post state differs")
					}
					wf := &types.VerifiableFairOrderFragment{Epoch: 1, FragmentSeq: 2, Evidence: f.orders, FinalOrder: wo, Certificate: wc, CandidateEvidenceStateID: want.StateID, CandidatePostStateID: wpost.StateID}
					gf := *wf
					gf.FinalOrder = go_
					gf.Certificate = gc
					gf.CandidateEvidenceStateID = got.StateID
					gf.CandidatePostStateID = gpost.StateID
					if types.FragmentDigest(&gf) != types.FragmentDigest(wf) {
						t.Fatal("fragment digest differs")
					}
				}
			})
		}
	}
}

func TestFollowerEvidenceStillRejectsInvalidInput(t *testing.T) {
	for _, kind := range []string{"signature", "duplicate", "predecessor", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			f := makeEvidenceFixture(t, 5, 10, 3, 1)
			switch kind {
			case "signature":
				f.orders[0].Signature[0] ^= 1
			case "duplicate":
				f.orders[1] = f.orders[0]
			case "predecessor":
				f.orders[0].PrevPosition++
			case "unavailable":
				delete(f.admission.content, f.orders[1].OrderedTxs[0])
			}
			want, got := f.state.clone(), f.state.clone()
			_, _, we := applyEvidenceReference(want, f.orders, 1, 2, 5, 1, .9, f.limit, f.auth, f.admission)
			_, _, ge := applyEvidenceWithTouches(got, f.orders, 1, 2, 5, 1, .9, f.limit, f.auth, f.admission, false)
			if we == nil || ge == nil || we.Error() != ge.Error() || !reflect.DeepEqual(want, got) {
				t.Fatal("validation differs from baseline")
			}
		})
	}
}

func BenchmarkEvidenceEnumeration(b *testing.B) {
	// Fixture setup is outside timed loops; baseline uses the frozen old code.
	for _, c := range []struct{ old, new int }{{0, 200}, {1000, 10}, {1000, 0}} {
		for _, mode := range []string{"baseline", "leader", "follower"} {
			b.Run(fmt.Sprintf("old%d_new%d/%s", c.old, c.new, mode), func(b *testing.B) {
				f := makeEvidenceFixture(b, 5, c.old, c.new, 1)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					state := f.state.clone()
					b.StartTimer()
					var err error
					if mode == "baseline" {
						_, _, err = applyEvidenceReference(state, f.orders, 1, 2, f.n, 1, .9, f.limit, f.auth, f.admission)
					} else {
						_, _, err = applyEvidenceWithTouches(state, f.orders, 1, 2, f.n, 1, .9, f.limit, f.auth, f.admission, mode == "leader")
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
