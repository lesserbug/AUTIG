package main

import (
	"SpeedFair_simplify/pkg/network"
	"SpeedFair_simplify/pkg/ofo"
	"SpeedFair_simplify/pkg/types"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

type adapterTestNetwork struct{ send func(network.Message) bool }

func (n *adapterTestNetwork) Send(m network.Message) bool          { return n.send(m) }
func (*adapterTestNetwork) Register(uint64, func(network.Message)) {}
func (*adapterTestNetwork) Stop()                                  {}
func (*adapterTestNetwork) WaitForPeers(time.Duration) error       { return nil }

func TestBenchmarkRequiresVerificationQuorum(t *testing.T) {
	for _, outcome := range []string{"quorum", "rejected", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			auth := &fileAuthenticator{replicaPublic: map[uint64]ed25519.PublicKey{}, replicaPrivate: map[uint64]ed25519.PrivateKey{}}
			for i := uint64(0); i < 5; i++ {
				pub, priv, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				auth.replicaPublic[i] = pub
				auth.replicaPrivate[i] = priv
			}
			auth.leaderPublic, auth.leaderPrivate = auth.replicaPublic[0], auth.replicaPrivate[0]
			sent := make(chan network.Message, 32)
			net := &adapterTestNetwork{send: func(m network.Message) bool { sent <- m; return true }}
			candidates := make(chan *types.VerifiableFairOrderFragment, 1)
			admission := newMemoryTransactionAdmission()
			leader, err := ofo.NewOFOService(0, 5, 1, 1, net, 10, nil, false, auth, admission, types.AuthContext{Epoch: 1, LeaderID: 0}, types.ProtocolGenesis{}, func(f *types.VerifiableFairOrderFragment, _ [32]byte) { candidates <- f })
			if err != nil {
				t.Fatal(err)
			}
			defer leader.Stop()
			b := []byte("transaction")
			id := types.TransactionID(b)
			leader.HandleMessage(network.Message{Payload: &types.Transaction{ID: id, CanonicalBytes: b, SubmissionTime: time.Now()}})
			for i := uint64(1); i < 5; i++ {
				order := &types.LocalOrder{ReplicaID: i, Epoch: 1, FragmentSeq: 1, NewPosition: 1, OrderedTxs: []types.TxID{id}}
				order.NewHead = types.LocalOrderHead(order)
				order.Signature, _ = auth.SignReplica(i, types.LocalOrderDigest(order))
				leader.HandleMessage(network.Message{From: i, Payload: order})
			}
			var fragment *types.VerifiableFairOrderFragment
			select {
			case fragment = <-candidates:
			case <-time.After(time.Second):
				t.Fatal("no candidate")
			}
			digest := types.FragmentDigest(fragment)
			host := &benchmarkHostingAdapter{network: net, replicaCount: 5, faultCount: 1, leaderID: 0, leader: leader, ready: make(chan struct{}), verified: make(chan network.Message, 10)}
			close(host.ready)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); host.Propose(ctx, fragment, digest) }()
			defer func() { cancel(); <-done }()
			for i := 0; i < 4; i++ {
				select {
				case <-sent:
				case <-time.After(time.Second):
					t.Fatal("candidate not broadcast")
				}
			}
			host.verified <- network.Message{From: 1, Payload: &benchmarkVerified{Digest: digest, Accepted: true}}
			host.verified <- network.Message{From: 1, Payload: &benchmarkVerified{Digest: digest, Accepted: true}}
			host.verified <- network.Message{From: 2, Payload: &benchmarkVerified{Digest: [32]byte{99}, Accepted: true}}
			host.verified <- network.Message{From: 2, Payload: &benchmarkVerified{Digest: digest, Accepted: true}}
			select {
			case <-done:
				t.Fatal("duplicate/stale acknowledgements completed quorum")
			case <-time.After(30 * time.Millisecond):
			}
			if leader.GetFinalizedCount() != 0 {
				t.Fatal("committed before quorum")
			}
			if outcome == "cancelled" {
				cancel()
			} else {
				host.verified <- network.Message{From: 3, Payload: &benchmarkVerified{Digest: digest, Accepted: outcome == "quorum"}}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("hosting gate did not finish")
			}
			want := 0
			if outcome == "quorum" {
				want = 1
			}
			if leader.GetFinalizedCount() != want {
				t.Fatalf("finalized=%d want=%d", leader.GetFinalizedCount(), want)
			}
			if _, pending := leader.PendingDigest(); pending {
				t.Fatal("hosting gate left pending candidate")
			}
		})
	}
}
