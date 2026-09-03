# Chapter 3 revision decisions — AUTIG v6

## Compact outline

The revised `System Model and Preliminaries` chapter has six subsections:

1. **System and Service Architecture** — replicas, order leader, followers, hosting BFT, candidate-versus-committed state, and handoff boundary.
2. **Network and Adversarial Model** — partial synchrony, adaptive non-mobile corruptions, Byzantine replicas and clients, and timing scope.
3. **Cryptographic and Data-Availability Assumptions** — signatures, canonical commitments, content-bound transaction identifiers, external validity, and recoverable data.
4. **Authenticated Receipt-Order Evidence** — actual first-receipt order, append-only observation logs, committed first positions, visibility counts, and cumulative pair weights.
5. **Fairness and Protocol Objectives** — agreement/evidence consistency/ordering validity, honest-replica batch-order-fairness, exact integer thresholds, cumulative states, and stabilized-ancestry progress.
6. **Graph-Based Ordering Preliminaries** — the complete edge predicate, authoritative graph, SCCs, condensation, down-closure, fairness batches, and deterministic intra-SCC serialization.

## Disposition of the original Chapter 3

| Original material | Decision | Reason and destination |
|---|---|---|
| Opening sentence “We consider a system composed of a static set of $n$ replicas...” | Retain with light editing | It is conventional, clear systems writing and correctly introduces the service boundary. |
| System and Network Model | Retain and split | Replica roles and partial synchrony belong in the system model. Unsupported claims about a constant number of exchanges, inherited optimistic responsiveness, and local $O(|P|)$ verification are removed. |
| Adversarial Model | Retain in compressed form | Byzantine equivocation, omission, false receipt orders, collusion, and adversarial scheduling remain relevant. The enumerated prose is condensed because arbitrary Byzantine behavior already subsumes many examples. |
| Protocol Objectives | Retain but rewrite | Agreement, validity, fairness, and progress belong here, but the old unconditional liveness claim and all-replica interpretation of $\gamma$ conflict with v5/v6 semantics. |
| Cryptographic Assumptions | Retain | PKI, unforgeability, collision resistance, and canonical encoding stay. The old fragment-hash formula moves to the Protocol section because it depends on the final fragment format. |
| Foundations of Graph-Based Protocols | Retain and formalize | Local receipt order, cumulative weights, states, SCCs, and condensation are necessary preliminaries. Per-round state reset is replaced by authenticated cumulative evidence. |
| Old architecture figure | Remove from Chapter 3 | The caption contains obsolete last-Solid extraction, pair-weight proofs, and stateless/fragment-local semantics. Its replacement belongs immediately after Protocol Overview. |
| Verifiable Fragments and Proofs | Move to Protocol | Fragment fields, structural certificates, frontier checks, candidate-state verification, and BFT installation are protocol mechanisms rather than environmental assumptions. |
| Internal/frontier pair-total proof | Delete | v6 followers derive weights from authenticated positions; the fragment no longer carries leader-asserted historical totals. |
| Old proof-size derivation based on pair records | Delete or replace in Complexity | It analyzes an obsolete proof format. The current text reports structural-certificate records and the worst-case local pair-inspection cost. |
| Hamiltonian-cycle intra-SCC ordering | Delete | A general strongly connected directed graph need not be Hamiltonian. v6 assigns one fairness-batch index per SCC and uses deterministic transaction-identifier serialization only for execution. |

## Reverse outline and paragraph roles

### System and Service Architecture

- **Opening:** establishes the static replica set and fair-ordering service.
- **Layer boundary:** separates AUTIG ordering from BFT agreement/finality.
- **State continuity:** explains fragment sequences, order-leader epochs, and committed handoff state.

### Network and Adversarial Model

- **Network:** states partial synchrony and separates timing-independent safety from post-GST progress.
- **Replica adversary:** states corruption and scheduling powers.
- **Client adversary:** restricts fairness to valid, available transactions.

### Cryptographic and Data-Availability Assumptions

- **Authentication:** defines signatures, canonical encoding, and domain-separated commitments.
- **Transaction identity:** binds identifiers to canonical contents.
- **Availability:** states the external-validity rule needed by fragments and checkpoints.

### Authenticated Receipt-Order Evidence

- **Evidence source:** connects actual first receipt to correct replicas' append-only logs.
- **Authoritative representation:** defines positions, visibility, and cumulative pair weights.
- **Uniqueness:** explains why retransmission cannot amplify evidence.
- **Versioning:** distinguishes candidate and committed evidence versions.
- **Lifecycle:** defines live and finalized identifiers and delayed-log continuity.

### Fairness and Protocol Objectives

- **Objectives:** separates BFT agreement, evidence consistency, ordering validity, and fairness.
- **Fairness definition:** fixes honest-replica $\gamma$ and fairness-batch indices.
- **Thresholds:** gives exact integer thresholds, feasibility, and parameter conversion.
- **States:** defines `DetermineState` and the cumulative active set $A_r$.
- **Progress scope:** states the precise stabilized-ancestry condition without claiming standard liveness.

### Graph-Based Ordering Preliminaries

- **Edge semantics:** defines the threshold, strict-weight comparison, and public tie rule.
- **Authoritative graph:** separates evidence-defined truth from leader-side UTIG materialization.
- **Graph concepts:** defines ancestors, SCCs, condensation, and down-closure.
- **Output interpretation:** separates fairness-batch assignment from deterministic execution serialization.

## Claim–evidence map

- **Claim:** Retransmission cannot increase a replica's contribution to a pair.  
  **Evidence:** Unique committed first positions and Eq. `eq:position-weight`.  
  **Status:** Supported by the position-uniqueness lemma.

- **Claim:** The edge orientation is deterministic and independent of leader choice.  
  **Evidence:** Eq. `eq:edge-predicate` uses cumulative weights and a public identifier tie rule.  
  **Status:** Supported as a definition; incremental equivalence proves that the leader materializes the same graph.

- **Claim:** AUTIG uses Themis-style honest-replica batch-order-fairness.  
  **Evidence:** $q_h=\lceil\gamma(n-f)\rceil$, the integer thresholds, and the fair-edge-formation proof.  
  **Status:** Supported under Eq. `eq:integer-feasibility`.

- **Claim:** Followers are graph-free.  
  **Evidence:** The follower state contains authenticated positions and derived weights but excludes the edge set, SCCs, and condensation DAG.  
  **Status:** Supported by the formal state definition; implementation evidence remains to be audited later.

- **Claim:** AUTIG provides progress once ancestry stabilizes.  
  **Evidence:** Eventual evidence admission, a correct post-GST order leader, and Proposition `prop:stabilized-progress`.  
  **Status:** Supported only under the stated conditional premise; standard liveness is not claimed.

## Self-review

- **Clarity:** Pass. Every subsection has a distinct purpose and defines terms before the Protocol uses them.
- **Flow:** Pass. The chapter proceeds from participants and faults to authenticated evidence, objectives, and finally graph semantics.
- **Terminology:** Pass. It uses $A_r$ as the sole active set,
  $\Sigma_r/\widehat\Sigma_r$ for committed/candidate state, *correct*
  for uncorrupted replicas, and graph-free rather than stateless followers.
- **Unsupported claims:** Removed. Constant-exchange progress, inherited optimistic responsiveness, Hamiltonian-cycle existence, standard liveness, and pair-total proof claims are absent.
- **Missing evidence:** The formal text is internally supported, but graph-free state-maintenance cost and the v6 certificate/recovery mechanisms still require implementation and evaluation before corresponding empirical claims appear in the Abstract or Introduction.
