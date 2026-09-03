# AUTIG v5 architecture figure specification

This figure should replace any architecture diagram that depicts stateless followers, leader-supplied cumulative pair totals, a proof-induced graph, or recovery from a last-solid anchor.

## Recommended layout

Use one horizontal `figure*` with four numbered stages and a checkpoint path below them.

1. **Prefix-consistent evidence**
   - Show replicas emitting signed, contiguous LocalOrder extensions.
   - Label the extension fields only at a high level: sender, sequence range, previous head, chunk, new head, signature.
   - The arrow into the ordering layer is labeled “BFT-admitted evidence batch $\mathcal L_r$.”

2. **Authenticated graph-free state at every replica**
   - Show the replicated state as $\Sigma_{r-1}\xrightarrow{\mathcal L_r}\widehat\Sigma_r$.
   - Inside the state box, list $\mathsf{Seq}$, $\mathsf{Head}$, $\mathsf{TxRef}$, $\mathsf{Live}$, $\mathsf{Pos}$, $W$, $\mathsf{Done}$, and the state identifier.
   - Mark $W$ as a derived cache and $\mathsf{Pos}$ as authoritative evidence.
   - Add a boundary note: “Followers store evidence state but no persistent edge set, SCCs, or condensation DAG.”

3. **Leader-only UTIG materialization and extraction**
   - Show $\mathsf{TouchedPairs}_r$ driving an incremental refresh of $G_{\mathrm{utig}}$.
   - Then show SCC decomposition, the Solid-safe down-closed set $K_r^{\mathsf{safe}}$, deterministic SCC linearization, and blocker-forest construction.
   - The output is $(F_r,\mathsf{Part}_r,\mathcal P_r)$.

4. **Structural verification and committed installation**
   - The proposed fragment contains $\mathcal F_r=\langle\mathsf{hdr}_r,F_r,\mathcal L_r,\mathcal P_r\rangle$.
   - The certificate box lists SCC in/out trees, ranks, and the blocker forest; it must not list cumulative pair totals.
   - Followers replay `ApplyEvidence`, evaluate internal and frontier edge predicates from local evidence, verify SCC maximality and safe-closure completeness, and produce $\Sigma_r^{\mathsf{cand}}$.
   - A BFT-commit gate separates the candidate state from installed $\Sigma_r$ and execution release.

5. **Checkpoint/recovery path**
   - Below stages 2–4, show $\mathsf{CP}_j=\langle\mathsf{epoch}_j,j,h_j,d_j,\mathsf{AvailQC}_j\rangle$.
   - The checkpoint points to the canonical live-evidence snapshot plus retrievable live transaction data.
   - Recovery performs “verified snapshot + BFT-committed suffix replay,” after which a new order leader rematerializes the UTIG.

## Visual emphasis

- Use one color for replicated authoritative evidence and a second color for leader-only derived graph structures.
- Use a dashed border for candidate state and a solid border for BFT-committed state.
- Show the asymmetry spatially: the large graph box appears only in the leader lane; follower lanes contain pair-predicate evaluation but no graph object.
- Do not use “stateless,” “succinct proof,” “self-contained proof,” or “subquadratic verification” in the figure.

## Publication caption

```latex
\caption{AUTIG separates authenticated evidence replication from graph
materialization.  All replicas deterministically apply the committed
prefix-consistent LocalOrders and retain the resulting live receipt-order
evidence.  The order leader alone incrementally materializes the UTIG,
extracts the Solid-safe down-closed set, and constructs an SCC and boundary
certificate.  Followers verify the proposed fragment by evaluating
prefix-relevant edge predicates from their local evidence state; they do not
maintain the persistent edge set, SCC decomposition, or condensation DAG.
Verification produces a candidate state that is installed only after BFT
commit.  Checkpoints preserve the complete live-evidence state and referenced
transaction data for recovery and order-leader handoff.}
\label{fig:autig-architecture}
```

## Suggested first reference in the protocol overview

```latex
Figure~\ref{fig:autig-architecture} summarizes the resulting asymmetry: every
replica authenticates cumulative receipt-order evidence, whereas only the
order leader materializes and incrementally maintains the dependency graph.
```
