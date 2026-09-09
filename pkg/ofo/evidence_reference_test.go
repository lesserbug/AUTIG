// Frozen pre-optimization oracle from edf39655853139d77e6e405c3ba705b1b1d2fb66.
// Test-only: keep independent of the optimized pair enumeration.
package ofo

import (
	"SpeedFair_simplify/pkg/types"
	"fmt"
)

func applyEvidenceReference(state *EvidenceState, orders []*types.LocalOrder, epoch, fragmentSeq, n, f uint64, gamma float64, maxOrderSize int, auth types.Authenticator, admission types.TransactionAdmission) (map[types.TxID]bool, map[pairKey]bool, error) {
	if fragmentSeq != state.FragmentSeq+1 {
		return nil, nil, fmt.Errorf("fragment sequence %d does not extend %d", fragmentSeq, state.FragmentSeq)
	}
	if len(orders) != int(n-f) {
		return nil, nil, fmt.Errorf("evidence batch contains %d senders, want %d", len(orders), n-f)
	}

	senders := make(map[uint64]bool, len(orders))
	resolved := make(map[types.TxID]types.TxID)
	for index, order := range orders {
		if order == nil {
			return nil, nil, fmt.Errorf("evidence batch contains a nil extension")
		}
		if index > 0 && orders[index-1].ReplicaID >= order.ReplicaID {
			return nil, nil, fmt.Errorf("evidence senders are not in canonical order")
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
