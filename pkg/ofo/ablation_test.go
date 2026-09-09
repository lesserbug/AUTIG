package ofo

import (
	"SpeedFair_simplify/pkg/types"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func checkAblationVerifier(t testing.TB, x *ablationFixture, fragment *types.VerifiableFairOrderFragment, sender uint64, want bool) {
	t.Helper()
	a, b := x.service(false), x.service(false)
	snapshot := x.base.committed.clone()
	acceptedA, acceptedB := a.verifyCandidate(fragment, sender), verifyCandidateByRecomputeForTest(b, fragment, sender)
	if acceptedA != want || acceptedB != want {
		t.Fatalf("accepted production=%t recompute=%t, want %t", acceptedA, acceptedB, want)
	}
	assertAblationState(t, a.committed, snapshot)
	assertAblationState(t, b.committed, snapshot)
	if !want {
		if a.pending != nil || b.pending != nil {
			t.Fatal("rejection installed pending state")
		}
		return
	}
	if a.pending.digest != b.pending.digest {
		t.Fatal("pending digest differs")
	}
	assertAblationState(t, a.pending.preState, b.pending.preState)
	assertAblationState(t, a.pending.postState, b.pending.postState)
	for _, service := range []*OFOService{a, b} {
		if _, ok := service.CommitPending(types.FragmentDigest(fragment)); !ok {
			t.Fatal("follower commit failed")
		}
	}
	assertAblationState(t, a.committed, b.committed)
	assertAblationState(t, a.committed, x.candidate.postState)
	assertAblationState(t, x.base.committed, snapshot)
}

func TestAblationVerifierEquivalence(t *testing.T) {
	for _, n := range []uint64{10, 50} {
		for _, seed := range []int64{1, 7, 19} {
			for _, c := range ablationCases(30) {
				c.fresh = min(c.fresh, 12)
				t.Run(fmt.Sprintf("n%d/%s/seed%d", n, c.name, seed), func(t *testing.T) {
					x := makeAblationFixture(t, n, 1, .9, c, seed)
					checkAblationVerifier(t, x, x.candidate.fragment, 0, true)
				})
			}
		}
	}
}

func cloneAblationFragment(t testing.TB, f *types.VerifiableFairOrderFragment) *types.VerifiableFairOrderFragment {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var copy types.VerifiableFairOrderFragment
	if err := json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	return &copy
}

func signAblationFragment(t testing.TB, x *ablationFixture, f *types.VerifiableFairOrderFragment) {
	t.Helper()
	var err error
	f.LeaderSignature, err = x.auth.SignLeader(x.base.authContext, types.FragmentDigest(f))
	if err != nil {
		t.Fatal(err)
	}
}

func signAblationOrder(t testing.TB, x *ablationFixture, o *types.LocalOrder) {
	t.Helper()
	o.NewPosition = o.PrevPosition + uint64(len(o.OrderedTxs))
	o.NewHead = types.LocalOrderHead(o)
	var err error
	o.Signature, err = x.auth.SignReplica(o.ReplicaID, types.LocalOrderDigest(o))
	if err != nil {
		t.Fatal(err)
	}
}

// Internal mutations are re-signed and explicitly checked at the target stage.
// Comparing only top-level false would let signature failures hide a bad oracle.
func TestAblationRejectsTampering(t *testing.T) {
	x := makeAblationFixture(t, 10, 1, .9, ablationCase{name: "proof", history: 30, fresh: 6, cycle: true, release: true}, 7)
	mutations := []struct {
		name, stage string
		edit        func(*types.VerifiableFairOrderFragment)
	}{
		{"leader-signature", "signature", func(f *types.VerifiableFairOrderFragment) { f.LeaderSignature[0] ^= 1 }},
		{"leader", "context", func(f *types.VerifiableFairOrderFragment) { f.LeaderID++ }},
		{"epoch", "context", func(f *types.VerifiableFairOrderFragment) { f.Epoch++ }},
		{"auth-context", "context", func(f *types.VerifiableFairOrderFragment) { f.AuthContextID[0] ^= 1 }},
		{"sequence", "context", func(f *types.VerifiableFairOrderFragment) { f.FragmentSeq++ }},
		{"previous-state", "context", func(f *types.VerifiableFairOrderFragment) { f.PreviousStateID[0] ^= 1 }},
		{"previous-fragment", "context", func(f *types.VerifiableFairOrderFragment) { f.PreviousFragmentDigest[0] ^= 1 }},
		{"replica-signature", "evidence", func(f *types.VerifiableFairOrderFragment) { f.Evidence[0].Signature[0] ^= 1 }},
		{"sender-count", "evidence", func(f *types.VerifiableFairOrderFragment) { f.Evidence = f.Evidence[1:] }},
		{"duplicate-sender", "evidence", func(f *types.VerifiableFairOrderFragment) { f.Evidence[1] = f.Evidence[0] }},
		{"evidence-order", "evidence", func(f *types.VerifiableFairOrderFragment) {
			f.Evidence[0], f.Evidence[1] = f.Evidence[1], f.Evidence[0]
		}},
		{"evidence-context", "evidence", func(f *types.VerifiableFairOrderFragment) {
			f.Evidence[0].Epoch++
			signAblationOrder(t, x, f.Evidence[0])
		}},
		{"predecessor", "evidence", func(f *types.VerifiableFairOrderFragment) {
			f.Evidence[0].PrevHead[0] ^= 1
			signAblationOrder(t, x, f.Evidence[0])
		}},
		{"position", "evidence", func(f *types.VerifiableFairOrderFragment) { f.Evidence[0].NewPosition++ }},
		{"hash-head", "evidence", func(f *types.VerifiableFairOrderFragment) { f.Evidence[0].NewHead[0] ^= 1 }},
		{"duplicate-id", "evidence", func(f *types.VerifiableFairOrderFragment) {
			o := f.Evidence[0]
			o.OrderedTxs = append(o.OrderedTxs, o.OrderedTxs[0])
			signAblationOrder(t, x, o)
		}},
		{"second-live-position", "evidence", func(f *types.VerifiableFairOrderFragment) {
			o := f.Evidence[0]
			for id := range x.base.committed.Pos[o.ReplicaID] {
				o.OrderedTxs = append(o.OrderedTxs, id)
				break
			}
			signAblationOrder(t, x, o)
		}},
		{"unavailable", "evidence", func(f *types.VerifiableFairOrderFragment) {
			f.Evidence[0].OrderedTxs[0] = types.TransactionID([]byte("missing"))
			signAblationOrder(t, x, f.Evidence[0])
		}},
		{"oversize", "evidence", func(f *types.VerifiableFairOrderFragment) {
			o := f.Evidence[0]
			o.OrderedTxs = make([]types.TxID, x.base.loMaxSize+1)
			signAblationOrder(t, x, o)
		}},
		{"evidence-state", "prestate", func(f *types.VerifiableFairOrderFragment) { f.CandidateEvidenceStateID[0] ^= 1 }},
		{"post-state", "poststate", func(f *types.VerifiableFairOrderFragment) { f.CandidatePostStateID[0] ^= 1 }},
		{"nil-order", "core", func(f *types.VerifiableFairOrderFragment) { f.FinalOrder = nil }},
		{"nil-certificate", "core", func(f *types.VerifiableFairOrderFragment) { f.Certificate = nil }},
		{"omit-safe-output", "core", func(f *types.VerifiableFairOrderFragment) { f.FinalOrder.Batches = nil; f.Certificate.Part = nil }},
		{"batch-index", "core", func(f *types.VerifiableFairOrderFragment) { f.FinalOrder.Batches[0].Index++ }},
		{"batch-order", "core", func(f *types.VerifiableFairOrderFragment) {
			f.FinalOrder.Batches[0], f.FinalOrder.Batches[1] = f.FinalOrder.Batches[1], f.FinalOrder.Batches[0]
			f.Certificate.Part[0], f.Certificate.Part[1] = f.Certificate.Part[1], f.Certificate.Part[0]
			for i := range f.Certificate.Part {
				f.Certificate.Part[i].Rank = uint64(i)
				f.FinalOrder.Batches[i].Index = x.base.committed.FairnessBatchCounter + uint64(i) + 1
			}
		}},
		{"transaction-order", "core", func(f *types.VerifiableFairOrderFragment) {
			ids := f.FinalOrder.Batches[0].Transactions
			ids[0], ids[1] = ids[1], ids[0]
			f.Certificate.Part[0].Transactions = append([]types.TxID(nil), ids...)
		}},
		{"rank", "core", func(f *types.VerifiableFairOrderFragment) { f.Certificate.Part[0].Rank++ }},
		{"membership", "core", func(f *types.VerifiableFairOrderFragment) {
			f.Certificate.Part[0].Transactions = f.Certificate.Part[0].Transactions[1:]
		}},
		{"bad-tree-edge", "core", func(f *types.VerifiableFairOrderFragment) { e := &f.Certificate.Part[0].OutTree[0]; e.To = e.From }},
		{"missing-tree-edge", "core", func(f *types.VerifiableFairOrderFragment) {
			f.Certificate.Part[0].InTree = f.Certificate.Part[0].InTree[1:]
		}},
		{"duplicate-parent", "core", func(f *types.VerifiableFairOrderFragment) {
			f.Certificate.Part[0].OutTree[1] = f.Certificate.Part[0].OutTree[0]
		}},
		{"reverse-tree-edge", "core", func(f *types.VerifiableFairOrderFragment) {
			e := &f.Certificate.Part[0].InTree[0]
			e.From, e.To = e.To, e.From
		}},
		{"split-scc", "core", func(f *types.VerifiableFairOrderFragment) {
			ids := f.FinalOrder.Batches[0].Transactions
			var batches []types.FairnessBatch
			var part []types.SCCClaim
			for _, id := range ids {
				batches = append(batches, types.FairnessBatch{Transactions: []types.TxID{id}})
				part = append(part, types.SCCClaim{Transactions: []types.TxID{id}})
			}
			f.FinalOrder.Batches = append(batches, f.FinalOrder.Batches[1:]...)
			f.Certificate.Part = append(part, f.Certificate.Part[1:]...)
			for i := range f.Certificate.Part {
				f.Certificate.Part[i].Rank = uint64(i)
				f.FinalOrder.Batches[i].Index = x.base.committed.FairnessBatchCounter + uint64(i) + 1
			}
		}},
		{"merge-sccs", "core", func(f *types.VerifiableFairOrderFragment) {
			ids := append(f.FinalOrder.Batches[0].Transactions, f.FinalOrder.Batches[1].Transactions...)
			types.SortTxIDs(ids)
			f.FinalOrder.Batches[0].Transactions = ids
			f.Certificate.Part[0].Transactions = ids
			f.FinalOrder.Batches = append(f.FinalOrder.Batches[:1], f.FinalOrder.Batches[2:]...)
			f.Certificate.Part = append(f.Certificate.Part[:1], f.Certificate.Part[2:]...)
		}},
	}
	for _, m := range mutations {
		t.Run(m.name, func(t *testing.T) {
			f := cloneAblationFragment(t, x.candidate.fragment)
			m.edit(f)
			if m.stage != "signature" {
				signAblationFragment(t, x, f)
			}
			checkAblationMutationStage(t, x, f, m.stage)
			checkAblationVerifier(t, x, f, 0, false)
		})
	}
	t.Run("sender", func(t *testing.T) { checkAblationVerifier(t, x, x.candidate.fragment, 1, false) })
	// Held Shaded history blocks fresh Solid output. The forest is nonempty.
	held := makeAblationFixture(t, 10, 1, .9, ablationCase{name: "held", history: 30, fresh: 6, cycle: true}, 19)
	for _, kind := range []string{"missing", "root", "depth", "parent", "duplicate", "unused", "unsafe-output"} {
		t.Run("forest-"+kind, func(t *testing.T) {
			f := cloneAblationFragment(t, held.candidate.fragment)
			root, child := -1, -1
			for i, r := range f.Certificate.BlockForest {
				if r.HasParent {
					child = i
				} else {
					root = i
				}
			}
			if root < 0 || child < 0 {
				t.Fatal("fixture lacks blocker root/child")
			}
			switch kind {
			case "missing":
				f.Certificate.BlockForest = nil
			case "root":
				f.Certificate.BlockForest[root].Vertex = f.Certificate.BlockForest[child].Vertex
			case "depth":
				f.Certificate.BlockForest[child].Depth = 0
			case "parent":
				f.Certificate.BlockForest[child].Parent = f.Certificate.BlockForest[child].Vertex
			case "duplicate":
				f.Certificate.BlockForest = append(f.Certificate.BlockForest, f.Certificate.BlockForest[root])
			case "unused":
				for _, id := range sortedSet(held.candidate.preState.Live) {
					if held.candidate.preState.states[id] == types.StateShaded && id != f.Certificate.BlockForest[root].Vertex {
						f.Certificate.BlockForest = append(f.Certificate.BlockForest, types.BlockRecord{Vertex: id})
						break
					}
				}
			case "unsafe-output":
				id := f.Certificate.BlockForest[child].Vertex
				f.FinalOrder.Batches = []types.FairnessBatch{{Index: held.base.committed.FairnessBatchCounter + 1, Transactions: []types.TxID{id}}}
				f.Certificate.Part = []types.SCCClaim{{Transactions: []types.TxID{id}}}
			}
			signAblationFragment(t, held, f)
			checkAblationMutationStage(t, held, f, "core")
			checkAblationVerifier(t, held, f, 0, false)
		})
	}
}

func checkAblationMutationStage(t testing.TB, x *ablationFixture, f *types.VerifiableFairOrderFragment, stage string) {
	t.Helper()
	if stage == "context" || stage == "signature" {
		return
	}
	s := x.base
	if !x.auth.VerifyLeader(s.authContext, f.LeaderID, types.FragmentDigest(f), f.LeaderSignature) {
		t.Fatal("internal mutation stopped at leader signature")
	}
	pre := s.committed.clone()
	_, _, err := applyEvidenceWithTouches(pre, f.Evidence, f.Epoch, f.FragmentSeq, s.replicaCount, s.fFaulty, s.gamma, s.loMaxSize, s.auth, s.admission, false)
	if stage == "evidence" {
		if err == nil {
			t.Fatal("mutation did not fail evidence validation")
		}
		return
	}
	if err != nil {
		t.Fatalf("mutation stopped early at evidence: %v", err)
	}
	if stage == "prestate" {
		if pre.StateID == f.CandidateEvidenceStateID {
			t.Fatal("prestate not corrupted")
		}
		return
	}
	if pre.StateID != f.CandidateEvidenceStateID {
		t.Fatal("mutation stopped early at prestate")
	}
	dm := rebuildDependencyManagerForTest(pre, s.replicaCount, s.fFaulty, s.gamma)
	a := verifyStructuralCertificate(pre, f.FinalOrder, f.Certificate, s.replicaCount, s.fFaulty, s.gamma)
	b := verifyRecomputedOutputForTest(pre, dm, f.FinalOrder, f.Certificate)
	if stage == "core" {
		if a == nil || b == nil {
			t.Fatalf("core rejection differs: %v / %v", a, b)
		}
		return
	}
	if a != nil || b != nil {
		t.Fatal("poststate mutation stopped early at core")
	}
	post := pre.clone()
	finalize(post, NewDependencyManager(s.replicaCount, s.fFaulty, s.gamma), f.FinalOrder, f.Certificate.Part)
	if post.StateID == f.CandidatePostStateID {
		t.Fatal("poststate not corrupted")
	}
}

func TestAblationAcceptsAlternativeProofs(t *testing.T) {
	for _, n := range []uint64{10, 50} {
		t.Run(fmt.Sprintf("n%d", n), func(t *testing.T) {
			x := makeAblationFixture(t, n, 1, .9, ablationCase{name: "alternative-tree", history: 30, fresh: 6, cycle: true, release: true}, 7)
			f := cloneAblationFragment(t, x.candidate.fragment)
			dm := rebuildDependencyManagerForTest(x.candidate.preState, n, 1, .9)
			claim := &f.Certificate.Part[0]
			claim.InTree = ablationAlternativeTree(claim.Transactions, dm.inverseEdges, true)
			claim.OutTree = ablationAlternativeTree(claim.Transactions, dm.edges, false)
			edgeSet := func(tree []types.TreeEdge) map[types.TreeEdge]bool {
				set := make(map[types.TreeEdge]bool)
				for _, edge := range tree {
					set[edge] = true
				}
				return set
			}
			if reflect.DeepEqual(edgeSet(claim.InTree), edgeSet(x.candidate.fragment.Certificate.Part[0].InTree)) && reflect.DeepEqual(edgeSet(claim.OutTree), edgeSet(x.candidate.fragment.Certificate.Part[0].OutTree)) {
				t.Fatal("no alternative tree produced")
			}
			if types.FragmentDigest(f) == x.candidate.digest {
				t.Fatal("proof change did not change fragment digest")
			}
			signAblationFragment(t, x, f)
			checkAblationVerifier(t, x, f, 0, true)
			held := makeAblationFixture(t, n, 1, .9, ablationCase{name: "alternative-forest", history: 30, fresh: 6, cycle: true}, 19)
			f = cloneAblationFragment(t, held.candidate.fragment)
			dm = rebuildDependencyManagerForTest(held.candidate.preState, n, 1, .9)
			oldRoot := types.TxID{}
			for _, r := range f.Certificate.BlockForest {
				if !r.HasParent {
					oldRoot = r.Vertex
					break
				}
			}
			var newRoot types.TxID
			for _, id := range sortedSet(dm.nodes) {
				if id == oldRoot || held.candidate.preState.states[id] != types.StateShaded {
					continue
				}
				all := true
				for _, r := range f.Certificate.BlockForest {
					if r.HasParent && !dm.edges[id][r.Vertex] {
						all = false
					}
				}
				if all {
					newRoot = id
					break
				}
			}
			if newRoot == (types.TxID{}) {
				t.Fatal("no alternative blocker root")
			}
			for i := range f.Certificate.BlockForest {
				r := &f.Certificate.BlockForest[i]
				if r.HasParent {
					r.Parent = newRoot
					r.Depth = 1
				} else {
					r.Vertex = newRoot
				}
			}
			signAblationFragment(t, held, f)
			checkAblationVerifier(t, held, f, 0, true)
		})
	}
}

// Reverse-neighbor BFS chooses different actual proof edges on a large SCC.
// This is only a positive differential-test generator, never a timed verifier.
func ablationAlternativeTree(ids []types.TxID, adjacency map[types.TxID]map[types.TxID]bool, inward bool) []types.TreeEdge {
	allowed := make(map[types.TxID]bool)
	for _, id := range ids {
		allowed[id] = true
	}
	visited := map[types.TxID]bool{ids[0]: true}
	queue := []types.TxID{ids[0]}
	var tree []types.TreeEdge
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		neighbors := sortedSet(adjacency[u])
		for i := len(neighbors) - 1; i >= 0; i-- {
			v := neighbors[i]
			if !allowed[v] || visited[v] {
				continue
			}
			visited[v] = true
			queue = append(queue, v)
			e := types.TreeEdge{From: u, To: v}
			if inward {
				e.From, e.To = v, u
			}
			tree = append(tree, e)
		}
	}
	return tree
}

func checkAblationGraph(t testing.TB, x *ablationFixture) {
	t.Helper()
	snapshot := x.base.committed.clone()
	s := x.base
	updated := snapshot.clone()
	nodes, pairs, err := applyEvidence(updated, x.orders, s.authContext.Epoch, snapshot.FragmentSeq+1, s.replicaCount, s.fFaulty, s.gamma, s.loMaxSize, s.auth, s.admission)
	if err != nil {
		t.Fatal(err)
	}
	incremental := s.UtigManager.clone()
	incremental.refresh(updated, nodes, pairs)
	rebuilt := rebuildDependencyManagerForTest(updated, s.replicaCount, s.fFaulty, s.gamma)
	assertAblationGraph(t, incremental, rebuilt)
	assertAblationState(t, updated, x.candidate.preState)
	checkAblationPositionCaches(t, updated, s.replicaCount, s.fFaulty, s.gamma)
	x.metrics["touched_pairs"] = float64(len(pairs))
	x.metrics["incremental_cache_weights"] = float64(len(incremental.weights))
	x.metrics["rebuilt_cache_weights"] = float64(len(rebuilt.weights))
	left, right := x.service(true), x.service(true)
	a, err := left.constructCandidate(x.orders)
	if err != nil {
		t.Fatal(err)
	}
	b, err := constructCandidateByRebuildForTest(right, x.orders)
	if err != nil {
		t.Fatal(err)
	}
	assertAblationState(t, a.preState, b.preState)
	assertAblationState(t, a.postState, b.postState)
	assertAblationGraph(t, a.manager, b.manager)
	if !reflect.DeepEqual(a.fragment.FinalOrder, b.fragment.FinalOrder) || a.digest != b.digest {
		t.Fatal("deterministic output or full certificate digest differs")
	}
	for _, c := range []*pendingCandidate{a, b} {
		if err := verifyStructuralCertificate(c.preState, c.fragment.FinalOrder, c.fragment.Certificate, s.replicaCount, s.fFaulty, s.gamma); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := left.CommitPending(a.digest); !ok {
		t.Fatal("incremental commit failed")
	}
	if _, ok := right.CommitPending(b.digest); !ok {
		t.Fatal("rebuild commit failed")
	}
	assertAblationState(t, left.committed, right.committed)
	// Both post-commit caches must support a later incremental update, including
	// previously Blank/Shaded nodes becoming active/Solid.
	var next []*types.LocalOrder
	for _, r := range ablationSenders(s.replicaCount, s.fFaulty, left.committed.FragmentSeq+1) {
		var ids []types.TxID
		for _, id := range sortedSet(left.committed.Live) {
			if _, ok := left.committed.Pos[r][id]; !ok {
				ids = append(ids, id)
			}
		}
		next = append(next, signedOrder(t, x.auth, left.committed, r, left.committed.FragmentSeq+1, ids...))
	}
	a, err = left.constructCandidate(next)
	if err != nil {
		t.Fatal(err)
	}
	b, err = right.constructCandidate(next)
	if err != nil {
		t.Fatal(err)
	}
	if a.digest != b.digest {
		t.Fatal("next-round construction differs")
	}
	assertAblationState(t, a.postState, b.postState)
	assertAblationGraph(t, a.manager, b.manager)
	assertAblationState(t, x.base.committed, snapshot)
}

// Independent, untimed cache oracle: derive counts from retained positions.
func checkAblationPositionCaches(t testing.TB, s *EvidenceState, n, f uint64, gamma float64) {
	t.Helper()
	visibility := make(map[types.TxID]int)
	weights := make(map[edgeKey]int)
	for _, positions := range s.Pos {
		for u, pu := range positions {
			visibility[u]++
			for v, pv := range positions {
				if pu < pv {
					weights[edgeKey{u, v}]++
				}
			}
		}
	}
	if !reflect.DeepEqual(visibility, s.visibility) || !reflect.DeepEqual(weights, s.weights) {
		t.Fatal("position-derived caches differ")
	}
	for id := range s.Live {
		want := types.StateBlank
		if visibility[id] >= types.CalculateSolidThreshold(n, f) {
			want = types.StateSolid
		} else if visibility[id] >= types.CalculateEdgeThreshold(n, f, gamma) {
			want = types.StateShaded
		}
		if s.states[id] != want {
			t.Fatal("position-derived classification differs")
		}
	}
}

func TestAblationGraphEquivalence(t *testing.T) {
	for _, n := range []uint64{10, 50} {
		for _, seed := range []int64{1, 7, 19} {
			for _, c := range ablationCases(30) {
				// Keep correctness cases small; benchmark uses larger sizes.
				c.fresh = min(c.fresh, 12)
				t.Run(fmt.Sprintf("n%d/%s/seed%d", n, c.name, seed), func(t *testing.T) {
					x := makeAblationFixture(t, n, 1, .9, c, seed)
					checkAblationGraph(t, x)
					if c.fresh == 0 && !c.release && x.metrics["new_positions"] != 0 {
						t.Fatal("expected no effective new positions")
					}
				})
			}
		}
	}
}

func TestAblationGraphActivationAndReorientation(t *testing.T) {
	x := makeAblationFixture(t, 10, 1, .9, ablationCase{name: "transitions"}, 7)
	ids := []types.TxID{types.TransactionID([]byte("pair-a")), types.TransactionID([]byte("pair-b"))}
	for i, content := range [][]byte{[]byte("pair-a"), []byte("pair-b")} {
		if err := x.admission.Store(types.Transaction{ID: ids[i], CanonicalBytes: content}); err != nil {
			t.Fatal(err)
		}
	}
	types.SortTxIDs(ids)
	seen := make(map[uint64]bool)
	// One first occurrence is Blank; two activate the pair. Reverse votes
	// first produce a public-ID tie, then reverse the retained edge.
	for phase, count := range []int{1, 1, 2, 1} {
		var orders []*types.LocalOrder
		for _, r := range ablationSenders(10, 1, x.base.committed.FragmentSeq+1) {
			var local []types.TxID
			if !seen[r] && count > 0 {
				local = append([]types.TxID(nil), ids...)
				if phase >= 2 {
					local[0], local[1] = local[1], local[0]
				}
				seen[r] = true
				count--
			}
			orders = append(orders, signedOrder(t, x.auth, x.base.committed, r, x.base.committed.FragmentSeq+1, local...))
		}
		if count != 0 {
			t.Fatal("insufficient scheduled fresh reporters")
		}
		x.advanceRound(t, orders)
		rebuilt := rebuildDependencyManagerForTest(x.base.committed, 10, 1, .9)
		assertAblationGraph(t, x.base.UtigManager, rebuilt)
		checkAblationPositionCaches(t, x.base.committed, 10, 1, .9)
		if phase == 0 && len(rebuilt.nodes) != 0 {
			t.Fatal("Blank nodes became active")
		}
		if (phase == 1 || phase == 2) && !rebuilt.edges[ids[0]][ids[1]] {
			t.Fatal("activation or deterministic tie failed")
		}
		if phase == 3 && !rebuilt.edges[ids[1]][ids[0]] {
			t.Fatal("retained edge did not reorient")
		}
	}
}

func TestAblationFixtureReproducible(t *testing.T) {
	c := ablationCase{name: "reproducible", history: 30, fresh: 6, cycle: true, release: true}
	a := makeAblationFixture(t, 10, 1, .9, c, 7)
	b := makeAblationFixture(t, 10, 1, .9, c, 7)
	if a.candidate.digest != b.candidate.digest || !reflect.DeepEqual(a.candidate.fragment, b.candidate.fragment) {
		t.Fatal("same seed did not reproduce signed evidence and candidate")
	}
	assertAblationState(t, a.base.committed, b.base.committed)
	assertAblationGraph(t, a.base.UtigManager, b.base.UtigManager)
}
