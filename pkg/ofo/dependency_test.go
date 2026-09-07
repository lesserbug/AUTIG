package ofo

import (
	"SpeedFair_simplify/pkg/types"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"sort"
	"sync"
	"testing"
)

type testAuthenticator struct {
	public        map[uint64]ed25519.PublicKey
	private       map[uint64]ed25519.PrivateKey
	leaderPublic  ed25519.PublicKey
	leaderPrivate ed25519.PrivateKey
}

func newTestAuthenticator(t *testing.T, replicas uint64) *testAuthenticator {
	t.Helper()
	auth := &testAuthenticator{public: make(map[uint64]ed25519.PublicKey), private: make(map[uint64]ed25519.PrivateKey)}
	for replicaID := uint64(0); replicaID < replicas; replicaID++ {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		auth.public[replicaID], auth.private[replicaID] = public, private
	}
	auth.leaderPublic, auth.leaderPrivate, _ = ed25519.GenerateKey(rand.Reader)
	return auth
}

func (auth *testAuthenticator) SignReplica(replicaID uint64, digest [32]byte) ([]byte, error) {
	return ed25519.Sign(auth.private[replicaID], digest[:]), nil
}
func (auth *testAuthenticator) VerifyReplica(replicaID uint64, digest [32]byte, signature []byte) bool {
	return ed25519.Verify(auth.public[replicaID], digest[:], signature)
}
func (auth *testAuthenticator) SignLeader(_ types.AuthContext, digest [32]byte) ([]byte, error) {
	return ed25519.Sign(auth.leaderPrivate, digest[:]), nil
}
func (auth *testAuthenticator) VerifyLeader(_ types.AuthContext, _ uint64, digest [32]byte, signature []byte) bool {
	return ed25519.Verify(auth.leaderPublic, digest[:], signature)
}

type testAdmission struct {
	mu      sync.RWMutex
	content map[types.TxID][]byte
}

func newTestAdmission() *testAdmission {
	return &testAdmission{content: make(map[types.TxID][]byte)}
}

func (admission *testAdmission) Store(transaction types.Transaction) error {
	if types.TransactionID(transaction.CanonicalBytes) != transaction.ID {
		return fmt.Errorf("invalid content commitment")
	}
	admission.mu.Lock()
	admission.content[transaction.ID] = append([]byte(nil), transaction.CanonicalBytes...)
	admission.mu.Unlock()
	return nil
}

func (admission *testAdmission) Resolve(id types.TxID) ([]byte, types.TxID, error) {
	admission.mu.RLock()
	defer admission.mu.RUnlock()
	content, ok := admission.content[id]
	if !ok {
		return nil, types.TxID{}, fmt.Errorf("unavailable")
	}
	return append([]byte(nil), content...), id, nil
}

func (*testAdmission) ValidateAdmission([]byte) error { return nil }

func testTx(t *testing.T, admission *testAdmission, value byte) types.TxID {
	t.Helper()
	canonical := []byte{value}
	id := types.TransactionID(canonical)
	if err := admission.Store(types.Transaction{ID: id, CanonicalBytes: canonical}); err != nil {
		t.Fatal(err)
	}
	return id
}

func signedOrder(t *testing.T, auth types.Authenticator, state *EvidenceState, replicaID, fragmentSeq uint64, ids ...types.TxID) *types.LocalOrder {
	t.Helper()
	order := &types.LocalOrder{
		ReplicaID: replicaID, Epoch: state.Epoch, FragmentSeq: fragmentSeq,
		PrevPosition: state.Seq[replicaID], PrevHead: state.Head[replicaID], OrderedTxs: append([]types.TxID(nil), ids...),
	}
	order.NewPosition = order.PrevPosition + uint64(len(ids))
	order.NewHead = types.LocalOrderHead(order)
	signature, err := auth.SignReplica(replicaID, types.LocalOrderDigest(order))
	if err != nil {
		t.Fatal(err)
	}
	order.Signature = signature
	return order
}

func applyAndRefresh(t *testing.T, state *EvidenceState, manager *DependencyManager, auth types.Authenticator, admission types.TransactionAdmission, n, f uint64, gamma float64, orders ...*types.LocalOrder) {
	t.Helper()
	sort.Slice(orders, func(i, j int) bool { return orders[i].ReplicaID < orders[j].ReplicaID })
	touchedNodes, touchedPairs, err := applyEvidence(state, orders, state.Epoch, state.FragmentSeq+1, n, f, gamma, 10, auth, admission)
	if err != nil {
		t.Fatal(err)
	}
	manager.refresh(state, touchedNodes, touchedPairs)
}

func TestEvidenceRejectsRetransmissionAndSecondLiveFirstPosition(t *testing.T) {
	const n, f = uint64(4), uint64(1)
	auth := newTestAuthenticator(t, n)
	admission := newTestAdmission()
	state := NewEvidenceState(1, n, [32]byte{1})
	tx := testTx(t, admission, 1)
	order := signedOrder(t, auth, state, 0, 1, tx)
	first := []*types.LocalOrder{order, signedOrder(t, auth, state, 1, 1), signedOrder(t, auth, state, 2, 1)}
	if _, _, err := applyEvidence(state, first, 1, 1, n, f, 1, 10, auth, admission); err != nil {
		t.Fatal(err)
	}
	visibility, seq := state.visibility[tx], state.Seq[0]
	retransmission := []*types.LocalOrder{order, signedOrder(t, auth, state, 1, 2), signedOrder(t, auth, state, 2, 2)}
	if _, _, err := applyEvidence(state, retransmission, 1, 2, n, f, 1, 10, auth, admission); err == nil {
		t.Fatal("retransmitted extension was accepted")
	}
	if state.visibility[tx] != visibility || state.Seq[0] != seq {
		t.Fatal("retransmission changed evidence")
	}
	duplicate := signedOrder(t, auth, state, 0, 2, tx)
	second := []*types.LocalOrder{duplicate, signedOrder(t, auth, state, 1, 2), signedOrder(t, auth, state, 2, 2)}
	if _, _, err := applyEvidence(state, second, 1, 2, n, f, 1, 10, auth, admission); err == nil {
		t.Fatal("second live first position was accepted")
	}
}

func TestEvidenceRequiresExactDistinctBoundedSendersAndAvailableContent(t *testing.T) {
	const n, f = uint64(4), uint64(1)
	auth := newTestAuthenticator(t, n)
	admission := newTestAdmission()
	state := NewEvidenceState(1, n, [32]byte{1})
	tx := testTx(t, admission, 1)
	originalStateID := state.StateID

	partial := []*types.LocalOrder{signedOrder(t, auth, state, 0, 1, tx)}
	if _, _, err := applyEvidence(state, partial, 1, 1, n, f, 1, 10, auth, admission); err == nil {
		t.Fatal("partial evidence batch was accepted")
	}
	if state.StateID != originalStateID || state.Seq[0] != 0 {
		t.Fatal("rejected partial evidence changed state")
	}

	duplicateSender := []*types.LocalOrder{
		signedOrder(t, auth, state, 0, 1, tx),
		signedOrder(t, auth, state, 0, 1),
		signedOrder(t, auth, state, 1, 1),
	}
	if _, _, err := applyEvidence(state, duplicateSender, 1, 1, n, f, 1, 10, auth, admission); err == nil {
		t.Fatal("duplicate evidence sender was accepted")
	}

	bounded := []*types.LocalOrder{
		signedOrder(t, auth, state, 0, 1, tx),
		signedOrder(t, auth, state, 1, 1),
		signedOrder(t, auth, state, 2, 1),
	}
	if _, _, err := applyEvidence(state, bounded, 1, 1, n, f, 1, 0, auth, admission); err == nil {
		t.Fatal("oversized local order was accepted")
	}

	unavailable := types.TransactionID([]byte("unavailable"))
	missingContent := []*types.LocalOrder{
		signedOrder(t, auth, state, 0, 1, unavailable),
		signedOrder(t, auth, state, 1, 1),
		signedOrder(t, auth, state, 2, 1),
	}
	if _, _, err := applyEvidence(state, missingContent, 1, 1, n, f, 1, 10, auth, admission); err == nil {
		t.Fatal("unavailable transaction content was accepted")
	}
	if state.StateID != originalStateID || len(state.Live) != 0 {
		t.Fatal("rejected evidence changed authoritative state")
	}
}

func TestCumulativeStateAndTouchedPairReorientEdge(t *testing.T) {
	const n, f = uint64(10), uint64(2)
	const gamma = 1.0
	auth := newTestAuthenticator(t, n)
	admission := newTestAdmission()
	state := NewEvidenceState(1, n, [32]byte{1})
	manager := NewDependencyManager(n, f, gamma)
	u, v := testTx(t, admission, 1), testTx(t, admission, 2)
	if types.LessTxID(v, u) {
		u, v = v, u
	}
	var first []*types.LocalOrder
	for replicaID := uint64(0); replicaID < 3; replicaID++ {
		first = append(first, signedOrder(t, auth, state, replicaID, 1, u, v))
	}
	for replicaID := uint64(3); replicaID < 8; replicaID++ {
		first = append(first, signedOrder(t, auth, state, replicaID, 1))
	}
	applyAndRefresh(t, state, manager, auth, admission, n, f, gamma, first...)
	if state.states[u] != types.StateShaded {
		t.Fatalf("expected Shaded, got %s", state.states[u])
	}
	var second []*types.LocalOrder
	for replicaID := uint64(3); replicaID < 6; replicaID++ {
		second = append(second, signedOrder(t, auth, state, replicaID, 2, v, u))
	}
	for _, replicaID := range []uint64{0, 1, 2, 6, 7} {
		second = append(second, signedOrder(t, auth, state, replicaID, 2))
	}
	applyAndRefresh(t, state, manager, auth, admission, n, f, gamma, second...)
	if state.states[u] != types.StateSolid || !manager.edges[u][v] {
		t.Fatal("tie did not keep the public-ID direction or state was not cumulative")
	}
	third := []*types.LocalOrder{signedOrder(t, auth, state, 6, 3, v, u)}
	for _, replicaID := range []uint64{0, 1, 2, 3, 4, 5, 7} {
		third = append(third, signedOrder(t, auth, state, replicaID, 3))
	}
	applyAndRefresh(t, state, manager, auth, admission, n, f, gamma, third...)
	if manager.edges[u][v] || !manager.edges[v][u] {
		t.Fatal("touched pair did not reorient after directional dominance changed")
	}
	if state.states[u] != types.StateSolid {
		t.Fatal("live transaction state regressed")
	}
}

func TestExactSafeClosureAndBlockerPath(t *testing.T) {
	const n, f = uint64(5), uint64(1)
	const gamma = 1.0
	admission := newTestAdmission()
	state := NewEvidenceState(1, n, [32]byte{1})
	manager := NewDependencyManager(n, f, gamma)
	x, y, z := testTx(t, admission, 1), testTx(t, admission, 2), testTx(t, admission, 3)
	for _, id := range []types.TxID{x, y, z} {
		state.Live[id] = true
		state.TxRef[id] = id
	}
	state.states[x], state.states[y], state.states[z] = types.StateShaded, types.StateSolid, types.StateSolid
	state.weights[edgeKey{x, y}] = manager.edgeThreshold
	touchedNodes := map[types.TxID]bool{x: true, y: true, z: true}
	touchedPairs := map[pairKey]bool{makePair(x, y): true, makePair(x, z): true, makePair(y, z): true}
	manager.refresh(state, touchedNodes, touchedPairs)
	finalOrder, certificate, err := manager.buildProposal(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalOrder.Batches) != 1 || !sameIDs(finalOrder.Batches[0].Transactions, []types.TxID{z}) {
		t.Fatalf("safe closure was not exact: %+v", finalOrder.Batches)
	}
	if err := verifyStructuralCertificate(state, finalOrder, certificate, n, f, gamma); err != nil {
		t.Fatal(err)
	}
	if len(certificate.BlockForest) != 2 {
		t.Fatalf("expected blocker path x->y, got %+v", certificate.BlockForest)
	}
	certificate.BlockForest = certificate.BlockForest[:1]
	if err := verifyStructuralCertificate(state, finalOrder, certificate, n, f, gamma); err == nil {
		t.Fatal("certificate without the excluded Solid blocker path was accepted")
	}
}

func TestCertificateBoundariesFromEvidence(t *testing.T) {
	const n, f = uint64(5), uint64(1)
	auth := newTestAuthenticator(t, n)
	admission := newTestAdmission()
	a, b, c, d, e, z := testTx(t, admission, 10), testTx(t, admission, 11), testTx(t, admission, 12), testTx(t, admission, 13), testTx(t, admission, 14), testTx(t, admission, 15)
	state := NewEvidenceState(1, n, [32]byte{})
	manager := NewDependencyManager(n, f, 1)
	applyAndRefresh(t, state, manager, auth, admission, n, f, 1,
		signedOrder(t, auth, state, 0, 1, z, e, a, b, c, d),
		signedOrder(t, auth, state, 1, 1, z, b, c, a, e, d),
		signedOrder(t, auth, state, 2, 1, z, c, a, b, d),
		signedOrder(t, auth, state, 3, 1))
	for _, name := range []string{"valid", "split SCC", "merge SCCs", "invalid tree", "incoming frontier", "omit safe vertex"} {
		t.Run(name, func(t *testing.T) {
			order, cert, err := manager.buildProposal(state)
			if err != nil {
				t.Fatal(err)
			}
			if len(order.Batches) != 2 || len(order.Batches[1].Transactions) != 3 {
				t.Fatal("fixture lacks singleton followed by SCC")
			}
			switch name {
			case "split SCC":
				cycle := order.Batches[1].Transactions
				order.Batches = order.Batches[:1]
				cert.Part = cert.Part[:1]
				for _, id := range cycle {
					rank := len(cert.Part)
					order.Batches = append(order.Batches, types.FairnessBatch{Index: uint64(rank + 1), Transactions: []types.TxID{id}})
					cert.Part = append(cert.Part, types.SCCClaim{Rank: uint64(rank), Transactions: []types.TxID{id}})
				}
			case "merge SCCs":
				merged := append([]types.TxID{z}, order.Batches[1].Transactions...)
				types.SortTxIDs(merged)
				order.Batches = []types.FairnessBatch{{Index: 1, Transactions: merged}}
				cert.Part = []types.SCCClaim{{Transactions: merged, InTree: spanningTree(merged[0], merged, manager.inverseEdges, true), OutTree: spanningTree(merged[0], merged, manager.edges, false)}}
			case "invalid tree":
				cert.Part[1].OutTree[0] = types.TreeEdge{From: a, To: a}
			case "incoming frontier":
				order.Batches = order.Batches[1:]
				order.Batches[0].Index = 1
				cert.Part = cert.Part[1:]
				cert.Part[0].Rank = 0
			case "omit safe vertex":
				order.Batches = nil
				cert.Part = nil
			}
			err = verifyStructuralCertificate(state, order, cert, n, f, 1)
			if (err == nil) != (name == "valid") {
				t.Fatalf("unexpected verification result: %v", err)
			}
		})
	}
}

func TestEmptyClosureAndSharedBlockerPaths(t *testing.T) {
	// Structural-verifier unit fixture: a Shaded root followed by a Solid chain.
	const n, f = uint64(5), uint64(1)
	state := NewEvidenceState(1, n, [32]byte{})
	manager := NewDependencyManager(n, f, 1)
	nodes := map[types.TxID]bool{}
	pairs := map[pairKey]bool{}
	var previous types.TxID
	for i := byte(1); i <= 32; i++ {
		id := types.TxID{i}
		state.Live[id] = true
		state.states[id] = types.StateSolid
		nodes[id] = true
		if i == 1 {
			state.states[id] = types.StateShaded
		} else {
			state.weights[edgeKey{previous, id}] = 2
			pairs[makePair(previous, id)] = true
		}
		previous = id
	}
	manager.refresh(state, nodes, pairs)
	order, cert, err := manager.buildProposal(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(order.Batches) != 0 || len(cert.BlockForest) != 32 {
		t.Fatal("expected empty closure and shared blocker chain")
	}
	if err := verifyStructuralCertificate(state, order, cert, n, f, 1); err != nil {
		t.Fatal(err)
	}
	cert.BlockForest[1].Depth = 0
	if err := verifyStructuralCertificate(state, order, cert, n, f, 1); err == nil {
		t.Fatal("invalid blocker depth accepted")
	}
}

func TestDelayedFinalizedOccurrencePreservesLivePairs(t *testing.T) {
	const n, f = uint64(5), uint64(1)
	auth := newTestAuthenticator(t, n)
	admission := newTestAdmission()
	state := NewEvidenceState(1, n, [32]byte{})
	manager := NewDependencyManager(n, f, 1)
	u, x, y := testTx(t, admission, 20), testTx(t, admission, 21), testTx(t, admission, 22)
	applyAndRefresh(t, state, manager, auth, admission, n, f, 1,
		signedOrder(t, auth, state, 0, 1, u, x, y), signedOrder(t, auth, state, 1, 1, u, x, y), signedOrder(t, auth, state, 2, 1, u), signedOrder(t, auth, state, 3, 1))
	order, cert, err := manager.buildProposal(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(order.Batches) != 1 || !sameIDs(order.Batches[0].Transactions, []types.TxID{u}) {
		t.Fatal("expected only u safe")
	}
	finalize(state, manager, order, cert.Part)
	if state.weights[edgeKey{x, y}] != 2 {
		t.Fatal("finalization changed retained pair")
	}
	delayed := signedOrder(t, auth, state, 3, 2, u, x, y)
	orders := []*types.LocalOrder{signedOrder(t, auth, state, 0, 2), signedOrder(t, auth, state, 1, 2), signedOrder(t, auth, state, 2, 2), delayed}
	unordered := append([]*types.LocalOrder(nil), orders...)
	unordered[0], unordered[1] = unordered[1], unordered[0]
	if _, _, err := applyEvidence(state.clone(), unordered, 1, 2, n, f, 1, 10, auth, admission); err == nil {
		t.Fatal("noncanonical evidence accepted")
	}
	applyAndRefresh(t, state, manager, auth, admission, n, f, 1, orders...)
	if state.Live[u] || state.Pos[3][u] != 0 || state.Seq[3] != 3 || state.Pos[3][x] != 2 || state.weights[edgeKey{x, y}] != 3 {
		t.Fatal("delayed finalized occurrence broke continuity or pair accounting")
	}
}
