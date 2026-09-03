package main

import (
	"SpeedFair_simplify/pkg/network"
	"SpeedFair_simplify/pkg/ofo"
	"SpeedFair_simplify/pkg/types"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"sync"
)

// fileAuthenticator is the standalone demo's composition adapter. OFOService
// receives only the Authenticator capability and never reads or creates keys.
type fileAuthenticator struct {
	replicaPublic  map[uint64]ed25519.PublicKey
	replicaPrivate map[uint64]ed25519.PrivateKey
	leaderPublic   ed25519.PublicKey
	leaderPrivate  ed25519.PrivateKey
}

func loadFileAuthenticator(config Config, localReplicas []uint64) (*fileAuthenticator, types.AuthContext, error) {
	auth := &fileAuthenticator{
		replicaPublic:  make(map[uint64]ed25519.PublicKey, len(config.Nodes)),
		replicaPrivate: make(map[uint64]ed25519.PrivateKey, len(localReplicas)),
	}
	for replica := range config.Nodes {
		replicaID, err := strconv.ParseUint(replica, 10, 64)
		if err != nil {
			return nil, types.AuthContext{}, fmt.Errorf("invalid replica ID %q", replica)
		}
		path := config.ReplicaPublicKeys[replica]
		if path == "" {
			return nil, types.AuthContext{}, fmt.Errorf("missing replica public key path for %d", replicaID)
		}
		key, err := readEd25519PublicKey(path)
		if err != nil {
			return nil, types.AuthContext{}, fmt.Errorf("replica %d public key: %w", replicaID, err)
		}
		auth.replicaPublic[replicaID] = key
	}
	for _, replicaID := range localReplicas {
		path := config.ReplicaPrivateKeys[strconv.FormatUint(replicaID, 10)]
		if path == "" {
			return nil, types.AuthContext{}, fmt.Errorf("missing local replica private key path for %d", replicaID)
		}
		key, err := readEd25519PrivateKey(path)
		if err != nil {
			return nil, types.AuthContext{}, fmt.Errorf("replica %d private key: %w", replicaID, err)
		}
		auth.replicaPrivate[replicaID] = key
	}
	if config.LeaderPublicKey == "" {
		return nil, types.AuthContext{}, fmt.Errorf("missing authorized leader public key path")
	}
	leaderPublic, err := readEd25519PublicKey(config.LeaderPublicKey)
	if err != nil {
		return nil, types.AuthContext{}, fmt.Errorf("leader public key: %w", err)
	}
	auth.leaderPublic = leaderPublic
	for _, replicaID := range localReplicas {
		if replicaID != config.LeaderID {
			continue
		}
		if config.LeaderPrivateKey == "" {
			return nil, types.AuthContext{}, fmt.Errorf("missing local leader private key path")
		}
		auth.leaderPrivate, err = readEd25519PrivateKey(config.LeaderPrivateKey)
		if err != nil {
			return nil, types.AuthContext{}, fmt.Errorf("leader private key: %w", err)
		}
	}
	authContext := types.AuthContext{Epoch: config.Epoch, LeaderID: config.LeaderID}
	authContext.Identifier = demoAuthContextID(authContext, auth.replicaPublic, auth.leaderPublic)
	return auth, authContext, nil
}

func (auth *fileAuthenticator) SignReplica(replicaID uint64, digest [32]byte) ([]byte, error) {
	key := auth.replicaPrivate[replicaID]
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("no signing key for replica %d", replicaID)
	}
	return ed25519.Sign(key, digest[:]), nil
}

func (auth *fileAuthenticator) VerifyReplica(replicaID uint64, digest [32]byte, signature []byte) bool {
	key := auth.replicaPublic[replicaID]
	return len(key) == ed25519.PublicKeySize && ed25519.Verify(key, digest[:], signature)
}

func (auth *fileAuthenticator) SignLeader(_ types.AuthContext, digest [32]byte) ([]byte, error) {
	if len(auth.leaderPrivate) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("no authorized leader signing key")
	}
	return ed25519.Sign(auth.leaderPrivate, digest[:]), nil
}

func (auth *fileAuthenticator) VerifyLeader(_ types.AuthContext, _ uint64, digest [32]byte, signature []byte) bool {
	return len(auth.leaderPublic) == ed25519.PublicKeySize && ed25519.Verify(auth.leaderPublic, digest[:], signature)
}

func readEd25519PublicKey(path string) (ed25519.PublicKey, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(encoded)
	if block == nil {
		return nil, fmt.Errorf("not PEM encoded")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 public key")
	}
	return key, nil
}

func readEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(encoded)
	if block == nil {
		return nil, fmt.Errorf("not PEM encoded")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an Ed25519 private key")
	}
	return key, nil
}

func demoAuthContextID(context types.AuthContext, replicaKeys map[uint64]ed25519.PublicKey, leaderKey ed25519.PublicKey) [32]byte {
	var b bytes.Buffer
	b.WriteString("AUTIG/demo-auth-context/v1")
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], context.Epoch)
	b.Write(encoded[:])
	binary.BigEndian.PutUint64(encoded[:], context.LeaderID)
	b.Write(encoded[:])
	replicas := make([]uint64, 0, len(replicaKeys))
	for replicaID := range replicaKeys {
		replicas = append(replicas, replicaID)
	}
	sort.Slice(replicas, func(i, j int) bool { return replicas[i] < replicas[j] })
	for _, replicaID := range replicas {
		binary.BigEndian.PutUint64(encoded[:], replicaID)
		b.Write(encoded[:])
		b.Write(replicaKeys[replicaID])
	}
	b.Write(leaderKey)
	return sha256.Sum256(b.Bytes())
}

func loadProtocolGenesis(config Config) (types.ProtocolGenesis, error) {
	decode := func(name, value string) ([32]byte, error) {
		var digest [32]byte
		encoded, err := hex.DecodeString(value)
		if err != nil || len(encoded) != len(digest) {
			return digest, fmt.Errorf("%s must be a 32-byte hexadecimal value", name)
		}
		copy(digest[:], encoded)
		return digest, nil
	}
	stateID, err := decode("genesis_state_id", config.GenesisStateID)
	if err != nil {
		return types.ProtocolGenesis{}, err
	}
	fragmentDigest, err := decode("genesis_fragment_digest", config.GenesisFragmentDigest)
	if err != nil {
		return types.ProtocolGenesis{}, err
	}
	return types.ProtocolGenesis{StateID: stateID, FragmentDigest: fragmentDigest}, nil
}

// memoryTransactionAdmission is the standalone benchmark's content store. The
// core receives only the TransactionAdmission capability.
type memoryTransactionAdmission struct {
	mu      sync.RWMutex
	content map[types.TxID][]byte
}

func newMemoryTransactionAdmission() *memoryTransactionAdmission {
	return &memoryTransactionAdmission{content: make(map[types.TxID][]byte)}
}

func (store *memoryTransactionAdmission) Store(transaction types.Transaction) error {
	if types.TransactionID(transaction.CanonicalBytes) != transaction.ID {
		return fmt.Errorf("transaction identifier does not commit to canonical bytes")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.content[transaction.ID]; ok && !bytes.Equal(existing, transaction.CanonicalBytes) {
		return fmt.Errorf("transaction identifier is already bound to different content")
	}
	store.content[transaction.ID] = append([]byte(nil), transaction.CanonicalBytes...)
	return nil
}

func (store *memoryTransactionAdmission) Resolve(id types.TxID) ([]byte, types.TxID, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	content, ok := store.content[id]
	if !ok {
		return nil, types.TxID{}, fmt.Errorf("content is unavailable")
	}
	return append([]byte(nil), content...), id, nil
}

func (*memoryTransactionAdmission) ValidateAdmission([]byte) error { return nil }

// These messages and benchmarkNodeAdapter exist only in the demo composition
// layer. OFOService never accepts them as protocol messages.
type benchmarkReady struct {
	ReplicaID uint64
}

type benchmarkStart struct{}

type benchmarkFragmentCommit struct {
	Digest [32]byte
}

type benchmarkNodeAdapter struct {
	service      *ofo.OFOService
	replicaID    uint64
	replicaCount uint64
	leaderID     uint64
	network      network.NetworkInterface
	start        chan struct{}
	startOnce    sync.Once
	mu           sync.Mutex
	readySenders map[uint64]struct{}
	started      bool
	deferred     *[32]byte
}

func (adapter *benchmarkNodeAdapter) HandleMessage(message network.Message) {
	switch payload := message.Payload.(type) {
	case *benchmarkReady:
		if adapter.replicaID != adapter.leaderID || message.From != payload.ReplicaID || payload.ReplicaID >= adapter.replicaCount {
			return
		}
		adapter.mu.Lock()
		alreadyStarted := adapter.started
		start := false
		if !adapter.started {
			adapter.readySenders[payload.ReplicaID] = struct{}{}
			if uint64(len(adapter.readySenders)) == adapter.replicaCount {
				adapter.started = true
				start = true
			}
		}
		adapter.mu.Unlock()
		if alreadyStarted {
			if payload.ReplicaID != adapter.leaderID {
				adapter.network.Send(network.Message{Type: "BenchmarkStart", From: adapter.leaderID, To: payload.ReplicaID, Payload: &benchmarkStart{}})
			}
			return
		}
		if !start {
			return
		}
		for replicaID := uint64(0); replicaID < adapter.replicaCount; replicaID++ {
			if replicaID == adapter.leaderID {
				continue
			}
			adapter.network.Send(network.Message{Type: "BenchmarkStart", From: adapter.leaderID, To: replicaID, Payload: &benchmarkStart{}})
		}
		// All remote Start messages have been sent before the leader can begin
		// submitting transactions on those same FIFO connections.
		adapter.startOnce.Do(func() { close(adapter.start) })
		return
	case *benchmarkStart:
		if message.From == adapter.leaderID {
			adapter.startOnce.Do(func() { close(adapter.start) })
		}
		return
	}

	commit, isCommit := message.Payload.(*benchmarkFragmentCommit)
	if !isCommit {
		adapter.service.HandleMessage(message)
		if _, isCandidate := message.Payload.(*types.VerifiableFairOrderFragment); isCandidate {
			adapter.applyDeferred()
		}
		return
	}
	if message.From != adapter.leaderID {
		return
	}
	if _, committed := adapter.service.CommitPending(commit.Digest); committed {
		return
	}
	adapter.mu.Lock()
	digest := commit.Digest
	adapter.deferred = &digest
	adapter.mu.Unlock()
}

func (adapter *benchmarkNodeAdapter) applyDeferred() {
	adapter.mu.Lock()
	deferred := adapter.deferred
	adapter.deferred = nil
	adapter.mu.Unlock()
	if deferred != nil {
		_, _ = adapter.service.CommitPending(*deferred)
	}
}

// benchmarkHostingAdapter keeps the benchmark's immediate progress behavior,
// while proposal verification and commit remain distinct OFOService events.
type benchmarkHostingAdapter struct {
	network      network.NetworkInterface
	replicaCount uint64
	leaderID     uint64
	leader       *ofo.OFOService
	ready        chan struct{}
}

func (adapter *benchmarkHostingAdapter) Propose(fragment *types.VerifiableFairOrderFragment, digest [32]byte) {
	<-adapter.ready
	for replicaID := uint64(0); replicaID < adapter.replicaCount; replicaID++ {
		if replicaID == adapter.leaderID {
			continue
		}
		if !adapter.network.Send(network.Message{Type: "AUTIGCandidate", From: adapter.leaderID, To: replicaID, Payload: fragment}) {
			log.Printf("BENCHMARK LOCAL SEND FAILURE: AUTIGCandidate to replica %d", replicaID)
		}
	}
	if _, ok := adapter.leader.CommitPending(digest); !ok {
		return
	}
	for replicaID := uint64(0); replicaID < adapter.replicaCount; replicaID++ {
		if replicaID == adapter.leaderID {
			continue
		}
		if !adapter.network.Send(network.Message{Type: "BenchmarkAUTIGCommit", From: adapter.leaderID, To: replicaID, Payload: &benchmarkFragmentCommit{Digest: digest}}) {
			log.Printf("BENCHMARK LOCAL SEND FAILURE: BenchmarkAUTIGCommit to replica %d", replicaID)
		}
	}
}
