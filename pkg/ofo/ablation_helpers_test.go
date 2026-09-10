package ofo

import (
	"SpeedFair_simplify/pkg/types"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// All ablation implementations are test-only. Keep the full-path wrappers in
// step with service.go; differential tests exercise their state effects.
type ablationCase struct {
	name           string
	history, fresh int
	loMaxSize      int // zero uses the main benchmark's current limit, 200
	cycle, release bool
	delayed        bool
	blocked        bool
}

type ablationFixture struct {
	base      *OFOService
	orders    []*types.LocalOrder
	candidate *pendingCandidate
	auth      *testAuthenticator
	admission *testAdmission
	metrics   map[string]float64
	warmMaxLO int
}

func ablationCases(history int) []ablationCase {
	return []ablationCase{
		{name: "common-low", history: 12, fresh: 96, release: true},
		{name: "synthetic-small-many", history: 24, fresh: 192},
		{name: "synthetic-large-few", history: history, fresh: 8},
		{name: "synthetic-large-many", history: history, fresh: 128},
		{name: "synthetic-blocked-solid", history: history, fresh: 8, blocked: true},
		{name: "synthetic-empty-extension", history: history},
		{name: "synthetic-delayed-done", history: history, delayed: true},
		{name: "synthetic-cycle-held", history: history, fresh: 8, cycle: true},
		{name: "synthetic-cycle-release", history: history, fresh: 8, cycle: true, release: true},
	}
}

// Use the production collector's rotating sender set, then canonicalize it.
func ablationSenders(n, f, seq uint64) []uint64 {
	var ids []uint64
	for r := uint64(0); r < n; r++ {
		if (r+n-seq%n)%n < n-f {
			ids = append(ids, r)
		}
	}
	return ids
}

func makeAblationFixture(t testing.TB, n, f uint64, gamma float64, c ablationCase, seed int64) *ablationFixture {
	t.Helper()
	x := &ablationFixture{auth: newAblationAuthenticator(n, seed), admission: newTestAdmission()}
	context := types.AuthContext{Epoch: 1, LeaderID: 0, Identifier: [32]byte{7}}
	genesis := types.ProtocolGenesis{StateID: [32]byte{1}, FragmentDigest: [32]byte{2}}
	limit := c.loMaxSize
	if limit == 0 {
		limit = 200
	}
	if limit < 3 || c.fresh > limit || (c.release && c.fresh >= limit) {
		t.Fatalf("LO limit %d cannot accommodate this case's fresh extension (%d)", limit, c.fresh)
	}
	s, err := NewOFOService(0, n, f, gamma, newTestNetwork(), limit, nil, false, x.auth, x.admission, context, genesis, func(*types.VerifiableFairOrderFragment, [32]byte) {})
	if err != nil {
		t.Fatal(err)
	}
	// Only synchronous local computation is under study; stop collector workers.
	s.Stop()
	x.base = s
	rng := rand.New(rand.NewSource(seed))
	makeIDs := func(count int) []types.TxID {
		ids := make([]types.TxID, count)
		for i := range ids {
			content := make([]byte, 512)
			_, _ = rng.Read(content)
			binary.BigEndian.PutUint64(content, uint64(i))
			ids[i] = types.TransactionID(content)
			if err := x.admission.Store(types.Transaction{ID: ids[i], CanonicalBytes: content}); err != nil {
				t.Fatal(err)
			}
		}
		return ids
	}
	dead, history, fresh := makeIDs(3), makeIDs(c.history), makeIDs(c.fresh)
	ordersFor := func(contents func(uint64) []types.TxID) []*types.LocalOrder {
		var orders []*types.LocalOrder
		for _, r := range ablationSenders(n, f, s.committed.FragmentSeq+1) {
			orders = append(orders, signedOrder(t, x.auth, s.committed, r, s.committed.FragmentSeq+1, contents(r)...))
		}
		return orders
	}
	// A real nonempty commit populates Done and advances batch/digest context.
	x.advanceRound(t, ordersFor(func(uint64) []types.TxID { return dead }))
	reporters := make(map[uint64]int)
	for index, r := range ablationSenders(n, f, s.committed.FragmentSeq+1) {
		if index < types.CalculateSolidThreshold(n, f)-1 {
			reporters[r] = index
		}
	}
	// Three cyclic block orders create one large SCC, rather than relying on
	// random permutations to happen to contain a cycle. All history is Shaded.
	historyOrder := func(index int) []types.TxID {
		if !c.cycle || len(history) < 3 {
			return history
		}
		cut := (index % 3) * (len(history) / 3)
		return append(append([]types.TxID(nil), history[cut:]...), history[:cut]...)
	}
	// Drain each replica's prescribed reception sequence in bounded extensions.
	// The rotating sender set may require extra rounds; never raise loMaxSize.
	queued := make(map[uint64][]types.TxID)
	for r := uint64(0); r < n; r++ {
		if index, ok := reporters[r]; ok {
			queued[r] = historyOrder(index)
		} else if c.blocked {
			queued[r] = history[3:]
		}
	}
	for len(queued) > 0 {
		x.advanceRound(t, ordersFor(func(r uint64) []types.TxID {
			ids := queued[r]
			count := min(len(ids), limit)
			if count == len(ids) {
				delete(queued, r)
			} else {
				queued[r] = ids[count:]
			}
			return ids[:count]
		}))
	}
	// A single additional reporter suffices to make the retained history Solid.
	// For cycles, add the least represented rotation to preserve the large SCC.
	var releaser uint64
	var releaseTail []types.TxID
	if c.release {
		for {
			if _, ok := reporters[releaser]; !ok {
				break
			}
			releaser++
		}
		releaseTail = historyOrder(len(reporters) % 3)
		if !c.cycle && len(releaseTail)+c.fresh > limit {
			t.Fatal("ordered release must fit one LO to retain all history until the sample")
		}
		for len(releaseTail)+c.fresh > limit {
			x.advanceRound(t, ordersFor(func(r uint64) []types.TxID {
				if r != releaser {
					return nil
				}
				count := min(limit, len(releaseTail)-(limit-c.fresh))
				ids := releaseTail[:count]
				releaseTail = releaseTail[count:]
				return ids
			}))
		}
	}
	// Retain the graph across another legally committed round, including when
	// the maximal safe output is empty. No state maps are hand-populated.
	x.advanceRound(t, ordersFor(func(uint64) []types.TxID { return nil }))
	// The final release/late-Done reporter must belong to the measured round.
	if c.release || c.delayed {
		required := releaser
		if c.delayed {
			required = 0
		}
		for {
			selected := false
			for _, r := range ablationSenders(n, f, s.committed.FragmentSeq+1) {
				if r == required {
					selected = true
				}
			}
			if selected {
				break
			}
			x.advanceRound(t, ordersFor(func(uint64) []types.TxID { return nil }))
		}
	}
	x.orders = ordersFor(func(r uint64) []types.TxID {
		var ids []types.TxID
		if c.release && r == releaser {
			ids = append(ids, releaseTail...)
		}
		ids = append(ids, fresh...)
		if c.delayed {
			// Only the replica omitted from round one has this late occurrence.
			if r == 0 {
				ids = append(ids, dead...)
			}
		}
		return ids
	})
	leader := x.service(true)
	x.candidate, err = leader.constructCandidate(x.orders)
	if err != nil {
		t.Fatal(err)
	}
	x.metrics = ablationMetrics(x)
	if len(s.committed.Live) != c.history {
		t.Fatalf("retained history=%d, want %d", len(s.committed.Live), c.history)
	}
	if c.cycle && (x.metrics["pre_max_scc"] != float64(c.history) || x.metrics["updated_max_scc"] != float64(c.history)) {
		t.Fatal("cycle fixture did not preserve its full historical SCC")
	}
	if c.release && x.metrics["output_tx"] != float64(c.history+c.fresh) {
		t.Fatal("release fixture did not output all history and fresh transactions")
	}
	if !c.release && c.history > 0 && x.metrics["output_tx"] != 0 {
		t.Fatal("held fixture unexpectedly released output")
	}
	if c.blocked && (x.metrics["pre_shaded"] != 3 || x.metrics["pre_solid"] != float64(c.history-3)) {
		t.Fatal("blocked fixture lost its Shaded roots or retained Solid history")
	}
	if !c.release && c.history > 0 && (c.fresh > 0 || c.blocked) && (x.metrics["excluded_solid"] == 0 || x.metrics["block_records"] == 0 || x.metrics["forest_roots"] == 0) {
		t.Fatal("held fixture lacks excluded Solid transactions or their blocker proof")
	}
	return x
}

// Reuse the existing real Ed25519 test authenticator, with reproducible test
// keys so the two experiments and independent processes get byte-identical F.
// Key derivation is fixture setup, never part of a timed operation.
func newAblationAuthenticator(n uint64, seed int64) *testAuthenticator {
	auth := &testAuthenticator{public: make(map[uint64]ed25519.PublicKey), private: make(map[uint64]ed25519.PrivateKey)}
	key := func(role string, replica uint64) ed25519.PrivateKey {
		material := sha256.Sum256([]byte(fmt.Sprintf("AUTIG/ablation-test-key/%d/%d/%s/%d", n, seed, role, replica)))
		return ed25519.NewKeyFromSeed(material[:])
	}
	for r := uint64(0); r < n; r++ {
		auth.private[r] = key("replica", r)
		auth.public[r] = auth.private[r].Public().(ed25519.PublicKey)
	}
	auth.leaderPrivate = key("leader", 0)
	auth.leaderPublic = auth.leaderPrivate.Public().(ed25519.PublicKey)
	return auth
}

func (x *ablationFixture) service(leader bool) *OFOService {
	s := x.base
	r := uint64(1)
	var manager *DependencyManager
	if leader {
		r = s.authContext.LeaderID
		manager = s.UtigManager
	}
	return &OFOService{ReplicaID: r, isLeader: leader, auth: s.auth, admission: s.admission,
		authContext: s.authContext, committed: s.committed, latestCommittedFragment: s.latestCommittedFragment,
		UtigManager: manager, replicaCount: s.replicaCount, fFaulty: s.fFaulty, gamma: s.gamma, loMaxSize: s.loMaxSize}
}

func (x *ablationFixture) advanceRound(t testing.TB, orders []*types.LocalOrder) {
	t.Helper()
	for _, order := range orders {
		x.warmMaxLO = max(x.warmMaxLO, len(order.OrderedTxs))
	}
	follower := x.service(false)
	candidate, err := x.base.constructCandidate(orders)
	if err != nil {
		t.Fatal(err)
	}
	if !follower.verifyCandidate(candidate.fragment, x.base.authContext.LeaderID) {
		t.Fatal("production follower rejected warm-up")
	}
	if _, ok := follower.CommitPending(candidate.digest); !ok {
		t.Fatal("follower warm-up commit failed")
	}
	if _, ok := x.base.CommitPending(candidate.digest); !ok {
		t.Fatal("leader warm-up commit failed")
	}
	assertAblationState(t, follower.committed, x.base.committed)
}

// Re-materialize derived graph/cache data from the retained evidence state.
// No log replay, touch map, or position-pair enumeration belongs here.
func rebuildDependencyManagerForTest(state *EvidenceState, n, f uint64, gamma float64) *DependencyManager {
	dm := NewDependencyManager(n, f, gamma)
	var active []types.TxID
	for id := range state.Live {
		dm.txStates[id] = state.states[id]
		if state.states[id] != types.StateBlank {
			active = append(active, id)
			dm.nodes[id] = true
			dm.edges[id] = make(map[types.TxID]bool)
			dm.inverseEdges[id] = make(map[types.TxID]bool)
		}
	}
	for key, weight := range state.weights {
		if weight != 0 {
			dm.weights[key] = weight
		}
	}
	for i, u := range active {
		for _, v := range active[i+1:] {
			wUV, wVU := dm.weights[edgeKey{u, v}], dm.weights[edgeKey{v, u}]
			if edgePred(u, v, wUV, wVU, dm.edgeThreshold) {
				dm.edges[u][v], dm.inverseEdges[v][u] = true, true
			} else if edgePred(v, u, wVU, wUV, dm.edgeThreshold) {
				dm.edges[v][u], dm.inverseEdges[u][v] = true, true
			}
		}
	}
	return dm
}

func constructCandidateByRebuildForTest(s *OFOService, orders []*types.LocalOrder) (*pendingCandidate, error) {
	s.rwMu.Lock()
	defer s.rwMu.Unlock()
	if s.pending != nil {
		return nil, fmt.Errorf("a candidate is already pending")
	}
	orders = append([]*types.LocalOrder(nil), orders...)
	sort.Slice(orders, func(i, j int) bool { return orders[i].ReplicaID < orders[j].ReplicaID })
	pre := s.committed.clone()
	seq := s.committed.FragmentSeq + 1
	if _, _, err := applyEvidenceWithTouches(pre, orders, s.authContext.Epoch, seq, s.replicaCount, s.fFaulty, s.gamma, s.loMaxSize, s.auth, s.admission, false); err != nil {
		return nil, err
	}
	manager := rebuildDependencyManagerForTest(pre, s.replicaCount, s.fFaulty, s.gamma)
	order, cert, err := manager.buildProposal(pre)
	if err != nil {
		return nil, err
	}
	post, postManager := pre.clone(), manager.clone()
	finalize(post, postManager, order, cert.Part)
	fragment := &types.VerifiableFairOrderFragment{
		Epoch: s.authContext.Epoch, AuthContextID: s.authContext.Identifier, LeaderID: s.authContext.LeaderID,
		FragmentSeq: seq, PreviousStateID: s.committed.StateID, PreviousFragmentDigest: s.latestCommittedFragment,
		Evidence: cloneOrders(orders), FinalOrder: order, Certificate: cert,
		CandidateEvidenceStateID: pre.StateID, CandidatePostStateID: post.StateID,
	}
	digest := types.FragmentDigest(fragment)
	fragment.LeaderSignature, err = s.auth.SignLeader(s.authContext, digest)
	if err != nil {
		return nil, err
	}
	s.pending = &pendingCandidate{digest: digest, preState: pre, postState: post, manager: postManager, fragment: fragment, done: make(chan struct{})}
	return s.pending, nil
}

func assertAblationState(t testing.TB, a, b *EvidenceState) {
	t.Helper()
	// Weight maps may omit zeros; all other authoritative fields and caches
	// produced by these execution paths have the same canonical value domain.
	a, b = a.clone(), b.clone()
	for _, s := range []*EvidenceState{a, b} {
		for k, w := range s.weights {
			if w == 0 {
				delete(s.weights, k)
			}
		}
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("evidence states differ (including retained positions, caches and Done)")
	}
}

func assertAblationGraph(t testing.TB, a, b *DependencyManager) {
	t.Helper()
	if a.edgeThreshold != b.edgeThreshold || !reflect.DeepEqual(a.nodes, b.nodes) {
		t.Fatal("active nodes or threshold differ")
	}
	for _, dm := range []*DependencyManager{a, b} {
		for _, adjacency := range []map[types.TxID]map[types.TxID]bool{dm.edges, dm.inverseEdges} {
			for u, neighbors := range adjacency {
				for v, exists := range neighbors {
					if exists && (!dm.nodes[u] || !dm.nodes[v]) {
						t.Fatal("graph contains an inactive endpoint")
					}
				}
			}
		}
	}
	for u := range a.nodes {
		if a.txStates[u] != b.txStates[u] {
			t.Fatal("active node classification differs")
		}
		for v := range a.nodes {
			if a.edges[u][v] != b.edges[u][v] || a.inverseEdges[v][u] != a.edges[u][v] || b.inverseEdges[v][u] != b.edges[u][v] {
				t.Fatal("effective graph or inverse graph differs")
			}
		}
	}
}

func ablationMetrics(x *ablationFixture) map[string]float64 {
	m := map[string]float64{"lo_max_size": float64(x.base.loMaxSize), "warm_max_lo": float64(x.warmMaxLO), "warm_rounds": float64(x.base.committed.FragmentSeq), "max_lo": 0, "output_tx": 0, "tree_edges": 0, "forest_roots": 0, "forest_max_depth": 0}
	for _, entry := range []struct {
		prefix string
		state  *EvidenceState
	}{{"pre_", x.base.committed}, {"updated_", x.candidate.preState}} {
		s, p := entry.state, entry.prefix
		for _, name := range []string{"blank", "shaded", "solid", "positions", "max_positions", "edges", "scc", "max_scc", "nontrivial_scc"} {
			m[p+name] = 0
		}
		m[p+"live"], m[p+"done"] = float64(len(s.Live)), float64(len(s.Done))
		m[p+"weights"] = float64(len(s.weights))
		for _, positions := range s.Pos {
			m[p+"positions"] += float64(len(positions))
			m[p+"max_positions"] = max(m[p+"max_positions"], float64(len(positions)))
		}
		for _, status := range s.states {
			m[p+string(status)]++
		}
		dm := rebuildDependencyManagerForTest(s, x.base.replicaCount, x.base.fFaulty, x.base.gamma)
		m[p+"active"] = float64(len(dm.nodes))
		for _, edges := range dm.edges {
			m[p+"edges"] += float64(len(edges))
		}
		for _, component := range TarjanSCC(dm.graph()) {
			m[p+"scc"]++
			m[p+"max_scc"] = max(m[p+"max_scc"], float64(len(component)))
			if len(component) > 1 {
				m[p+"nontrivial_scc"]++
			}
		}
	}
	m["new_positions"] = m["updated_positions"] - m["pre_positions"]
	m["new_live"] = m["updated_live"] - m["pre_live"]
	for _, order := range x.orders {
		m["lo_occurrences"] += float64(len(order.OrderedTxs))
		m["max_lo"] = max(m["max_lo"], float64(len(order.OrderedTxs)))
	}
	f := x.candidate.fragment
	m["output_batches"] = float64(len(f.FinalOrder.Batches))
	for _, batch := range f.FinalOrder.Batches {
		m["output_tx"] += float64(len(batch.Transactions))
	}
	for _, claim := range f.Certificate.Part {
		m["tree_edges"] += float64(len(claim.InTree) + len(claim.OutTree))
	}
	m["block_records"] = float64(len(f.Certificate.BlockForest))
	for _, record := range f.Certificate.BlockForest {
		if !record.HasParent {
			m["forest_roots"]++
		}
		m["forest_max_depth"] = max(m["forest_max_depth"], float64(record.Depth))
	}
	m["excluded_solid"] = m["updated_solid"] - m["output_tx"]
	return m
}

// Reuse production SCC/Kahn primitives, but never build a new certificate.
// This is the output portion of buildProposal, including maximal safe closure.
func recomputeSafeOrderForTest(state *EvidenceState, dm *DependencyManager) *types.FairOrderFragment {
	graph := dm.graph()
	sccs := TarjanSCC(graph)
	condensation, _, infos := BuildCondensationAndSCCInfo(graph, sccs, state.states)
	unsafe := make(map[int]bool)
	for index, info := range infos {
		if !info.IsSolid {
			unsafe[index] = true
		}
	}
	for _, from := range topoSortCondensation(condensation, infos, nil) {
		if unsafe[from] {
			for _, to := range condensation[from] {
				unsafe[to] = true
			}
		}
	}
	selected := make(map[int]bool)
	for index := range infos {
		if !unsafe[index] {
			selected[index] = true
		}
	}
	order := &types.FairOrderFragment{}
	for rank, index := range topoSortCondensation(condensation, infos, selected) {
		order.Batches = append(order.Batches, types.FairnessBatch{
			Index:        state.FairnessBatchCounter + uint64(rank) + 1,
			Transactions: infos[index].Txs, // Tarjan sorts each SCC by TxID.
		})
	}
	return order
}

func verifyRecomputedOutputForTest(state *EvidenceState, dm *DependencyManager, order *types.FairOrderFragment, certificate *types.StructuralCertificate) error {
	if order == nil || certificate == nil {
		return fmt.Errorf("missing order or certificate")
	}
	expected := recomputeSafeOrderForTest(state, dm)
	if len(order.Batches) != len(expected.Batches) || len(certificate.Part) != len(expected.Batches) {
		return fmt.Errorf("maximal safe partition differs")
	}
	included := make(map[types.TxID]bool)
	for i, batch := range expected.Batches {
		claim := certificate.Part[i]
		if order.Batches[i].Index != batch.Index || !sameIDs(order.Batches[i].Transactions, batch.Transactions) || claim.Rank != uint64(i) || !sameIDs(claim.Transactions, batch.Transactions) {
			return fmt.Errorf("SCC membership, batch index or deterministic order differs")
		}
		component := make(map[types.TxID]bool, len(batch.Transactions))
		for _, id := range batch.Transactions {
			included[id], component[id] = true, true
		}
		if len(claim.InTree) != len(component)-1 || len(claim.OutTree) != len(component)-1 {
			return fmt.Errorf("tree size mismatch")
		}
		if err := verifyTreeOnGraphForTest(dm, batch.Transactions[0], component, claim.InTree, true); err != nil {
			return err
		}
		if err := verifyTreeOnGraphForTest(dm, batch.Transactions[0], component, claim.OutTree, false); err != nil {
			return err
		}
	}
	// Exact independently computed output already establishes closure, SCC
	// maximality and order. Only validate the supplied proof structures here;
	// do not repeat the original pair/frontier scans or claim-based Kahn sort.
	return verifyBlockForestOnGraphForTest(state, dm.nodes, included, certificate.BlockForest, dm)
}

func verifyCandidateByRecomputeForTest(s *OFOService, fragment *types.VerifiableFairOrderFragment, sender uint64) bool {
	s.rwMu.Lock()
	defer s.rwMu.Unlock()
	digest := types.FragmentDigest(fragment)
	if sender != s.authContext.LeaderID || fragment.LeaderID != s.authContext.LeaderID || fragment.Epoch != s.authContext.Epoch || fragment.AuthContextID != s.authContext.Identifier {
		return false
	}
	if fragment.FragmentSeq != s.committed.FragmentSeq+1 || fragment.PreviousStateID != s.committed.StateID || fragment.PreviousFragmentDigest != s.latestCommittedFragment {
		return false
	}
	if !s.auth.VerifyLeader(s.authContext, fragment.LeaderID, digest, fragment.LeaderSignature) {
		return false
	}
	if s.pending != nil {
		return s.pending.digest == digest
	}
	pre := s.committed.clone()
	if _, _, err := applyEvidenceWithTouches(pre, fragment.Evidence, fragment.Epoch, fragment.FragmentSeq, s.replicaCount, s.fFaulty, s.gamma, s.loMaxSize, s.auth, s.admission, false); err != nil {
		return false
	}
	if pre.StateID != fragment.CandidateEvidenceStateID {
		return false
	}
	manager := rebuildDependencyManagerForTest(pre, s.replicaCount, s.fFaulty, s.gamma)
	if err := verifyRecomputedOutputForTest(pre, manager, fragment.FinalOrder, fragment.Certificate); err != nil {
		return false
	}
	post := pre.clone()
	// Followers discard the temporary graph. Match production finalization;
	// do not charge them for persisting or cleaning an unneeded graph cache.
	finalize(post, NewDependencyManager(s.replicaCount, s.fFaulty, s.gamma), fragment.FinalOrder, fragment.Certificate.Part)
	if post.StateID != fragment.CandidatePostStateID {
		return false
	}
	s.pending = &pendingCandidate{digest: digest, preState: pre, postState: post, fragment: fragment, done: make(chan struct{})}
	return true
}

// Proof shape checks mirror dependency.go; only edge lookups use the graph
// already built by the recomputing verifier. Differential tests cover both.
func verifyTreeOnGraphForTest(dm *DependencyManager, root types.TxID, component map[types.TxID]bool, tree []types.TreeEdge, inward bool) error {
	adjacency := make(map[types.TxID][]types.TxID)
	parentCount := make(map[types.TxID]int)
	for _, edge := range tree {
		if !component[edge.From] || !component[edge.To] || !dm.edges[edge.From][edge.To] {
			return fmt.Errorf("certificate tree contains a non-local EdgePred edge")
		}
		if inward {
			adjacency[edge.To] = append(adjacency[edge.To], edge.From)
			parentCount[edge.From]++
		} else {
			adjacency[edge.From] = append(adjacency[edge.From], edge.To)
			parentCount[edge.To]++
		}
	}
	for id := range component {
		expected := 1
		if id == root {
			expected = 0
		}
		if parentCount[id] != expected {
			return fmt.Errorf("certificate tree does not assign one parent per non-root")
		}
	}
	visited := map[types.TxID]bool{root: true}
	queue := []types.TxID{root}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[current] {
			if !visited[next] {
				visited[next] = true
				queue = append(queue, next)
			}
		}
	}
	if len(visited) != len(component) {
		return fmt.Errorf("certificate tree does not span claimed SCC")
	}
	return nil
}

func verifyBlockForestOnGraphForTest(state *EvidenceState, active, included map[types.TxID]bool, records []types.BlockRecord, dm *DependencyManager) error {
	recordByID := make(map[types.TxID]types.BlockRecord, len(records))
	for _, record := range records {
		if !active[record.Vertex] || included[record.Vertex] {
			return fmt.Errorf("block forest contains a vertex outside A\\F")
		}
		if _, duplicate := recordByID[record.Vertex]; duplicate {
			return fmt.Errorf("duplicate block forest record")
		}
		if !record.HasParent {
			if record.Depth != 0 || state.states[record.Vertex] != types.StateShaded {
				return fmt.Errorf("block forest root is not Shaded at depth zero")
			}
		} else if record.Depth == 0 {
			return fmt.Errorf("non-root block record has zero depth")
		}
		recordByID[record.Vertex] = record
	}
	for _, record := range records {
		if !record.HasParent {
			continue
		}
		parent, ok := recordByID[record.Parent]
		if !ok || record.Depth != parent.Depth+1 || !dm.edges[record.Parent][record.Vertex] {
			return fmt.Errorf("invalid block forest parent edge or depth")
		}
	}
	used := make(map[types.TxID]bool)
	for id := range active {
		if included[id] || state.states[id] != types.StateSolid {
			continue
		}
		current := id
		for {
			record, ok := recordByID[current]
			if !ok {
				return fmt.Errorf("excluded Solid transaction has no blocker path")
			}
			used[current] = true
			if !record.HasParent {
				break
			}
			current = record.Parent
		}
	}
	if len(used) != len(records) {
		return fmt.Errorf("block forest contains records outside certified paths")
	}
	return nil
}
