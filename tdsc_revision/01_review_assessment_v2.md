# 第一轮协议修订意见评估（v2）

本评估以用户提供的初始 LaTeX 为定位基准，并以 Themis 的原始
\(\gamma\)-batch-order-fairness 和 FairPropose/FairUpdate 语义作为外部参照。
它只冻结协议层方向；不声称当前原型已实现本文件所列的 follower state、
checkpoint 或 handoff 机制。

## 总体判断

外部意见的总体判断正确：第一版修订堵住了历史权重伪造、batch-local
Blank 遗漏累计依赖、以及 handoff 丢失 external--external evidence 三个 P0
漏洞，但仍未形成可直接证明的冻结协议。v2 采用以下架构：

1. follower 复制经过已提交 LocalOrder 确定性派生的 authenticated evidence
   state，但不物化 \(E\)、SCC、condensation 或完整拓扑序；
2. authoritative evidence 使用 per-replica append-only local positions
   \(\mathsf{Pos}_r(i,u)\)，而不是显式 \(O(n|A_r|^2)\) pair-contribution map；
3. \(W_r\) 是由 positions 确定性派生的聚合缓存；fragment 不再重复携带
   pair totals，而携带线性规模的 SCC/拓扑结构证书；
4. extraction 改为唯一的 solid-ancestor closure。AUTIG 保留 Themis-style
   fairness objective，但不再声称与 Themis 的具体 last-solid extraction
   implementation 完全等价；
5. verification 只产生 candidate post-state；底层 BFT commit 后才安装；
6. recovery 从具有 availability certificate 的完整 committed evidence
   checkpoint 恢复。

## 逐条判断与原稿定位

| 意见 | 判断 | v2 决定 | 初始 LaTeX 的直接位置 |
|---|---|---|---|
| follower 已有 \(W_r\) 时，proof 再携带全部 states/weights 冗余 | 正确，P0 架构问题 | 采纳。删除 state、infix-weight、frontier-weight records；\(\mathcal P_r\) 改为 structural certificate，frontier keys/weights 从本地 \(A_r,W_r\) 枚举 | “Verifiable Fragments and Proofs” 中 “It consists of … Internal-pair totals … Frontier completeness”; Alg. `Extract Fair Prefix and Generate Proof` 的 Step 6；“Phase 4” Steps 1--3；Alg. `Fragment-Local Verification at a Follower` |
| 用 \(\mathsf{Pos}_r(i,u)\) 替代显式 \(C_r(i,\{u,v\})\) | 有条件正确 | 采纳，但必须将 LocalOrder 改成 hash-chained、contiguous、append-only log extension；否则位置比较不能覆盖跨窗口 pair | “Foundations of Graph-Based Protocols” 中 “A local order … sequence … for a given round”; “Phase 1: Local Order Generation and Collection”; Evaluation 中 “lo-size: the maximum number …” |
| authenticated observations 与真实 receive-order fairness 之间缺 completeness bridge | 完全正确，P0 | 采纳。新增 prefix-completeness rule 和 finalization-coverage lemma 的精确前提；在该 lemma 完成前不改写主公平性 theorem | “Batch-Order-Fairness”; “Transaction States and Thresholds”; Phase 1 末句 “cumulative weights … fairness … in the long run”; Evaluation “lo-size … do not alter fairness” |
| solid-complete/down-closed 不等价于原 last-solid prefix | 完全正确，P0 | 采纳问题判断；不采纳“继续模糊二者”。v2 明确选择 canonical solid-ancestor closure，并删除 “exact same extraction” 与 Themis-equivalence claims | “The Solid Anchor Principle”; Alg. `Extract Fair Prefix and Generate Proof`; verification Step 3 的 “exact same deterministic extraction logic”; Thm. `Incremental Equivalence and Pipeline Linearizability` |
| verification 不得在 BFT commit 前安装 \(\Sigma_r\) | 完全正确，P0 | 采纳。verifier 返回 \((accept,\Sigma_r^{cand},h_r)\)，另设 `InstallCommittedState` 事件 | “Consensus and Finalization” 及 Alg. `Fragment-Local Verification at a Follower` 的 return 之前 |
| \(f+1\) matching complaints 不能可靠覆盖 equivocated invalid fragments | 正确 | 采纳。leader-signed objectively invalid fragment 是 candidate fault proof；absence 用 \(n-f\) timeout certificate；replacement 由底层 BFT 排序 | “Dual leadership—Fault detection”; Appendix “Order Fault Detection and Handoff Trigger” |
| verification 还需显式检查 duplicate、subset、sender、prefix、finalized ID、digest binding | 正确 | 全部采纳，集中进 `Admit`、fragment-integrity definition 和 verifier | Alg. `Fragment-Local Verification at a Follower`；Phase 1 admissibility checks |
| checkpoint acknowledgement、canonical encoding、data availability、committed-only replay 未定义 | 正确 | 全部采纳 | “Dual leadership—State recovery”; Appendix “Lightweight State Recovery Protocol”; Thm. `Recovery Soundness …` |
| hard deletion 需要 fairness lemma | 正确 | 采纳为 proof obligation，不把它伪装成已证 invariant | Appendix “Secure UTIG Pruning”; Thm. `Pruning Safety …` |
| 增加 local-order consistency、candidate isolation、finalization completeness | 前两项是状态 invariant；第三项是 theorem-level bridge | 前两项列入 invariants；第三项列为必须证明的 lemma，避免循环论证 | Security Analysis 及 Appendix proofs |
| “authenticated light-state” 会被攻击 | 正确 | 采纳，统一改为 “authenticated graph-free follower state” 或 “authenticated evidence state without materialized graph structure” | Abstract/Introduction “Lightweight, Proof-based Verification”; Protocol “lightweight verification engines”; Conclusion |
| 改为 commitment-only follower | 理论可行但不适合本轮 | 不采纳。它需要 authenticated dictionary/transition completeness proofs 和新实现，超出文本修订可可信覆盖的范围 | 会影响 fragment format、verification、checkpoint、evaluation 全部路径 |

## 原稿中额外发现的阈值问题

初始 LaTeX 的 “Batch-Order-Fairness” 写出
\(n>2f(\gamma+1)/(2\gamma-1)\)，而 “Transaction States and Thresholds”
又定义
\(T_{\mathrm{non\mbox{-}blank}}=\lfloor n(1-\gamma)+\gamma f+1\rfloor\)。
这两处与 Themis 原始 FairPropose 使用的
\(T_{\mathrm{edge}}=n(1-\gamma)+f+1\) 及其约
\(n(2\gamma-1)>4f\) 的可行性条件并不一致。

v2 不再用口头的 “inherit the Themis relation” 掩盖这一差异，而采用整数化的
阈值 \(T_{\mathrm{edge}}=\lceil n(1-\gamma)+f+1\rceil\) 和覆盖条件：
令 \(q_\gamma=\lceil\gamma n\rceil\)，
\[
  T_{\mathrm{solid}}+q_\gamma-n-f\;\ge\;T_{\mathrm{edge}}.
\]
当 \(T_{\mathrm{solid}}=n-2f\) 时，该条件化为
\(q_\gamma-3f\ge T_{\mathrm{edge}}\)，近似对应
\(n(2\gamma-1)>4f\)，并在 \(\gamma=1\) 时给出
\(n\ge4f+1\)。最终 theorem 必须使用同一组取整规则逐步证明。

## 不能通过文字假装已经完成的部分

- 当前 Go prototype 和现有实验并未测量 per-replica position state、follower
  aggregate-weight maintenance、structural-certificate verification、checkpoint、
  invalid-fragment 或 handoff/recovery 成本。
- 因此 v2 文本只能作为 repaired protocol specification。Abstract、Evaluation 和
  Conclusion 后续必须把现有数字明确限定为旧 ordering dataplane 的结果，不能声称
  它们已经验证 repaired end-to-end protocol。
- `lo-size` 若在代码中仍表示任意有限滑动窗口，而不是 contiguous append-only
  chunk，则当前实验不能被用作 prefix-complete LocalOrder 实现的证据。
