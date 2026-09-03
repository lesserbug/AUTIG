package types

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sort"
	"time"
)

// TxID is the public transaction identifier and its public ordering key.
type TxID [32]byte

func LessTxID(a, b TxID) bool {
	return bytes.Compare(a[:], b[:]) < 0
}

func SortTxIDs(ids []TxID) {
	sort.Slice(ids, func(i, j int) bool { return LessTxID(ids[i], ids[j]) })
}

type Transaction struct {
	ID             TxID
	CanonicalBytes []byte
	SubmissionTime time.Time
}

type TransactionAdmission interface {
	Store(transaction Transaction) error
	Resolve(id TxID) (canonicalBytes []byte, reference TxID, err error)
	ValidateAdmission(canonicalBytes []byte) error
}

type TxState string

const (
	StateSolid  TxState = "solid"
	StateShaded TxState = "shaded"
	StateBlank  TxState = "blank"
)

// AuthContext identifies the hosting BFT authorization state for an epoch.
// Key material and membership verification remain in the injected Authenticator.
type AuthContext struct {
	Epoch      uint64
	LeaderID   uint64
	Identifier [32]byte
}

type ProtocolGenesis struct {
	StateID        [32]byte
	FragmentDigest [32]byte
}

type Authenticator interface {
	SignReplica(replicaID uint64, digest [32]byte) ([]byte, error)
	VerifyReplica(replicaID uint64, digest [32]byte, signature []byte) bool
	SignLeader(auth AuthContext, digest [32]byte) ([]byte, error)
	VerifyLeader(auth AuthContext, leaderID uint64, digest [32]byte, signature []byte) bool
}

// LocalOrder is a signed, contiguous extension of one replica's first-receipt log.
type LocalOrder struct {
	ReplicaID    uint64
	Epoch        uint64
	FragmentSeq  uint64
	PrevPosition uint64
	NewPosition  uint64
	PrevHead     [32]byte
	OrderedTxs   []TxID
	NewHead      [32]byte
	Signature    []byte
}

type DependencyGraph struct {
	Nodes map[TxID]bool
	Edges map[TxID][]TxID
}

type FairnessBatch struct {
	Index        uint64
	Transactions []TxID
}

type FairOrderFragment struct {
	Batches []FairnessBatch
}

// TreeEdge always names an actual directed EdgePred edge.
type TreeEdge struct {
	From TxID
	To   TxID
}

type SCCClaim struct {
	Transactions []TxID
	InTree       []TreeEdge
	OutTree      []TreeEdge
	Rank         uint64
}

type BlockRecord struct {
	Vertex    TxID
	Parent    TxID
	HasParent bool
	Depth     uint64
}

type StructuralCertificate struct {
	Part        []SCCClaim
	BlockForest []BlockRecord
}

// VerifiableFairOrderFragment is a hosting-BFT proposal. Its signature and
// digest use the canonical encodings below; gob is only the transport encoding.
type VerifiableFairOrderFragment struct {
	Epoch                    uint64
	AuthContextID            [32]byte
	LeaderID                 uint64
	FragmentSeq              uint64
	PreviousStateID          [32]byte
	PreviousFragmentDigest   [32]byte
	Evidence                 []*LocalOrder
	FinalOrder               *FairOrderFragment
	Certificate              *StructuralCertificate
	CandidateEvidenceStateID [32]byte
	CandidatePostStateID     [32]byte
	LeaderSignature          []byte
}

type SCCInfo struct {
	ID        int
	Txs       []TxID
	IsSolid   bool
	TopoIndex int
}

func CalculateEdgeThreshold(n, f uint64, gamma float64) int {
	qH := int(math.Ceil(gamma * float64(n-f)))
	return int(n) - qH + 1
}

func CalculateSolidThreshold(n, f uint64) int {
	return int(n - 2*f)
}

func TransactionID(canonicalBytes []byte) TxID {
	var b bytes.Buffer
	b.WriteString("AUTIG/transaction")
	writeBytes(&b, canonicalBytes)
	return TxID(sha256.Sum256(b.Bytes()))
}

func LocalOrderHead(order *LocalOrder) [32]byte {
	if len(order.OrderedTxs) == 0 {
		return order.PrevHead
	}
	var b bytes.Buffer
	b.WriteString("AUTIG/local-log")
	b.Write(order.PrevHead[:])
	writeUint64(&b, order.PrevPosition)
	writeUint64(&b, order.NewPosition)
	writeTxIDs(&b, order.OrderedTxs)
	return sha256.Sum256(b.Bytes())
}

func LocalOrderDigest(order *LocalOrder) [32]byte {
	var b bytes.Buffer
	b.WriteString("AUTIG/local-order/v1")
	writeUint64(&b, order.ReplicaID)
	writeUint64(&b, order.Epoch)
	writeUint64(&b, order.FragmentSeq)
	writeUint64(&b, order.PrevPosition)
	writeUint64(&b, order.NewPosition)
	b.Write(order.PrevHead[:])
	writeTxIDs(&b, order.OrderedTxs)
	b.Write(order.NewHead[:])
	return sha256.Sum256(b.Bytes())
}

func EvidenceBatchDigest(orders []*LocalOrder) [32]byte {
	ordered := append([]*LocalOrder(nil), orders...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ReplicaID < ordered[j].ReplicaID })
	var b bytes.Buffer
	b.WriteString("AUTIG/evidence-batch")
	writeUint64(&b, uint64(len(ordered)))
	for _, order := range ordered {
		writeLocalOrder(&b, order)
	}
	return sha256.Sum256(b.Bytes())
}

func PreStateIdentifier(epoch, fragmentSeq uint64, previousStateID, evidenceDigest [32]byte) [32]byte {
	var b bytes.Buffer
	b.WriteString("AUTIG/prestate")
	writeUint64(&b, epoch)
	writeUint64(&b, fragmentSeq)
	b.Write(previousStateID[:])
	b.Write(evidenceDigest[:])
	return sha256.Sum256(b.Bytes())
}

func PostStateIdentifier(epoch, fragmentSeq uint64, preStateID [32]byte, finalOrder *FairOrderFragment, part []SCCClaim) [32]byte {
	var b bytes.Buffer
	b.WriteString("AUTIG/poststate")
	writeUint64(&b, epoch)
	writeUint64(&b, fragmentSeq)
	b.Write(preStateID[:])
	if finalOrder == nil {
		writeUint64(&b, 0)
	} else {
		writeUint64(&b, uint64(len(finalOrder.Batches)))
		for _, batch := range finalOrder.Batches {
			writeUint64(&b, batch.Index)
			writeTxIDs(&b, batch.Transactions)
		}
	}
	writeUint64(&b, uint64(len(part)))
	for _, component := range part {
		writeUint64(&b, component.Rank)
		writeTxIDs(&b, component.Transactions)
	}
	return sha256.Sum256(b.Bytes())
}

func FragmentDigest(fragment *VerifiableFairOrderFragment) [32]byte {
	var b bytes.Buffer
	b.WriteString("AUTIG/fragment/v1")
	writeUint64(&b, fragment.Epoch)
	b.Write(fragment.AuthContextID[:])
	writeUint64(&b, fragment.LeaderID)
	writeUint64(&b, fragment.FragmentSeq)
	b.Write(fragment.PreviousStateID[:])
	b.Write(fragment.PreviousFragmentDigest[:])
	writeUint64(&b, uint64(len(fragment.Evidence)))
	for _, order := range fragment.Evidence {
		writeLocalOrder(&b, order)
	}
	if fragment.FinalOrder == nil {
		writeUint64(&b, 0)
	} else {
		writeUint64(&b, uint64(len(fragment.FinalOrder.Batches)))
		for _, batch := range fragment.FinalOrder.Batches {
			writeUint64(&b, batch.Index)
			writeTxIDs(&b, batch.Transactions)
		}
	}
	writeCertificate(&b, fragment.Certificate)
	b.Write(fragment.CandidateEvidenceStateID[:])
	b.Write(fragment.CandidatePostStateID[:])
	return sha256.Sum256(b.Bytes())
}

func writeCertificate(b *bytes.Buffer, certificate *StructuralCertificate) {
	if certificate == nil {
		writeUint64(b, 0)
		writeUint64(b, 0)
		return
	}
	writeUint64(b, uint64(len(certificate.Part)))
	for _, component := range certificate.Part {
		writeTxIDs(b, component.Transactions)
		writeTree(b, component.InTree)
		writeTree(b, component.OutTree)
		writeUint64(b, component.Rank)
	}
	writeUint64(b, uint64(len(certificate.BlockForest)))
	for _, record := range certificate.BlockForest {
		b.Write(record.Vertex[:])
		if record.HasParent {
			b.WriteByte(1)
			b.Write(record.Parent[:])
		} else {
			b.WriteByte(0)
			b.Write(make([]byte, len(record.Parent)))
		}
		writeUint64(b, record.Depth)
	}
}

func writeTree(b *bytes.Buffer, tree []TreeEdge) {
	writeUint64(b, uint64(len(tree)))
	for _, edge := range tree {
		b.Write(edge.From[:])
		b.Write(edge.To[:])
	}
}

func writeLocalOrder(b *bytes.Buffer, order *LocalOrder) {
	writeUint64(b, order.ReplicaID)
	writeUint64(b, order.Epoch)
	writeUint64(b, order.FragmentSeq)
	writeUint64(b, order.PrevPosition)
	writeUint64(b, order.NewPosition)
	b.Write(order.PrevHead[:])
	writeTxIDs(b, order.OrderedTxs)
	b.Write(order.NewHead[:])
	writeBytes(b, order.Signature)
}

func writeTxIDs(b *bytes.Buffer, ids []TxID) {
	writeUint64(b, uint64(len(ids)))
	for _, id := range ids {
		b.Write(id[:])
	}
}

func writeBytes(b *bytes.Buffer, value []byte) {
	writeUint64(b, uint64(len(value)))
	b.Write(value)
}

func writeUint64(b *bytes.Buffer, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	b.Write(encoded[:])
}
