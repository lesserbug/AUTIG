package ofo

import (
	"SpeedFair_simplify/pkg/network"
	"SpeedFair_simplify/pkg/types"
	"context"
	"sync"
	"testing"
	"time"
)

type testNetwork struct {
	mu       sync.RWMutex
	handlers map[uint64]func(network.Message)
}

func newTestNetwork() *testNetwork {
	return &testNetwork{handlers: make(map[uint64]func(network.Message))}
}
func (n *testNetwork) Register(id uint64, handler func(network.Message)) {
	n.mu.Lock()
	n.handlers[id] = handler
	n.mu.Unlock()
}
func (n *testNetwork) Send(message network.Message) bool {
	n.mu.RLock()
	handler := n.handlers[message.To]
	n.mu.RUnlock()
	if handler != nil {
		handler(message)
		return true
	}
	return false
}
func (n *testNetwork) Stop()                            {}
func (n *testNetwork) WaitForPeers(time.Duration) error { return nil }

type blockingAdmission struct {
	*testAdmission
	blockedID    types.TxID
	secondID     types.TxID
	blockedStart chan struct{}
	release      chan struct{}
	secondStored chan struct{}
	blockedOnce  sync.Once
	secondOnce   sync.Once
}

func (admission *blockingAdmission) Store(transaction types.Transaction) error {
	if transaction.ID == admission.blockedID {
		admission.blockedOnce.Do(func() { close(admission.blockedStart) })
		<-admission.release
	}
	if err := admission.testAdmission.Store(transaction); err != nil {
		return err
	}
	if transaction.ID == admission.secondID {
		admission.secondOnce.Do(func() { close(admission.secondStored) })
	}
	return nil
}

func TestCandidateIsNotInstalledBeforeHostingCommit(t *testing.T) {
	const n, f = uint64(2), uint64(0)
	auth := newTestAuthenticator(t, n)
	admission := newTestAdmission()
	authContext := types.AuthContext{Epoch: 7, LeaderID: 0, Identifier: [32]byte{1}}
	genesis := types.ProtocolGenesis{StateID: [32]byte{2}, FragmentDigest: [32]byte{3}}
	testNet := newTestNetwork()
	candidates := make(chan *types.VerifiableFairOrderFragment, 1)
	digests := make(chan [32]byte, 1)
	leader, err := NewOFOService(0, n, f, 1, testNet, 10, nil, false, auth, admission, authContext, genesis, func(fragment *types.VerifiableFairOrderFragment, digest [32]byte) {
		candidates <- fragment
		digests <- digest
	})
	if err != nil {
		t.Fatal(err)
	}
	defer leader.Stop()
	follower, err := NewOFOService(1, n, f, 1, testNet, 10, nil, false, auth, admission, authContext, genesis, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer follower.Stop()
	testNet.Register(0, leader.HandleMessage)
	testNet.Register(1, follower.HandleMessage)

	canonical := []byte{9}
	tx := &types.Transaction{ID: types.TransactionID(canonical), CanonicalBytes: canonical, SubmissionTime: time.Now()}
	testNet.Send(network.Message{Type: "Transaction", From: 0, To: 0, Payload: tx})
	testNet.Send(network.Message{Type: "Transaction", From: 0, To: 1, Payload: tx})
	leader.GenerateAndSendLocalOrder()
	follower.GenerateAndSendLocalOrder()

	var fragment *types.VerifiableFairOrderFragment
	var digest [32]byte
	select {
	case fragment = <-candidates:
		digest = <-digests
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not construct a candidate")
	}
	if leader.GetFinalizedCount() != 0 {
		t.Fatal("leader installed candidate before hosting commit")
	}
	testNet.Send(network.Message{Type: "AUTIGCandidate", From: 0, To: 1, Payload: fragment})
	if follower.GetFinalizedCount() != 0 {
		t.Fatal("follower installed verified candidate before hosting commit")
	}
	if _, pending := follower.PendingDigest(); !pending {
		t.Fatal("follower did not retain verified candidate")
	}
	follower.HandleMessage(network.Message{Type: "AUTIGCommit", From: 0, To: 1, Payload: &struct{ Digest [32]byte }{Digest: digest}})
	if follower.GetFinalizedCount() != 0 {
		t.Fatal("network message installed a candidate without a hosting commit callback")
	}
	if _, ok := leader.CommitPending(digest); !ok {
		t.Fatal("leader rejected matching hosting commit")
	}
	if _, ok := follower.CommitPending(digest); !ok {
		t.Fatal("follower rejected matching hosting commit")
	}
	if leader.GetFinalizedCount() != 1 || follower.GetFinalizedCount() != 1 {
		t.Fatal("matching hosting commit did not install candidate")
	}
}

func TestServiceRejectsUnsafeProtocolParameters(t *testing.T) {
	auth := newTestAuthenticator(t, 5)
	admission := newTestAdmission()
	testNet := newTestNetwork()
	authContext := types.AuthContext{Epoch: 1, LeaderID: 0}
	genesis := types.ProtocolGenesis{StateID: [32]byte{1}, FragmentDigest: [32]byte{2}}
	tests := []struct {
		name  string
		n, f  uint64
		gamma float64
	}{
		{name: "n below three-f-plus-one", n: 3, f: 1, gamma: 1},
		{name: "f not below n", n: 1, f: 1, gamma: 1},
		{name: "gamma at one half", n: 5, f: 1, gamma: 0.5},
		{name: "insufficient honest-quorum overlap", n: 4, f: 1, gamma: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewOFOService(1, test.n, test.f, test.gamma, testNet, 10, nil, false, auth, admission, authContext, genesis, nil); err == nil {
				t.Fatal("unsafe protocol parameters were accepted")
			}
		})
	}
	service, err := NewOFOService(1, 5, 1, 1, testNet, 10, nil, false, auth, admission, authContext, genesis, nil)
	if err != nil {
		t.Fatalf("valid protocol parameters were rejected: %v", err)
	}
	service.Stop()
}

func TestFirstReceiptOrderSurvivesBlockedAdmission(t *testing.T) {
	canonicalX, canonicalY := []byte("x"), []byte("y")
	txX := types.Transaction{ID: types.TransactionID(canonicalX), CanonicalBytes: canonicalX}
	txY := types.Transaction{ID: types.TransactionID(canonicalY), CanonicalBytes: canonicalY}
	admission := &blockingAdmission{
		testAdmission: newTestAdmission(),
		blockedID:     txX.ID,
		secondID:      txY.ID,
		blockedStart:  make(chan struct{}),
		release:       make(chan struct{}),
		secondStored:  make(chan struct{}),
	}
	released := false
	defer func() {
		if !released {
			close(admission.release)
		}
	}()

	distributed, err := network.NewDistributedNetwork(network.NetworkConfig{
		ReplicaID:   0,
		ReplicaAddr: map[uint64]string{0: "127.0.0.1:0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(distributed.Stop)
	auth := newTestAuthenticator(t, 1)
	service, err := NewOFOService(
		0, 1, 0, 1, distributed, 10, nil, false, auth, admission,
		types.AuthContext{Epoch: 1, LeaderID: 0},
		types.ProtocolGenesis{StateID: [32]byte{1}, FragmentDigest: [32]byte{2}},
		func(*types.VerifiableFairOrderFragment, [32]byte) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Stop)
	distributed.Register(0, service.HandleMessage)

	distributed.Send(network.Message{Type: "Transaction", From: 0, To: 0, Payload: &txX})
	select {
	case <-admission.blockedStart:
	case <-time.After(time.Second):
		t.Fatal("first transaction did not enter admission")
	}
	distributed.Send(network.Message{Type: "Transaction", From: 0, To: 0, Payload: &txY})
	select {
	case <-admission.secondStored:
		t.Fatal("second transaction overtook the blocked first receipt")
	case <-time.After(100 * time.Millisecond):
	}

	close(admission.release)
	released = true
	select {
	case <-admission.secondStored:
	case <-time.After(time.Second):
		t.Fatal("second transaction was not processed after releasing the first")
	}

	deadline := time.Now().Add(time.Second)
	for {
		service.rwMu.RLock()
		receipts := append([]types.TxID(nil), service.receiptQueue...)
		service.rwMu.RUnlock()
		if len(receipts) == 2 {
			if receipts[0] != txX.ID || receipts[1] != txY.ID {
				t.Fatalf("first-receipt order changed: got %x then %x", receipts[0], receipts[1])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipt log contains %d transactions, want 2", len(receipts))
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCollectorWaitsForRoundOutcomeAndDropsCommittedRetransmissions(t *testing.T) {
	pipelineCtx, pipelineCancel := context.WithCancel(context.Background())
	service := &OFOService{
		committed:          NewEvidenceState(1, 4, [32]byte{}),
		pipelineCtx:        pipelineCtx,
		pipelineCancel:     pipelineCancel,
		localOrderChan:     make(chan *types.LocalOrder, 16),
		batchReadyChan:     make(chan []*types.LocalOrder, 2),
		collectorRoundDone: make(chan struct{}),
		replicaCount:       4,
		fFaulty:            1,
	}
	service.pipelineWg.Add(1)
	go service.runCollectorStage()
	defer func() {
		pipelineCancel()
		service.pipelineWg.Wait()
	}()

	for sender := uint64(0); sender < 3; sender++ {
		service.localOrderChan <- &types.LocalOrder{ReplicaID: sender, FragmentSeq: 1}
	}
	select {
	case batch := <-service.batchReadyChan:
		if len(batch) != 3 {
			t.Fatalf("collector emitted %d extensions, want 3", len(batch))
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not emit the first batch")
	}

	for sender := uint64(0); sender < 3; sender++ {
		service.localOrderChan <- &types.LocalOrder{ReplicaID: sender, FragmentSeq: 1}
	}
	select {
	case batch := <-service.batchReadyChan:
		t.Fatalf("collector emitted another batch before round completion: %+v", batch)
	default:
	}

	service.rwMu.Lock()
	service.committed.FragmentSeq = 1
	service.rwMu.Unlock()
	select {
	case service.collectorRoundDone <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("collector was not waiting for the round outcome")
	}

	for sender := uint64(0); sender < 3; sender++ {
		service.localOrderChan <- &types.LocalOrder{ReplicaID: sender, FragmentSeq: 2}
	}
	select {
	case batch := <-service.batchReadyChan:
		if len(batch) != 3 {
			t.Fatalf("collector emitted %d extensions, want 3", len(batch))
		}
		for _, order := range batch {
			if order.FragmentSeq != 2 {
				t.Fatalf("collector carried fragment %d retransmission into fragment 2", order.FragmentSeq)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not emit the next committed-sequence batch")
	}
}
