# AUTIG TDSC text revision notes — v5

## Version boundary

- Frozen comparison baseline: `01_authenticated_state_and_progress_v4.tex`
- Current text: `01_authenticated_state_and_progress_v5.tex`
- The v4 file was copied before editing and has not been modified in this round.

## Scope of v5

This version stabilizes the System Model, Protocol, Security Analysis, and technical appendices. It deliberately does not revise the Abstract, Introduction, Related Work, Conclusion, or experimental claims. Those sections should be updated only after the protocol semantics and theorem dependencies below are accepted.

## Principal changes

1. Removed internal version suffixes from LaTeX labels and standardized the core algorithm names:
   - `ApplyEvidence: Deterministic Evidence Transition`;
   - `SafeExtract: Safe Extraction and Structural Certification`;
   - `VerifyFragment: Verify a Proof-Carrying Fragment`;
   - `RecoverState: Checkpoint Recovery and Committed Replay`.
2. Added a consolidated notation appendix that distinguishes the current-batch set $B_r$ from the cumulative active set $A_r$, authoritative evidence from derived caches, and committed from candidate state.
3. Defined the authoritative graph $G_r^\star$ and the leader's materialized graph $\widetilde G_r$, then stated and proved incremental-materialization equivalence through touched-pair completeness.
4. Added a version-consistency condition for pipelined extraction: graph refresh, extraction, and certificate generation must read one immutable evidence version bound by $\widehat h_r$.
5. Replaced informal pruning discussion with explicit retention rules for authoritative evidence, derived caches, live–live pairs, finalized-incident evidence, and delayed append-only chunks.
6. Added a concrete recovery algorithm that validates the checkpoint, reconstructs derived state from positions, and replays only BFT-committed fragments.
7. Added $\mathsf{TxRef}_r$ to the live authenticated state and checkpoint. This closes the recovery gap in which identifiers and positions survived but pre-checkpoint transaction contents could become unavailable.
8. Added an order-leader handoff corollary connecting exact recovery to deterministic UTIG rematerialization.
9. Prepared a separate architecture-figure specification consistent with graph-free followers and structural certificates.

## Claim–support alignment

| Claim | Protocol mechanism | Formal support | Remaining empirical support |
|---|---|---|---|
| Historical pair evidence cannot be inflated or rewritten by the order leader | Prefix-consistent signed logs and authoritative per-replica positions | Position uniqueness; authenticated-state agreement | Cost of follower position/weight maintenance |
| Incremental UTIG refresh matches full reconstruction | Complete touched-pair enumeration and deterministic edge predicate | Touched-pair completeness; incremental-materialization equivalence | Ablation against full graph reconstruction |
| An accepted fragment finalizes exactly the safe closure | SCC trees, rank checks, frontier scan, blocker forest | Safe-fragment soundness and safe-closure-certificate lemma | Verification-time breakdown and malformed-certificate cost |
| Finalized outputs satisfy the stated honest-replica $\gamma$-batch-order-fairness | Solid visibility, fair-edge formation, down-closed safe extraction | Committed observation coverage; fair-edge formation; main fairness theorem | Parameter sweep and adversarial graph workloads |
| Soft pruning preserves extraction semantics | Retain authoritative positions; evict only derived graph state | Soft-pruning equivalence | Memory/rebuild tradeoff |
| Hard deletion of finalized-incident evidence is safe | Every finalized transaction is Solid and the accepted set is down-closed | Hard-deletion theorem; delayed-extension compatibility | Long-run storage and pruning measurements |
| Recovery and handoff preserve all live evidence | Full committed snapshot, live-data references, availability certificate, committed-only suffix replay | Recovery equivalence; handoff continuity | Checkpoint bytes, recovery time, and leader-handoff latency |

## Claims intentionally excluded

- Stateless follower verification.
- Leader-authenticated historical pair totals without follower evidence state.
- Self-contained or succinct proofs.
- Subquadratic worst-case verification.
- Constant or workload-independent follower state.
- Themis-style standard liveness under an indefinitely expanding ancestor set.

The present progress proposition is conditional on stabilized ancestry. This scope belongs in the formal model and the Discussion section, while Abstract and Introduction should later describe it once, concisely.

## Integration order for the complete manuscript

1. Replace the corresponding System Model and Protocol text with the v5 sections while preserving manuscript citation keys and style macros.
2. Place the architecture figure immediately after the Protocol Overview.
3. Keep theorem statements in the main Security Analysis; move the notation and detailed proofs after the manuscript's `\appendices` command.
4. Reconcile every old occurrence of UTIG state, fragment fields, pruning, recovery, handoff, and follower verification with v5 semantics.
5. Update the complexity table only after its rows use the same state and verification boundaries as v5.
6. Revise Abstract, Introduction, contribution bullets, Related Work positioning, Conclusion, and experimental claims last.

## Text-level checks completed

- No duplicate LaTeX labels.
- No unresolved local `\ref` or `\eqref` targets within the v5 file.
- Balanced braces and matched algorithm, theorem, lemma, proof, table, and enumerate environments.
- No remaining `v4` label suffixes or obsolete claims about stateless followers, pair-total proofs, or last-solid recovery.

A full LaTeX compilation was not possible in the current environment because no TeX engine is installed. The section fragment must therefore be compiled after integration with the manuscript preamble to catch package-level and float-placement issues.
