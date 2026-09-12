package ofo

import (
	"SpeedFair_simplify/pkg/diagnostics"
	"SpeedFair_simplify/pkg/network"
	"SpeedFair_simplify/pkg/types"
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"
)

const (
	localOrderChanSize = 512
	batchReadyChanSize = 256
)

type CandidateHandler func(fragment *types.VerifiableFairOrderFragment, digest [32]byte)

type pendingCandidate struct {
	digest    [32]byte
	preState  *EvidenceState
	postState *EvidenceState
	manager   *DependencyManager
	fragment  *types.VerifiableFairOrderFragment
	done      chan struct{}
}

type OFOService struct {
	Benchmark   *diagnostics.Mechanism
	ReplicaID   uint64
	isLeader    bool
	isMalicious bool
	network     network.NetworkInterface
	auth        types.Authenticator
	admission   types.TransactionAdmission
	authContext types.AuthContext
	onCandidate CandidateHandler

	rwMu                    sync.RWMutex
	committed               *EvidenceState
	pending                 *pendingCandidate
	latestCommittedFragment [32]byte

	// Only the leader persists the active graph materialization.
	UtigManager *DependencyManager

	receiptQueue          []types.TxID
	observed              map[types.TxID]bool
	localOrderPending     *types.LocalOrder
	localOrderFresh       uint64
	localOrderRetransmits uint64
	txSubmissionTimes     map[types.TxID]time.Time

	pipelineCtx        context.Context
	pipelineCancel     context.CancelFunc
	pipelineWg         sync.WaitGroup
	localOrderChan     chan *types.LocalOrder
	batchReadyChan     chan []*types.LocalOrder
	collectorRoundDone chan struct{}

	replicaCount uint64
	fFaulty      uint64
	gamma        float64
	loMaxSize    int
	loInterval   *int

	totalLatency             time.Duration
	finalizedCountForLatency int64
	measuredFinalized        int
	// Set before the first commit; zero leaves measurement unrestricted.
	MeasurementDeadline time.Time
}

func NewOFOService(replicaID, n, f uint64, gamma float64, net network.NetworkInterface, loMaxSize int, loInterval *int, isMalicious bool, auth types.Authenticator, admission types.TransactionAdmission, authContext types.AuthContext, genesis types.ProtocolGenesis, onCandidate CandidateHandler) (*OFOService, error) {
	if n == 0 || replicaID >= n || f >= n || f > (n-1)/3 {
		return nil, fmt.Errorf("AUTIG requires n >= 3f+1 and replica identifiers in [0,n)")
	}
	if math.IsNaN(gamma) || gamma <= 0.5 || gamma > 1 {
		return nil, fmt.Errorf("AUTIG requires gamma in (1/2,1]")
	}
	qH := uint64(math.Ceil(gamma * float64(n-f)))
	if 2*qH < n+2*f+1 {
		return nil, fmt.Errorf("AUTIG parameters violate 2*ceil(gamma*(n-f)) >= n+2f+1")
	}
	if loMaxSize < 0 {
		return nil, fmt.Errorf("local order size limit cannot be negative")
	}
	if auth == nil {
		return nil, fmt.Errorf("AUTIG authentication capability is required")
	}
	if admission == nil {
		return nil, fmt.Errorf("AUTIG transaction admission capability is required")
	}
	if authContext.LeaderID >= n {
		return nil, fmt.Errorf("authorized order leader %d is not a replica", authContext.LeaderID)
	}
	s := &OFOService{
		Benchmark: &diagnostics.Mechanism{},
		ReplicaID: replicaID, isLeader: replicaID == authContext.LeaderID, isMalicious: isMalicious,
		network: net, auth: auth, admission: admission, authContext: authContext, onCandidate: onCandidate,
		committed: NewEvidenceState(authContext.Epoch, n, genesis.StateID), latestCommittedFragment: genesis.FragmentDigest,
		observed:          make(map[types.TxID]bool),
		txSubmissionTimes: make(map[types.TxID]time.Time), replicaCount: n, fFaulty: f,
		gamma: gamma, loMaxSize: loMaxSize, loInterval: loInterval,
	}
	if s.isLeader {
		if onCandidate == nil {
			return nil, fmt.Errorf("order leader requires a hosting candidate handler")
		}
		s.UtigManager = NewDependencyManager(n, f, gamma)
		s.pipelineCtx, s.pipelineCancel = context.WithCancel(context.Background())
		s.localOrderChan = make(chan *types.LocalOrder, localOrderChanSize)
		s.batchReadyChan = make(chan []*types.LocalOrder, batchReadyChanSize)
		s.collectorRoundDone = make(chan struct{})
		s.pipelineWg.Add(2)
		go s.runCollectorStage()
		go s.runProposerStage()
	}
	return s, nil
}

func (s *OFOService) Stop() {
	if s.isLeader {
		s.pipelineCancel()
		s.pipelineWg.Wait()
	}
}

func (s *OFOService) HandleMessage(msg network.Message) {
	switch payload := msg.Payload.(type) {
	case *types.LocalOrder:
		if s.isLeader && msg.From == payload.ReplicaID {
			select {
			case s.localOrderChan <- cloneLocalOrder(payload):
			case <-s.pipelineCtx.Done():
			}
		}
	case *types.VerifiableFairOrderFragment:
		if !s.isLeader {
			if !s.verifyCandidate(payload, msg.From) {
				fmt.Printf("[Replica %d] Verification FAILED for fragment.\n", s.ReplicaID)
			}
		}
	case *types.Transaction:
		if types.TransactionID(payload.CanonicalBytes) != payload.ID {
			return
		}
		if err := s.admission.ValidateAdmission(payload.CanonicalBytes); err != nil {
			return
		}
		if err := s.admission.Store(*payload); err != nil {
			return
		}
		s.rwMu.Lock()
		if !s.observed[payload.ID] {
			s.observed[payload.ID] = true
			s.receiptQueue = append(s.receiptQueue, payload.ID)
			s.Benchmark.Observe("receipt_queue", uint64(len(s.receiptQueue)))
		}
		if _, exists := s.txSubmissionTimes[payload.ID]; !exists {
			s.txSubmissionTimes[payload.ID] = payload.SubmissionTime
		}
		s.rwMu.Unlock()
	}
}

func cloneLocalOrder(order *types.LocalOrder) *types.LocalOrder {
	copyOrder := *order
	copyOrder.OrderedTxs = append([]types.TxID(nil), order.OrderedTxs...)
	copyOrder.Signature = append([]byte(nil), order.Signature...)
	return &copyOrder
}

func (s *OFOService) GenerateAndSendLocalOrder() {
	s.rwMu.Lock()
	if s.localOrderPending != nil {
		s.localOrderRetransmits++
		s.Benchmark.Count("lo_retransmit_attempts", 1)
		order := cloneLocalOrder(s.localOrderPending)
		if order.FragmentSeq != s.committed.FragmentSeq+1 || order.Epoch != s.authContext.Epoch {
			order.FragmentSeq = s.committed.FragmentSeq + 1
			order.Epoch = s.authContext.Epoch
			signature, err := s.auth.SignReplica(s.ReplicaID, types.LocalOrderDigest(order))
			if err != nil {
				s.rwMu.Unlock()
				log.Printf("Replica %d could not re-sign LocalOrder: %v", s.ReplicaID, err)
				return
			}
			order.Signature = signature
			s.Benchmark.Count("lo_signatures", 1)
			s.localOrderPending = cloneLocalOrder(order)
		}
		s.rwMu.Unlock()
		s.sendLocalOrder(order)
		return
	}
	sampleSize := s.loMaxSize
	if len(s.receiptQueue) < sampleSize {
		sampleSize = len(s.receiptQueue)
	}
	ids := append([]types.TxID(nil), s.receiptQueue[:sampleSize]...)
	if s.isMalicious {
		for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
			ids[i], ids[j] = ids[j], ids[i]
		}
	}
	order := &types.LocalOrder{
		ReplicaID: s.ReplicaID, Epoch: s.authContext.Epoch, FragmentSeq: s.committed.FragmentSeq + 1,
		PrevPosition: s.committed.Seq[s.ReplicaID], PrevHead: s.committed.Head[s.ReplicaID], OrderedTxs: ids,
	}
	order.NewPosition = order.PrevPosition + uint64(len(ids))
	order.NewHead = types.LocalOrderHead(order)
	signature, err := s.auth.SignReplica(s.ReplicaID, types.LocalOrderDigest(order))
	if err != nil {
		s.rwMu.Unlock()
		log.Printf("Replica %d could not sign LocalOrder: %v", s.ReplicaID, err)
		return
	}
	order.Signature = signature
	s.Benchmark.Count("lo_signatures", 1)
	s.Benchmark.Count("lo_fresh", 1)
	s.Benchmark.Observe("lo_fresh_ids", uint64(len(ids)))
	s.localOrderPending = cloneLocalOrder(order)
	s.localOrderFresh++
	s.rwMu.Unlock()
	s.sendLocalOrder(order)
}

func (s *OFOService) sendLocalOrder(order *types.LocalOrder) {
	if !s.network.Send(network.Message{Type: "LocalOrder", From: s.ReplicaID, To: s.authContext.LeaderID, Payload: order}) {
		log.Printf("BENCHMARK LOCAL SEND FAILURE: LocalOrder from replica %d", s.ReplicaID)
	}
}

func (s *OFOService) runCollectorStage() {
	defer s.pipelineWg.Done()
	defer close(s.batchReadyChan)
	requiredOrders := s.replicaCount - s.fFaulty
	var pending []*types.LocalOrder
	var collection *diagnostics.Span
	defer func() { collection.Finish() }()
	receivedFrom := make(map[uint64]bool)
	timeout := 200 * time.Millisecond
	if s.loInterval != nil {
		timeout = 2 * time.Duration(*s.loInterval) * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	sendBatch := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		collection.Set("evidence_senders", len(batch))
		collection.Set("complete", true)
		collection.Finish()
		collection = nil
		pending = nil
		receivedFrom = make(map[uint64]bool)
		select {
		case s.batchReadyChan <- batch:
		case <-s.pipelineCtx.Done():
			return
		}
		for {
			select {
			case <-s.collectorRoundDone:
				return
			case <-s.localOrderChan:
				// Replicas retransmit pending chunks. Drain while awaiting commit
				// so their messages cannot block verification acknowledgements.
			case <-s.pipelineCtx.Done():
				return
			}
		}
	}
	for {
		select {
		case order := <-s.localOrderChan:
			s.rwMu.RLock()
			expectedFragmentSeq := s.committed.FragmentSeq + 1
			s.rwMu.RUnlock()
			if order.FragmentSeq != expectedFragmentSeq {
				continue
			}
			if order.ReplicaID >= s.replicaCount || (order.ReplicaID+s.replicaCount-expectedFragmentSeq%s.replicaCount)%s.replicaCount >= requiredOrders {
				continue
			}
			if len(pending) == 0 {
				collection = diagnostics.Start("collection_since_first_order", s.ReplicaID, expectedFragmentSeq)
				timer.Reset(timeout)
			}
			if !receivedFrom[order.ReplicaID] {
				pending = append(pending, order)
				receivedFrom[order.ReplicaID] = true
			}
			if uint64(len(pending)) >= requiredOrders {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				sendBatch()
			}
		case <-timer.C:
			// Without a committed handoff protocol, timeout cannot authorize a
			// partial evidence batch. Keep collecting the current round.
		case <-s.pipelineCtx.Done():
			return
		}
	}
}

func (s *OFOService) runProposerStage() {
	defer s.pipelineWg.Done()
	for {
		select {
		case <-s.pipelineCtx.Done():
			return
		case orders, ok := <-s.batchReadyChan:
			if !ok {
				return
			}
			candidate, err := s.constructCandidate(orders)
			if err != nil {
				log.Printf("Order leader rejected evidence batch: %v", err)
				select {
				case s.collectorRoundDone <- struct{}{}:
				case <-s.pipelineCtx.Done():
					return
				}
				continue
			}
			s.onCandidate(candidate.fragment, candidate.digest)
			select {
			case <-candidate.done:
			case <-s.pipelineCtx.Done():
				return
			}
			select {
			case s.collectorRoundDone <- struct{}{}:
			case <-s.pipelineCtx.Done():
				return
			}
		}
	}
}

func (s *OFOService) constructCandidate(orders []*types.LocalOrder) (*pendingCandidate, error) {
	constructStart := time.Now()
	defer func() { s.Benchmark.Observe("construct_wall_ns", uint64(time.Since(constructStart))) }()
	span := diagnostics.Start("construct", s.ReplicaID, 0)
	defer span.Finish()
	s.rwMu.Lock()
	defer s.rwMu.Unlock()
	span.Mark("lock_acquired")
	span.Set("fragment_seq", s.committed.FragmentSeq+1)
	if s.pending != nil {
		return nil, fmt.Errorf("a candidate is already pending")
	}
	orders = append([]*types.LocalOrder(nil), orders...)
	sort.Slice(orders, func(i, j int) bool { return orders[i].ReplicaID < orders[j].ReplicaID })
	preState := s.committed.clone()
	span.Mark("state_cloned")
	manager := s.UtigManager.clone()
	span.Mark("manager_cloned")
	fragmentSeq := s.committed.FragmentSeq + 1
	touchedNodes, touchedPairs, err := applyEvidence(preState, orders, s.authContext.Epoch, fragmentSeq, s.replicaCount, s.fFaulty, s.gamma, s.loMaxSize, s.auth, s.admission)
	if err != nil {
		return nil, err
	}
	span.Mark("evidence_applied")
	manager.refresh(preState, touchedNodes, touchedPairs)
	span.Mark("graph_refreshed")
	finalOrder, certificate, err := manager.buildProposal(preState)
	if err != nil {
		return nil, err
	}
	span.Mark("proposal_built")
	postState := preState.clone()
	span.Mark("post_cloned")
	// manager already belongs exclusively to this candidate. No pre-finalize
	// graph is retained, so finalize it in place; committed state stays isolated.
	finalize(postState, manager, finalOrder, certificate.Part)
	span.Mark("finalized")
	fragment := &types.VerifiableFairOrderFragment{
		Epoch: s.authContext.Epoch, AuthContextID: s.authContext.Identifier, LeaderID: s.authContext.LeaderID,
		FragmentSeq: fragmentSeq, PreviousStateID: s.committed.StateID,
		PreviousFragmentDigest: s.latestCommittedFragment, Evidence: cloneOrders(orders),
		FinalOrder: finalOrder, Certificate: certificate,
		CandidateEvidenceStateID: preState.StateID, CandidatePostStateID: postState.StateID,
	}
	digest := types.FragmentDigest(fragment)
	signature, err := s.auth.SignLeader(s.authContext, digest)
	if err != nil {
		return nil, fmt.Errorf("sign fragment: %w", err)
	}
	fragment.LeaderSignature = signature
	span.Mark("signed")
	s.recordStageState(span, preState)
	if span != nil {
		occurrences, output, trees := 0, 0, 0
		for _, order := range orders {
			occurrences += len(order.OrderedTxs)
		}
		for _, batch := range finalOrder.Batches {
			output += len(batch.Transactions)
		}
		for _, claim := range certificate.Part {
			trees += len(claim.InTree) + len(claim.OutTree)
		}
		span.Set("evidence_senders", len(orders))
		span.Set("evidence_id_occurrences", occurrences)
		span.Set("output_transactions", output)
		span.Set("certificate_tree_edges", trees)
		span.Set("certificate_sccs", len(certificate.Part))
		span.Set("certificate_block_records", len(certificate.BlockForest))
		span.Set("accepted", true)
	}
	candidate := &pendingCandidate{digest: digest, preState: preState, postState: postState, manager: manager, fragment: fragment, done: make(chan struct{})}
	s.pending = candidate
	s.Benchmark.Count("construct_success", 1)
	return candidate, nil
}

func cloneOrders(orders []*types.LocalOrder) []*types.LocalOrder {
	cloned := make([]*types.LocalOrder, len(orders))
	for index, order := range orders {
		cloned[index] = cloneLocalOrder(order)
	}
	return cloned
}

func (s *OFOService) verifyCandidate(fragment *types.VerifiableFairOrderFragment, sender uint64) bool {
	span := diagnostics.Start("verify", s.ReplicaID, fragment.FragmentSeq)
	defer span.Finish()
	s.rwMu.Lock()
	defer s.rwMu.Unlock()
	span.Mark("lock_acquired")
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
	preState := s.committed.clone()
	span.Mark("state_cloned")
	if _, _, err := applyEvidenceWithTouches(preState, fragment.Evidence, fragment.Epoch, fragment.FragmentSeq, s.replicaCount, s.fFaulty, s.gamma, s.loMaxSize, s.auth, s.admission, false); err != nil {
		return false
	}
	span.Mark("evidence_applied")
	if preState.StateID != fragment.CandidateEvidenceStateID {
		return false
	}
	if err := verifyStructuralCertificate(preState, fragment.FinalOrder, fragment.Certificate, s.replicaCount, s.fFaulty, s.gamma); err != nil {
		log.Printf("Replica %d rejected structural certificate: %v", s.ReplicaID, err)
		return false
	}
	span.Mark("certificate_verified")
	postState := preState.clone()
	span.Mark("post_cloned")
	finalize(postState, NewDependencyManager(s.replicaCount, s.fFaulty, s.gamma), fragment.FinalOrder, fragment.Certificate.Part)
	span.Mark("finalized")
	if postState.StateID != fragment.CandidatePostStateID {
		return false
	}
	s.pending = &pendingCandidate{digest: digest, preState: preState, postState: postState, fragment: fragment, done: make(chan struct{})}
	s.recordStageState(span, preState)
	span.Set("accepted", true)
	return true
}

// Called with rwMu held. Counts are diagnostic only; no per-pair logging.
func (s *OFOService) recordStageState(span *diagnostics.Span, state *EvidenceState) {
	if span == nil {
		return
	}
	total, maxPos, active := 0, 0, 0
	for _, positions := range state.Pos {
		total += len(positions)
		if len(positions) > maxPos {
			maxPos = len(positions)
		}
	}
	for _, status := range state.states {
		if status != types.StateBlank {
			active++
		}
	}
	span.Set("live", len(state.Live))
	span.Set("active_nodes", active)
	span.Set("weights", len(state.weights))
	span.Set("positions_mean", float64(total)/float64(s.replicaCount))
	span.Set("positions_max", maxPos)
	span.Set("receipt_queue", len(s.receiptQueue))
	span.Set("local_orders_fresh_total", s.localOrderFresh)
	span.Set("local_orders_retransmitted_total", s.localOrderRetransmits)
}

// CommitPending is the only path that installs authoritative state. The hosting
// layer must call it with the digest of a BFT-committed proposal.
func (s *OFOService) CommitPending(digest [32]byte) ([]types.FairnessBatch, bool) {
	s.rwMu.Lock()
	defer s.rwMu.Unlock()
	if s.pending == nil || s.pending.digest != digest {
		return nil, false
	}
	candidate := s.pending
	s.committed = candidate.postState
	s.latestCommittedFragment = digest
	if s.isLeader {
		s.UtigManager = candidate.manager
	}
	s.applyCommittedLocalOrder(candidate.fragment.Evidence)
	finalizeTime := time.Now()
	s.Benchmark.Count("committed_fragments", 1)
	outputTransactions := uint64(0)
	measured := s.MeasurementDeadline.IsZero() || finalizeTime.Before(s.MeasurementDeadline)
	for _, batch := range candidate.fragment.FinalOrder.Batches {
		for _, id := range batch.Transactions {
			outputTransactions++
			if measured {
				s.measuredFinalized++
			}
			if submitted, ok := s.txSubmissionTimes[id]; ok {
				latency := finalizeTime.Sub(submitted)
				if latency < 0 {
					latency = 0
				}
				if measured {
					s.totalLatency += latency
					s.finalizedCountForLatency++
				}
				delete(s.txSubmissionTimes, id)
			}
		}
	}
	batches := append([]types.FairnessBatch(nil), candidate.fragment.FinalOrder.Batches...)
	s.Benchmark.Observe("fragment_output_transactions", outputTransactions)
	s.pending = nil
	close(candidate.done)
	return batches, true
}

func (s *OFOService) AbandonPending(digest [32]byte) bool {
	s.rwMu.Lock()
	defer s.rwMu.Unlock()
	if s.pending == nil || s.pending.digest != digest {
		return false
	}
	candidate := s.pending
	s.pending = nil
	close(candidate.done)
	return true
}

func (s *OFOService) applyCommittedLocalOrder(orders []*types.LocalOrder) {
	for _, order := range orders {
		if order.ReplicaID != s.ReplicaID {
			continue
		}
		committed := make(map[types.TxID]bool, len(order.OrderedTxs))
		for _, id := range order.OrderedTxs {
			committed[id] = true
		}
		remaining := s.receiptQueue[:0]
		for _, id := range s.receiptQueue {
			if !committed[id] {
				remaining = append(remaining, id)
			}
		}
		s.receiptQueue = remaining
		s.Benchmark.Observe("receipt_queue", uint64(len(s.receiptQueue)))
		if s.localOrderPending != nil && types.LocalOrderDigest(s.localOrderPending) == types.LocalOrderDigest(order) {
			s.localOrderPending = nil
		}
	}
	// An omitted extension retains its exact chunk and is re-signed on next send.
}

func (s *OFOService) GetMeasurementStats() (int, time.Duration, int64) {
	s.rwMu.RLock()
	defer s.rwMu.RUnlock()
	var average time.Duration
	if s.finalizedCountForLatency > 0 {
		average = s.totalLatency / time.Duration(s.finalizedCountForLatency)
	}
	return s.measuredFinalized, average, s.finalizedCountForLatency
}

func (s *OFOService) CommittedContext() (uint64, [32]byte, [32]byte) {
	s.rwMu.RLock()
	defer s.rwMu.RUnlock()
	return s.committed.FragmentSeq, s.committed.StateID, s.latestCommittedFragment
}

func (s *OFOService) GetFinalizedCount() int {
	s.rwMu.RLock()
	defer s.rwMu.RUnlock()
	return len(s.committed.Done)
}

func (s *OFOService) GetUTIGNodeCount() int {
	if !s.isLeader {
		return -1
	}
	s.rwMu.RLock()
	defer s.rwMu.RUnlock()
	return s.UtigManager.GetUTIGNodesCount()
}

func (s *OFOService) GetLatencyStats() (time.Duration, int64) {
	s.rwMu.RLock()
	defer s.rwMu.RUnlock()
	if s.finalizedCountForLatency == 0 {
		return 0, 0
	}
	return s.totalLatency / time.Duration(s.finalizedCountForLatency), s.finalizedCountForLatency
}

func (s *OFOService) CommittedStateID() [32]byte {
	s.rwMu.RLock()
	defer s.rwMu.RUnlock()
	return s.committed.StateID
}

func (s *OFOService) PendingDigest() ([32]byte, bool) {
	s.rwMu.RLock()
	defer s.rwMu.RUnlock()
	if s.pending == nil {
		return [32]byte{}, false
	}
	return s.pending.digest, true
}
