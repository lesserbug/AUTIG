# AUTIG v4 核心章节合并说明与后续修订顺序

## 1. 本轮已经冻结的论文主张

AUTIG 的当前核心主张是协议架构而非新的公平定义或完整的标准活性方案：

> All replicas deterministically replicate authenticated receipt-order evidence, while only the order leader persistently materializes and incrementally maintains the live dependency graph. A structural certificate lets graph-free followers verify the maximal Solid down-closed output without reconstructing or extracting the full live graph.

这一主张由以下三条技术链支撑：

1. prefix-consistent LocalOrders → authoritative positions and cumulative pair weights；
2. leader-only UTIG materialization → incremental touched-pair refresh and safe extraction；
3. structural certificate + local pair predicates → graph-free follower verification。

Standard liveness against indefinitely extending Condorcet chains不属于当前声称已经解决的问题。正文只保留 `Progress under stabilized ancestry`。

## 2. 本轮已完成的跨章节修改

### 2.1 System Model

System Model 现在只承担四项职责：

- replica、network 和 hosting BFT assumptions；
- non-mobile adaptive Byzantine adversary 与 Byzantine clients；
- cryptographic and data-availability assumptions；
- honest-replica γ parameterization、整数 thresholds、batch-order-fairness 与 progress scope。

以下内容不再放在 System Model：

- fragment tuple；
- internal-pair/frontier proof records；
- proof-size formula；
- follower verification workflow；
- leader handoff and recovery mechanism。

这些内容全部属于 Protocol。

### 2.2 Protocol

Protocol 按数据流重新组织为：

1. Protocol Overview；
2. Prefix-Consistent LocalOrders；
3. Authenticated Graph-Free Follower State；
4. Deterministic Evidence Transition and UTIG Refresh；
5. Safe Finalization and Fairness Batches；
6. Proof-Carrying Fragment Verification；
7. Checkpointing, Pruning, Recovery, and Handoff。

LocalOrder admissibility 现在同时检查 digest-bound transaction availability 和 application validity，防止只有 identifier、没有可执行内容的节点进入 live state。

### 2.3 Security Analysis

旧的 `Proof-Induced Graph Correctness` 与“asserted totals dominate current batch”论证已由以下定理链替换：

1. Authenticated-state agreement；
2. Safe-fragment soundness；
3. γ-Batch-Order-Fairness；
4. Soft-pruning equivalence；
5. Hard-deletion safety；
6. Recovery equivalence；
7. Progress under stabilized ancestry。

主公平性证明只使用权威 follower state，不再信任 leader 声称的历史 totals。

### 2.4 Pruning

Pruning 被拆成两个语义不同的操作：

- **Soft pruning/cache eviction**：只删除 leader-side derived structures；authoritative positions 和 live--live evidence 保留，重物化后与未裁剪 UTIG 完全一致。
- **Hard deletion**：只在 transaction 已进入 BFT-committed (F_r) 后删除其 incident live evidence；(mathsf{Done}_r) 阻止 delayed LocalOrder 重新激活它。

旧稿中以下规则不再成立：

- 因 transaction 在 current batch 为 Blank 而删除其累计状态；
- 因 Shaded transaction 位于 last-solid anchor 之后而删除其 live evidence；
- 用经验 staleness horizon (D) 证明 correctness；
- 从 last finalized fragment 的 partial pair records 重建未终结状态。

## 3. 原稿位置与替换关系

| 原稿位置或 distinctive sentence | v4 处理 |
|---|---|
| System Model: “γ denotes the fraction of all replicas” | 改为 honest-replica γ，定义 (q_h=\lceil\gamma(n-f)\rceil) |
| System Model: “We inherit ... standard liveness” | 删除；明确 stabilized-ancestry progress scope |
| Transaction States: “states are recomputed per round” | 改为 cumulative monotone support and state |
| `Verifiable Fragments and Proofs` in System Model | 整体移入 Protocol verification subsection |
| Fragment proof contains leader-asserted cumulative totals | 删除；follower 从 committed positions 导出权威 weights |
| Phase 3: “last SCC ... solid anchor” | 改为 maximal Solid down-closed set (K_r^{\mathsf{safe}}) |
| Algorithm 2: frontier ((B_r\setminus F)\times F) | 改为 cumulative active set ((A_r\setminus F_r)\times F_r) |
| `thm:verif_sc` on proof-induced graph | 改为 Safe-fragment soundness + main fairness theorem |
| `thm:liveness_bounds` | 删除，不再声称 bounded standard liveness |
| `thm:pruning_safety` using Blank/post-anchor removal | 改为 Soft-pruning equivalence |
| Anchor-fragment recovery theorem | 改为 committed full live-evidence checkpoint + committed-only replay |

## 4. 下一步修改顺序

### Step 1 — 统一 notation、labels 与图示（下一轮）

- 将 v4 临时 `-v4` labels 映射为整稿最终 labels；
- 更新 notation table：增加 (q_h,A_r,\mathsf{Pos}_r,\Sigma_r,K_r^{\mathsf{safe}},\mathsf{Done}_r,\beta_r)；
- 删除旧 `DirtyNodes/DirtyPairs` threshold-crossing 定义，统一为 `TouchedNodes/TouchedPairs`；
- 重画 architecture figure，删除 stateless follower、last-solid anchor 和 pair-total proof；
- 用 running example 校验图、算法与正文一致。

### Step 2 — 清理其余 theorem 与 appendix

- 用 touched-pair exact refresh 重写 incremental equivalence theorem；
- 删除旧 liveness-bound proof 和 (R_{\mathrm{defer}})；
- 用 checkpoint replay induction 替换 anchor-fragment recovery proof；
- 重写 Appendix pruning，删除经验 (D) 的 correctness 角色；若保留 (D)，只能作为 cache-retention performance parameter；
- 检查 adaptive-adversary 与 availability-retention assumptions 一致。

### Step 3 — Related Work 与 Background

- 把 Themis deferred ordering 作为未采用的 standard-liveness mechanism 准确描述；
- 增加 fairness taxonomy：receive-order、batch-order、bounded-unfairness、data-dependent/relative-order variants；
- 明确 AUTIG 的 delta 是 verification architecture，而不是 fairness-definition novelty；
- 加入近期 fair-order liveness 与 DAG ordering work，但不让 Related Work 改写核心 claim。

### Step 4 — Evaluation semantics audit

- 逐项确认现有代码是否实现 prefix-consistent logs、positions、safe closure、BlockForest 和 checkpoint；
- 未实现的 v4 mechanism 不能由旧实验结果声称支持；
- 先修改 evaluation questions 和 metric definitions，再决定最小补实验；
- 至少区分 leader materialization、follower evidence update、certificate verification 和 pipeline overlap。

### Step 5 — 最后统一 Abstract、Introduction 与 Conclusion

只有在 Protocol、theorems 和 evaluation scope 对齐后再修改：

- 删除 stateless、succinct、self-contained proof、critical innovations；
- 核心贡献改为 graph-free authenticated verification architecture；
- 性能数字只保留由最终协议实现直接支持的结果；
- limitation 集中为一段，不在各节重复防御。

## 5. 当前 claim--evidence map

| Claim | Theoretical evidence | Implementation/evaluation evidence | Status |
|---|---|---|---|
| Correct followers derive identical evidence state | deterministic replay + position uniqueness | 尚未审计代码 | 理论支持，实验待核 |
| Followers remain graph-free | follower state excludes (E), SCCs, condensation | 尚未审计内存结构 | 定义支持，工程待核 |
| Accepted fragment equals (K_r^{\mathsf{safe}}) | trees + ranks + frontier + BlockForest | certificate 尚需实现确认 | 理论支持，工程待核 |
| γ-batch-order-fairness | coverage + strict edge + down-closure proof | 不由性能实验决定 | 支持 |
| Soft pruning preserves extraction | exact rematerialization from authoritative state | cache policy 尚需审计 | 理论支持 |
| Hard deletion is safe | main fairness theorem + Done non-reactivation | 尚需审计 delayed chunks | 理论支持 |
| Standard liveness | 无 | 无 | 不声称 |
| Existing speedups apply to final v4 | 无直接证据 | 旧实现语义未知 | 待代码/实验审计 |

## 6. 五维自审

- **Contribution**：通过 leader-only graph materialization 与 graph-free structural verification 形成清晰系统贡献；没有把安全修复包装成新公平定义。
- **Writing clarity**：System Model 与 Protocol 职责已经分开；下一步需要最终 notation table 和架构图。
- **Experimental strength**：当前不能判断，必须先核对实现语义。
- **Evaluation completeness**：proof bytes、verification breakdown、checkpoint/handoff 和 adversarial graphs 仍缺失。
- **Method soundness**：历史权重认证、cumulative live frontier、safe finalization、candidate/commit 和 pruning/recovery 已形成一致理论链；standard liveness 明确不在 claim 内。
