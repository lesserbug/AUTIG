package ofo

import (
	"SpeedFair_simplify/pkg/types"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
)

type edgeKey struct {
	u, v types.TxID
}

type pairKey struct {
	a, b types.TxID
}

func makePair(u, v types.TxID) pairKey {
	if types.LessTxID(u, v) {
		return pairKey{u, v}
	}
	return pairKey{v, u}
}

type DoneRecord struct {
	FragmentSeq uint64
	BatchIndex  uint64
}

// EvidenceState is authoritative. visibility, weights and states are
// deterministic caches and are deliberately excluded from StateID.
type EvidenceState struct {
	Epoch                uint64
	FragmentSeq          uint64
	FairnessBatchCounter uint64
	Seq                  map[uint64]uint64
	Head                 map[uint64][32]byte
	Live                 map[types.TxID]bool
	TxRef                map[types.TxID]types.TxID
	Pos                  map[uint64]map[types.TxID]uint64
	Done                 map[types.TxID]DoneRecord
	DoneRoot             [32]byte
	StateID              [32]byte
	visibility           map[types.TxID]int
	weights              map[edgeKey]int
	states               map[types.TxID]types.TxState
}

func NewEvidenceState(epoch uint64, replicaCount uint64, genesisStateID [32]byte) *EvidenceState {
	state := &EvidenceState{
		Epoch:      epoch,
		Seq:        make(map[uint64]uint64, replicaCount),
		Head:       make(map[uint64][32]byte, replicaCount),
		Live:       make(map[types.TxID]bool),
		TxRef:      make(map[types.TxID]types.TxID),
		Pos:        make(map[uint64]map[types.TxID]uint64, replicaCount),
		Done:       make(map[types.TxID]DoneRecord),
		visibility: make(map[types.TxID]int),
		weights:    make(map[edgeKey]int),
		states:     make(map[types.TxID]types.TxState),
		StateID:    genesisStateID,
	}
	for replicaID := uint64(0); replicaID < replicaCount; replicaID++ {
		state.Pos[replicaID] = make(map[types.TxID]uint64)
	}
	state.DoneRoot = finalizedSetRoot(state.Done)
	return state
}

func (state *EvidenceState) clone() *EvidenceState {
	copyState := &EvidenceState{
		Epoch:                state.Epoch,
		FragmentSeq:          state.FragmentSeq,
		FairnessBatchCounter: state.FairnessBatchCounter,
		Seq:                  make(map[uint64]uint64, len(state.Seq)),
		Head:                 make(map[uint64][32]byte, len(state.Head)),
		Live:                 make(map[types.TxID]bool, len(state.Live)),
		TxRef:                make(map[types.TxID]types.TxID, len(state.TxRef)),
		Pos:                  make(map[uint64]map[types.TxID]uint64, len(state.Pos)),
		Done:                 make(map[types.TxID]DoneRecord, len(state.Done)),
		DoneRoot:             state.DoneRoot,
		StateID:              state.StateID,
		visibility:           make(map[types.TxID]int, len(state.visibility)),
		weights:              make(map[edgeKey]int, len(state.weights)),
		states:               make(map[types.TxID]types.TxState, len(state.states)),
	}
	for id, value := range state.Seq {
		copyState.Seq[id] = value
	}
	for id, value := range state.Head {
		copyState.Head[id] = value
	}
	for id := range state.Live {
		copyState.Live[id] = true
	}
	for id, ref := range state.TxRef {
		copyState.TxRef[id] = ref
	}
	for replicaID, positions := range state.Pos {
		copyState.Pos[replicaID] = make(map[types.TxID]uint64, len(positions))
		for id, position := range positions {
			copyState.Pos[replicaID][id] = position
		}
	}
	for id, record := range state.Done {
		copyState.Done[id] = record
	}
	for id, value := range state.visibility {
		copyState.visibility[id] = value
	}
	for key, value := range state.weights {
		copyState.weights[key] = value
	}
	for id, value := range state.states {
		copyState.states[id] = value
	}
	return copyState
}

func finalizedSetRoot(done map[types.TxID]DoneRecord) [32]byte {
	var b bytes.Buffer
	b.WriteString("AUTIG/finalized-set")
	ids := make([]types.TxID, 0, len(done))
	for id := range done {
		ids = append(ids, id)
	}
	types.SortTxIDs(ids)
	writeU64(&b, uint64(len(ids)))
	for _, id := range ids {
		record := done[id]
		b.Write(id[:])
		writeU64(&b, record.FragmentSeq)
		writeU64(&b, record.BatchIndex)
	}
	return sha256.Sum256(b.Bytes())
}

func writeU64(b *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	b.Write(encoded[:])
}

func sortedSet(set map[types.TxID]bool) []types.TxID {
	ids := make([]types.TxID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	types.SortTxIDs(ids)
	return ids
}

func applyEvidence(state *EvidenceState, orders []*types.LocalOrder, epoch, fragmentSeq, n, f uint64, gamma float64, maxOrderSize int, auth types.Authenticator, admission types.TransactionAdmission) (map[types.TxID]bool, map[pairKey]bool, error) {
	if fragmentSeq != state.FragmentSeq+1 {
		return nil, nil, fmt.Errorf("fragment sequence %d does not extend %d", fragmentSeq, state.FragmentSeq)
	}
	if len(orders) != int(n-f) {
		return nil, nil, fmt.Errorf("evidence batch contains %d senders, want %d", len(orders), n-f)
	}

	senders := make(map[uint64]bool, len(orders))
	resolved := make(map[types.TxID]types.TxID)
	for _, order := range orders {
		if order == nil {
			return nil, nil, fmt.Errorf("evidence batch contains a nil extension")
		}
		if senders[order.ReplicaID] {
			return nil, nil, fmt.Errorf("duplicate evidence sender %d", order.ReplicaID)
		}
		senders[order.ReplicaID] = true
		if order.ReplicaID >= n || order.Epoch != epoch || order.FragmentSeq != fragmentSeq {
			return nil, nil, fmt.Errorf("local order context mismatch for replica %d", order.ReplicaID)
		}
		if len(order.OrderedTxs) > maxOrderSize {
			return nil, nil, fmt.Errorf("local order from replica %d exceeds size limit", order.ReplicaID)
		}
		if order.NewPosition != order.PrevPosition+uint64(len(order.OrderedTxs)) {
			return nil, nil, fmt.Errorf("local order positions are not contiguous for replica %d", order.ReplicaID)
		}
		if order.PrevPosition != state.Seq[order.ReplicaID] || order.PrevHead != state.Head[order.ReplicaID] {
			return nil, nil, fmt.Errorf("local order predecessor mismatch for replica %d", order.ReplicaID)
		}
		if order.NewHead != types.LocalOrderHead(order) {
			return nil, nil, fmt.Errorf("local order hash-chain head mismatch for replica %d", order.ReplicaID)
		}
		if !auth.VerifyReplica(order.ReplicaID, types.LocalOrderDigest(order), order.Signature) {
			return nil, nil, fmt.Errorf("invalid local order signature for replica %d", order.ReplicaID)
		}
		seen := make(map[types.TxID]bool, len(order.OrderedTxs))
		for _, id := range order.OrderedTxs {
			if seen[id] {
				return nil, nil, fmt.Errorf("duplicate identifier in replica %d extension", order.ReplicaID)
			}
			seen[id] = true
			if _, finalized := state.Done[id]; finalized {
				continue
			}
			if _, exists := state.Pos[order.ReplicaID][id]; exists {
				return nil, nil, fmt.Errorf("replica %d assigned a second live first position", order.ReplicaID)
			}
			canonical, reference, err := admission.Resolve(id)
			if err != nil {
				return nil, nil, fmt.Errorf("transaction %x is unavailable: %w", id, err)
			}
			if types.TransactionID(canonical) != id || reference != id {
				return nil, nil, fmt.Errorf("transaction %x has an invalid content commitment", id)
			}
			if err := admission.ValidateAdmission(canonical); err != nil {
				return nil, nil, fmt.Errorf("transaction %x failed admission: %w", id, err)
			}
			if previous, exists := state.TxRef[id]; exists && previous != reference {
				return nil, nil, fmt.Errorf("transaction %x changed content reference", id)
			}
			if previous, exists := resolved[id]; exists && previous != reference {
				return nil, nil, fmt.Errorf("transaction %x has inconsistent content references", id)
			}
			resolved[id] = reference
		}
	}

	state.StateID = types.PreStateIdentifier(epoch, fragmentSeq, state.StateID, types.EvidenceBatchDigest(orders))
	state.Epoch = epoch
	touchedNodes := make(map[types.TxID]bool)
	touchedPairs := make(map[pairKey]bool)
	for _, order := range orders {
		newPositions := make(map[types.TxID]bool)
		position := order.PrevPosition
		for _, id := range order.OrderedTxs {
			position++
			if _, finalized := state.Done[id]; finalized {
				continue
			}
			if !state.Live[id] {
				state.Live[id] = true
				state.TxRef[id] = resolved[id]
				state.states[id] = types.StateBlank
			}
			state.Pos[order.ReplicaID][id] = position
			state.visibility[id]++
			newPositions[id] = true
			touchedNodes[id] = true
		}

		positioned := make([]types.TxID, 0, len(state.Pos[order.ReplicaID]))
		for id := range state.Pos[order.ReplicaID] {
			positioned = append(positioned, id)
		}
		for i := 0; i < len(positioned); i++ {
			for j := i + 1; j < len(positioned); j++ {
				u, v := positioned[i], positioned[j]
				if !newPositions[u] && !newPositions[v] {
					continue
				}
				if state.Pos[order.ReplicaID][u] < state.Pos[order.ReplicaID][v] {
					state.weights[edgeKey{u, v}]++
				} else {
					state.weights[edgeKey{v, u}]++
				}
				touchedPairs[makePair(u, v)] = true
			}
		}
		state.Seq[order.ReplicaID] = order.NewPosition
		state.Head[order.ReplicaID] = order.NewHead
	}

	solidThreshold := types.CalculateSolidThreshold(n, f)
	nonBlankThreshold := types.CalculateEdgeThreshold(n, f, gamma)
	for id := range touchedNodes {
		oldState := state.states[id]
		newState := types.StateBlank
		if state.visibility[id] >= solidThreshold {
			newState = types.StateSolid
		} else if state.visibility[id] >= nonBlankThreshold {
			newState = types.StateShaded
		}
		state.states[id] = newState
		if oldState == types.StateBlank && newState != types.StateBlank {
			for other := range state.Live {
				if other != id {
					touchedPairs[makePair(id, other)] = true
				}
			}
		}
	}
	state.FragmentSeq = fragmentSeq
	return touchedNodes, touchedPairs, nil
}

// DependencyManager is the order leader's persistent materialization of the
// active graph. Its contents are caches derived from EvidenceState.
type DependencyManager struct {
	nodes         map[types.TxID]bool
	txStates      map[types.TxID]types.TxState
	weights       map[edgeKey]int
	edges         map[types.TxID]map[types.TxID]bool
	inverseEdges  map[types.TxID]map[types.TxID]bool
	edgeThreshold int
}

func NewDependencyManager(n, f uint64, gamma float64) *DependencyManager {
	return &DependencyManager{
		nodes: make(map[types.TxID]bool), txStates: make(map[types.TxID]types.TxState),
		weights: make(map[edgeKey]int), edges: make(map[types.TxID]map[types.TxID]bool),
		inverseEdges:  make(map[types.TxID]map[types.TxID]bool),
		edgeThreshold: types.CalculateEdgeThreshold(n, f, gamma),
	}
}

func (dm *DependencyManager) clone() *DependencyManager {
	copyManager := &DependencyManager{
		nodes: make(map[types.TxID]bool, len(dm.nodes)), txStates: make(map[types.TxID]types.TxState, len(dm.txStates)),
		weights: make(map[edgeKey]int, len(dm.weights)), edges: make(map[types.TxID]map[types.TxID]bool, len(dm.edges)),
		inverseEdges: make(map[types.TxID]map[types.TxID]bool, len(dm.inverseEdges)), edgeThreshold: dm.edgeThreshold,
	}
	for id := range dm.nodes {
		copyManager.nodes[id] = true
	}
	for id, state := range dm.txStates {
		copyManager.txStates[id] = state
	}
	for key, weight := range dm.weights {
		copyManager.weights[key] = weight
	}
	for id, neighbors := range dm.edges {
		copyManager.edges[id] = make(map[types.TxID]bool, len(neighbors))
		for v := range neighbors {
			copyManager.edges[id][v] = true
		}
	}
	for id, neighbors := range dm.inverseEdges {
		copyManager.inverseEdges[id] = make(map[types.TxID]bool, len(neighbors))
		for v := range neighbors {
			copyManager.inverseEdges[id][v] = true
		}
	}
	return copyManager
}

func (dm *DependencyManager) refresh(state *EvidenceState, touchedNodes map[types.TxID]bool, touchedPairs map[pairKey]bool) {
	for id := range touchedNodes {
		dm.txStates[id] = state.states[id]
		if state.states[id] != types.StateBlank && !dm.nodes[id] {
			dm.nodes[id] = true
			dm.edges[id] = make(map[types.TxID]bool)
			dm.inverseEdges[id] = make(map[types.TxID]bool)
			for other := range state.Live {
				if other != id {
					touchedPairs[makePair(id, other)] = true
				}
			}
		}
	}
	for pair := range touchedPairs {
		dm.weights[edgeKey{pair.a, pair.b}] = state.weights[edgeKey{pair.a, pair.b}]
		dm.weights[edgeKey{pair.b, pair.a}] = state.weights[edgeKey{pair.b, pair.a}]
		dm.refreshPair(pair.a, pair.b)
	}
}

func (dm *DependencyManager) refreshPair(u, v types.TxID) {
	if dm.edges[u] != nil {
		delete(dm.edges[u], v)
	}
	if dm.edges[v] != nil {
		delete(dm.edges[v], u)
	}
	if dm.inverseEdges[u] != nil {
		delete(dm.inverseEdges[u], v)
	}
	if dm.inverseEdges[v] != nil {
		delete(dm.inverseEdges[v], u)
	}
	if !dm.nodes[u] || !dm.nodes[v] {
		return
	}
	wUV, wVU := dm.weights[edgeKey{u, v}], dm.weights[edgeKey{v, u}]
	if edgePred(u, v, wUV, wVU, dm.edgeThreshold) {
		dm.edges[u][v] = true
		dm.inverseEdges[v][u] = true
	} else if edgePred(v, u, wVU, wUV, dm.edgeThreshold) {
		dm.edges[v][u] = true
		dm.inverseEdges[u][v] = true
	}
}

func edgePred(u, v types.TxID, wUV, wVU, threshold int) bool {
	return wUV >= threshold && (wUV > wVU || (wUV == wVU && types.LessTxID(u, v)))
}

func (dm *DependencyManager) graph() *types.DependencyGraph {
	graph := &types.DependencyGraph{Nodes: make(map[types.TxID]bool, len(dm.nodes)), Edges: make(map[types.TxID][]types.TxID, len(dm.edges))}
	for id := range dm.nodes {
		graph.Nodes[id] = true
	}
	for u, neighbors := range dm.edges {
		for v := range neighbors {
			graph.Edges[u] = append(graph.Edges[u], v)
		}
		types.SortTxIDs(graph.Edges[u])
	}
	return graph
}

func (dm *DependencyManager) buildProposal(state *EvidenceState) (*types.FairOrderFragment, *types.StructuralCertificate, error) {
	graph := dm.graph()
	if len(graph.Nodes) == 0 {
		return &types.FairOrderFragment{}, &types.StructuralCertificate{}, nil
	}
	sccs := TarjanSCC(graph)
	condensation, nodeToSCC, infos := BuildCondensationAndSCCInfo(graph, sccs, state.states)
	all := topoSortCondensation(condensation, infos, nil)
	unsafe := make(map[int]bool)
	for index, info := range infos {
		if !info.IsSolid {
			unsafe[index] = true
		}
	}
	for _, from := range all {
		if !unsafe[from] {
			continue
		}
		for _, to := range condensation[from] {
			unsafe[to] = true
		}
	}
	selected := make(map[int]bool)
	for index := range infos {
		if !unsafe[index] {
			selected[index] = true
		}
	}
	ordered := topoSortCondensation(condensation, infos, selected)

	certificate := &types.StructuralCertificate{}
	finalOrder := &types.FairOrderFragment{}
	for rank, index := range ordered {
		transactions := append([]types.TxID(nil), infos[index].Txs...)
		types.SortTxIDs(transactions)
		certificate.Part = append(certificate.Part, types.SCCClaim{
			Transactions: transactions,
			InTree:       spanningTree(transactions[0], transactions, dm.inverseEdges, true),
			OutTree:      spanningTree(transactions[0], transactions, dm.edges, false),
			Rank:         uint64(rank),
		})
		finalOrder.Batches = append(finalOrder.Batches, types.FairnessBatch{
			Index:        state.FairnessBatchCounter + uint64(rank) + 1,
			Transactions: transactions,
		})
	}
	certificate.BlockForest = dm.blockForest(state, selected, nodeToSCC)
	return finalOrder, certificate, nil
}

func spanningTree(root types.TxID, component []types.TxID, adjacency map[types.TxID]map[types.TxID]bool, inward bool) []types.TreeEdge {
	inComponent := make(map[types.TxID]bool, len(component))
	for _, id := range component {
		inComponent[id] = true
	}
	visited := map[types.TxID]bool{root: true}
	queue := []types.TxID{root}
	var tree []types.TreeEdge
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		neighbors := make([]types.TxID, 0, len(adjacency[current]))
		for next := range adjacency[current] {
			if inComponent[next] && !visited[next] {
				neighbors = append(neighbors, next)
			}
		}
		types.SortTxIDs(neighbors)
		for _, next := range neighbors {
			if visited[next] {
				continue
			}
			visited[next] = true
			queue = append(queue, next)
			if inward {
				tree = append(tree, types.TreeEdge{From: next, To: current})
			} else {
				tree = append(tree, types.TreeEdge{From: current, To: next})
			}
		}
	}
	return tree
}

func (dm *DependencyManager) blockForest(state *EvidenceState, selected map[int]bool, nodeToSCC map[types.TxID]int) []types.BlockRecord {
	parents := make(map[types.TxID]types.TxID)
	depth := make(map[types.TxID]uint64)
	roots := make(map[types.TxID]bool)
	queue := make([]types.TxID, 0)
	for id := range dm.nodes {
		if state.states[id] == types.StateShaded {
			roots[id] = true
			queue = append(queue, id)
		}
	}
	types.SortTxIDs(queue)
	visited := make(map[types.TxID]bool, len(queue))
	for _, id := range queue {
		visited[id] = true
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		neighbors := make([]types.TxID, 0, len(dm.edges[current]))
		for next := range dm.edges[current] {
			if !visited[next] {
				neighbors = append(neighbors, next)
			}
		}
		types.SortTxIDs(neighbors)
		for _, next := range neighbors {
			if visited[next] {
				continue
			}
			visited[next] = true
			parents[next] = current
			depth[next] = depth[current] + 1
			queue = append(queue, next)
		}
	}
	used := make(map[types.TxID]bool)
	for id := range dm.nodes {
		if state.states[id] != types.StateSolid || selected[nodeToSCC[id]] {
			continue
		}
		current := id
		for {
			used[current] = true
			if roots[current] {
				break
			}
			parent, ok := parents[current]
			if !ok {
				break
			}
			current = parent
		}
	}
	ids := sortedSet(used)
	records := make([]types.BlockRecord, 0, len(ids))
	for _, id := range ids {
		parent, hasParent := parents[id]
		records = append(records, types.BlockRecord{Vertex: id, Parent: parent, HasParent: hasParent, Depth: depth[id]})
	}
	return records
}

func finalize(state *EvidenceState, manager *DependencyManager, finalOrder *types.FairOrderFragment, part []types.SCCClaim) {
	finalized := make(map[types.TxID]bool)
	for _, batch := range finalOrder.Batches {
		for _, id := range batch.Transactions {
			state.Done[id] = DoneRecord{FragmentSeq: state.FragmentSeq, BatchIndex: batch.Index}
			finalized[id] = true
		}
		state.FairnessBatchCounter = batch.Index
	}
	for id := range finalized {
		delete(state.Live, id)
		delete(state.TxRef, id)
		delete(state.visibility, id)
		delete(state.states, id)
		for _, positions := range state.Pos {
			delete(positions, id)
		}
	}
	for key := range state.weights {
		if finalized[key.u] || finalized[key.v] {
			delete(state.weights, key)
		}
	}
	manager.removeFinalized(finalized)
	state.DoneRoot = finalizedSetRoot(state.Done)
	state.StateID = types.PostStateIdentifier(state.Epoch, state.FragmentSeq, state.StateID, finalOrder, part)
}

func (dm *DependencyManager) removeFinalized(finalized map[types.TxID]bool) {
	for id := range finalized {
		for parent := range dm.inverseEdges[id] {
			delete(dm.edges[parent], id)
		}
		for child := range dm.edges[id] {
			delete(dm.inverseEdges[child], id)
		}
		delete(dm.nodes, id)
		delete(dm.txStates, id)
		delete(dm.edges, id)
		delete(dm.inverseEdges, id)
	}
	for key := range dm.weights {
		if finalized[key.u] || finalized[key.v] {
			delete(dm.weights, key)
		}
	}
}

func (dm *DependencyManager) GetUTIGNodesCount() int { return len(dm.nodes) }

func BuildCondensationAndSCCInfo(graph *types.DependencyGraph, sccs [][]types.TxID, states map[types.TxID]types.TxState) (map[int][]int, map[types.TxID]int, map[int]types.SCCInfo) {
	nodeToSCC := make(map[types.TxID]int)
	infos := make(map[int]types.SCCInfo, len(sccs))
	for index, component := range sccs {
		allSolid := true
		for _, id := range component {
			nodeToSCC[id] = index
			if states[id] != types.StateSolid {
				allSolid = false
			}
		}
		infos[index] = types.SCCInfo{ID: index, Txs: component, IsSolid: allSolid}
	}
	condensation := make(map[int][]int, len(sccs))
	seen := make(map[[2]int]bool)
	for index := range sccs {
		condensation[index] = nil
	}
	for from, neighbors := range graph.Edges {
		for _, to := range neighbors {
			u, v := nodeToSCC[from], nodeToSCC[to]
			if u != v && !seen[[2]int{u, v}] {
				seen[[2]int{u, v}] = true
				condensation[u] = append(condensation[u], v)
			}
		}
	}
	return condensation, nodeToSCC, infos
}

func TarjanSCC(graph *types.DependencyGraph) [][]types.TxID {
	index := 0
	indices := make(map[types.TxID]int)
	lowlink := make(map[types.TxID]int)
	var stack []types.TxID
	onStack := make(map[types.TxID]bool)
	var components [][]types.TxID
	var connect func(types.TxID)
	connect = func(v types.TxID) {
		indices[v] = index
		lowlink[v] = index
		index++
		stack = append(stack, v)
		onStack[v] = true
		neighbors := append([]types.TxID(nil), graph.Edges[v]...)
		types.SortTxIDs(neighbors)
		for _, w := range neighbors {
			if _, visited := indices[w]; !visited {
				connect(w)
				if lowlink[w] < lowlink[v] {
					lowlink[v] = lowlink[w]
				}
			} else if onStack[w] && indices[w] < lowlink[v] {
				lowlink[v] = indices[w]
			}
		}
		if lowlink[v] == indices[v] {
			var component []types.TxID
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				component = append(component, w)
				if w == v {
					break
				}
			}
			types.SortTxIDs(component)
			components = append(components, component)
		}
	}
	nodes := sortedSet(graph.Nodes)
	for _, id := range nodes {
		if _, visited := indices[id]; !visited {
			connect(id)
		}
	}
	return components
}

func topoSortCondensation(condensation map[int][]int, infos map[int]types.SCCInfo, included map[int]bool) []int {
	use := func(id int) bool { return included == nil || included[id] }
	indegree := make(map[int]int)
	for from, neighbors := range condensation {
		if !use(from) {
			continue
		}
		for _, to := range neighbors {
			if use(to) {
				indegree[to]++
			}
		}
	}
	var ready []int
	for id := range infos {
		if use(id) && indegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	lessComponent := func(i, j int) bool { return types.LessTxID(infos[ready[i]].Txs[0], infos[ready[j]].Txs[0]) }
	sort.Slice(ready, lessComponent)
	var result []int
	for len(ready) > 0 {
		from := ready[0]
		ready = ready[1:]
		result = append(result, from)
		for _, to := range condensation[from] {
			if !use(to) {
				continue
			}
			indegree[to]--
			if indegree[to] == 0 {
				ready = append(ready, to)
			}
		}
		sort.Slice(ready, lessComponent)
	}
	return result
}

func verifyStructuralCertificate(state *EvidenceState, finalOrder *types.FairOrderFragment, certificate *types.StructuralCertificate, n, f uint64, gamma float64) error {
	if finalOrder == nil || certificate == nil {
		return fmt.Errorf("missing final order or structural certificate")
	}
	threshold := types.CalculateEdgeThreshold(n, f, gamma)
	active := make(map[types.TxID]bool)
	for id, txState := range state.states {
		if state.Live[id] && txState != types.StateBlank {
			active[id] = true
		}
	}
	included := make(map[types.TxID]bool)
	for batchOffset, batch := range finalOrder.Batches {
		if batch.Index != state.FairnessBatchCounter+uint64(batchOffset)+1 {
			return fmt.Errorf("fairness-batch index mismatch")
		}
		for index, id := range batch.Transactions {
			if included[id] {
				return fmt.Errorf("duplicate included identifier")
			}
			if _, done := state.Done[id]; done || !active[id] || state.states[id] != types.StateSolid {
				return fmt.Errorf("included transaction is not a live active Solid transaction")
			}
			if index > 0 && !types.LessTxID(batch.Transactions[index-1], id) {
				return fmt.Errorf("intra-SCC serialization is not transaction-ID order")
			}
			included[id] = true
		}
	}
	if len(certificate.Part) != len(finalOrder.Batches) {
		return fmt.Errorf("claimed partition does not match fairness batches")
	}
	componentOf := make(map[types.TxID]int, len(included))
	for index, claim := range certificate.Part {
		if claim.Rank != uint64(index) || !sameIDs(claim.Transactions, finalOrder.Batches[index].Transactions) || len(claim.Transactions) == 0 {
			return fmt.Errorf("claimed SCC rank or membership mismatch")
		}
		for _, id := range claim.Transactions {
			if _, exists := componentOf[id]; exists {
				return fmt.Errorf("identifier appears in multiple claimed SCCs")
			}
			componentOf[id] = index
		}
		if err := verifyClaimedTrees(state, claim, threshold); err != nil {
			return err
		}
	}
	if len(componentOf) != len(included) {
		return fmt.Errorf("claimed SCC partition is incomplete")
	}

	condensation := make(map[int][]int, len(certificate.Part))
	seenCross := make(map[[2]int]bool)
	ids := sortedSet(included)
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			u, v := ids[i], ids[j]
			from, to, exists := selectedEdge(state, u, v, threshold)
			if !exists {
				continue
			}
			fromComponent, toComponent := componentOf[from], componentOf[to]
			if fromComponent == toComponent {
				continue
			}
			if fromComponent >= toComponent {
				return fmt.Errorf("cross-component edge does not increase rank")
			}
			key := [2]int{fromComponent, toComponent}
			if !seenCross[key] {
				seenCross[key] = true
				condensation[fromComponent] = append(condensation[fromComponent], toComponent)
			}
		}
	}
	for index := range certificate.Part {
		if _, ok := condensation[index]; !ok {
			condensation[index] = nil
		}
	}
	infos := make(map[int]types.SCCInfo, len(certificate.Part))
	selectedComponents := make(map[int]bool, len(certificate.Part))
	for index, claim := range certificate.Part {
		infos[index] = types.SCCInfo{ID: index, Txs: claim.Transactions, IsSolid: true}
		selectedComponents[index] = true
	}
	ordered := topoSortCondensation(condensation, infos, selectedComponents)
	for rank, component := range ordered {
		if component != rank {
			return fmt.Errorf("claimed ranks do not match deterministic Kahn order")
		}
	}

	for outside := range active {
		if included[outside] {
			continue
		}
		for inside := range included {
			if localEdgePred(state, outside, inside, threshold) {
				return fmt.Errorf("selected set has an incoming active frontier edge")
			}
		}
	}
	return verifyBlockForest(state, active, included, certificate.BlockForest, threshold)
}

func sameIDs(a, b []types.TxID) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func localEdgePred(state *EvidenceState, u, v types.TxID, threshold int) bool {
	if state.states[u] == types.StateBlank || state.states[v] == types.StateBlank {
		return false
	}
	return edgePred(u, v, state.weights[edgeKey{u, v}], state.weights[edgeKey{v, u}], threshold)
}

func selectedEdge(state *EvidenceState, u, v types.TxID, threshold int) (types.TxID, types.TxID, bool) {
	if localEdgePred(state, u, v, threshold) {
		return u, v, true
	}
	if localEdgePred(state, v, u, threshold) {
		return v, u, true
	}
	return types.TxID{}, types.TxID{}, false
}

func verifyClaimedTrees(state *EvidenceState, claim types.SCCClaim, threshold int) error {
	inComponent := make(map[types.TxID]bool, len(claim.Transactions))
	for _, id := range claim.Transactions {
		inComponent[id] = true
	}
	root := claim.Transactions[0]
	if len(claim.InTree) != len(claim.Transactions)-1 || len(claim.OutTree) != len(claim.Transactions)-1 {
		return fmt.Errorf("claimed SCC tree size mismatch")
	}
	if err := verifyTree(state, root, inComponent, claim.OutTree, false, threshold); err != nil {
		return err
	}
	return verifyTree(state, root, inComponent, claim.InTree, true, threshold)
}

func verifyTree(state *EvidenceState, root types.TxID, component map[types.TxID]bool, tree []types.TreeEdge, inward bool, threshold int) error {
	adjacency := make(map[types.TxID][]types.TxID)
	parentCount := make(map[types.TxID]int)
	for _, edge := range tree {
		if !component[edge.From] || !component[edge.To] || !localEdgePred(state, edge.From, edge.To, threshold) {
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

func verifyBlockForest(state *EvidenceState, active, included map[types.TxID]bool, records []types.BlockRecord, threshold int) error {
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
		if !ok || record.Depth != parent.Depth+1 || !localEdgePred(state, record.Parent, record.Vertex, threshold) {
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
