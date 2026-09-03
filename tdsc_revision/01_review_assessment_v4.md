# AUTIG 第四版修订评估与采用记录

## 1. 总体判断

这轮意见中的核心反例成立，而且属于 P0 协议安全问题。v3 的

\[
K_r=\{u\in A_r:\exists s\in \mathsf{Solid}_r,\ u\leadsto s\}
\]

允许一个尚未获得充分已提交可见性的 Shaded transaction 作为 Solid transaction 的祖先被提前终结。此前的 Fair Edge Formation lemma 只在被终结的后项 (y) 已经是 Solid 时成立，因此不能覆盖这种祖先。继续使用 v3 的 extraction set 会使主 batch-order-fairness theorem 存在未覆盖情形。

第四版采用

\[
K_r^{\mathsf{safe}}
=\{u\in\mathsf{Solid}_r:\mathsf{Anc}_r(u)\subseteq\mathsf{Solid}_r\},
\]

即完整 live graph 上由 Solid transactions 构成的唯一最大 down-closed set。该修改修复公平性证明的缺口，但不会自动给出 Themis 的 standard liveness：对手仍可能持续增加新的 Shaded ancestors。第四版因此将无条件结论限定为安全性、batch-order-fairness、certificate soundness、hard-deletion safety 和 recovery equivalence；进展结论明确依赖 stabilized ancestry。若最终论文仍要声称 Themis-style standard liveness，必须进一步加入 deferred ordering/unspooling 或等价机制。

## 2. v3 反例

取 (n=5,f=1,gamma=1)，则

\[
q_h=4,qquad T_{\mathrm{edge}}=2,qquad T_{\mathrm{solid}}=3.
\]

四个诚实副本都真实观察到 (x\prec y)。当前已提交的日志前缀可以为：

- (H_1:[x,y,s])；
- (H_2:[s])，而其尚未提交的后缀包含 (x,y)；
- (H_3,H_4) 尚未提交包含 (x,y) 的前缀；
- Byzantine replica (B:[y,s])。

此时 (c(s)=3)，所以 (s) 为 Solid；(c(y)=2)，所以 (y) 为 Shaded；并且 (W(y,s)=2)，形成 (y\to s)。然而 (W(x,y)=1<T_{\mathrm{edge}})，而 (x) 甚至可能仍为 Blank。v3 的 solid-ancestor closure 会终结 (y) 与 (s)，但不会终结 (x)，从而违反 (b(x)\le b(y))。完整 incoming-frontier scan 也无法发现尚未形成的 (x\to y)。

该反例定位到原始稿的以下连续设计：

- “A prefix of the fair order is extracted up to the last SCC that contains at least one solid transaction” 一段；
- Fig. `fig:protocol` 的 “selecting the last SCC with a Solid node as the anchor” 图注；
- `Algorithm 1` 的 extraction/last-solid 逻辑；
- `Algorithm 2` 的 (B_r\setminus F) frontier；
- `Theorem 1`/`thm:verif_sc` 关于 canonical prefix 与 fairness 的结论；
- `thm:pruning_safety` 中“post-anchor Shaded nodes 可以安全裁剪”的论证。

## 3. 对各项意见的处理

| 意见 | 判断 | 优先级 | 第四版处理 |
|---|---|---:|---|
| 非-Solid ancestor 可能违反公平性 | 正确，是真实反例 | P0 | 用 (K_r^{\mathsf{safe}}) 替换 solid-ancestor closure；所有被终结交易必须为 Solid，且集合对完整 live graph down-closed |
| 空 fragment 不能再等价于 Solid set 为空 | 正确 | P0 | 改为 (F_r=\varnothing\iff K_r^{\mathsf{safe}}=\varnothing)；BlockForest 证明所有被排除 Solid 交易都有 Shaded ancestor |
| DoneRoot 不能直接回答 membership query | 正确 | P0 | 区分可查询字典 (mathsf{Done}_r) 与承诺 (mathsf{DoneRoot}_r)；checkpoint 保存字典或绑定的 application checkpoint reference |
| fairness batch 与 execution serialization 混淆 | 正确 | P0 | 一个 SCC 对应一个 fairness-batch index；fragment 是 SCC batches 的序列；SCC 内排序只是确定性执行序列 |
| LocalOrder eventual admission 未定义 | 正确 | P1 | 增加连续重传、bounded chunk、公开轮转 sender priority、honest order leader 和 eventual dissemination 假设 |
| Algorithm 1 每轮扫描全部 live nodes | 正确，但不是安全漏洞 | P1 | 增加 `TouchedNodes` 与 `TouchedPairs`，只更新本轮获得新 position 的节点和关联 pair |
| availability QC 中至少有 (n-2f) 个 correct holders | 正确 | P2 | checkpoint 段采用精确计数，并保留“至少一个可恢复副本”的可用性结论 |
| 文本像 revision specification | 正确 | P1 | 主文本改为 Protocol Overview、Formal Protocol、Security、Complexity/Discussion 与 Appendix Proofs；删除“repaired/gap/remaining obligation”等修订语气 |
| 信息密度过高 | 正确 | P1 | 先给一页内 protocol overview，再给状态与算法，证明细节后移 Appendix |
| 增加 running example | 正确 | P1 | 增加 (n=5,f=1,gamma=1) 的 (a,b,c,d,e) 示例，串联 positions、weights、SCC、safe closure、blocker 与 verification |

## 4. 需要调整而非原样采用的建议

### 4.1 Blocker certificate 按 transaction 覆盖，而非要求 follower 先识别外部 SCC

“为每个被排除 Solid SCC 提供 blocker path”在概念上正确，但 graph-free follower 并不维护 (A_r\setminus F_r) 的 SCC decomposition。第四版使用可共享节点的 `BlockForest`：每个被排除的 Solid transaction 都出现在一条从 Shaded root 出发的已验证路径上。多条路径共享前缀，因此证书记录数为 (O(|V_{\mathsf{blk}}|))，同时避免要求 follower 对完整外部图运行 SCC。

### 4.2 Safe closure 是安全修复，不是完整活性修复

“Shaded ancestor 以后会成为 Solid，因此只是延迟”只有在祖先集合最终停止扩展时成立。若 Byzantine inputs 或持续到达的交易能够不断形成新的 Shaded predecessors，安全闭包可以被无限阻塞。这与公平排序协议近期重新暴露的 liveness 风险一致。因此第四版只证明 `Progress under stabilized ancestry`；standard liveness 需要单独的 deferred-ordering/unspooling 设计，不能由 safe closure 的定义直接推出。

### 4.3 确定性 SCC 内排序不等于 SCC 内公平

按 transaction identifier 序列化可以消除 order leader 的自由选择，但 batch-order-fairness 只约束公平批次编号，不约束同一 SCC 内的严格执行顺序。最终正文将二者分开，并在 Discussion 中集中声明：确定性序列化不提供更强的 intra-batch fairness。若威胁模型包含 transaction-ID grinding，还需另行考虑 commit 后随机性或更强的 intra-batch ordering rule；这不是当前 batch-order-fairness theorem 的内容。

### 4.4 Sender rotation 只建立 honest-leader 条件下的 admission liveness

公开轮转 priority 使诚实 leader 的选择可预测且无自由裁量，但无法把 Byzantine leader 对某个 pending extension 的沉默变成单轮可客观验证的 fault。第四版因此把 eventual honest order leader 作为 hosting BFT liveness assumption。若要在论文中宣称更强的 censorship resistance，需要把 sender schedule 或 pending-extension receipts 纳入共识状态。

## 5. 第四版协议语义

### 5.1 权威证据状态

每个 follower 保存：

- prefix-consistent per-replica log heads and sequence numbers；
- per-replica first positions (mathsf{Pos}_r(i,u))；
- 从 positions 唯一导出的 support counts 与 cumulative pair weights；
- cumulative live/active set；
- 可查询 finalized dictionary (mathsf{Done}_r) 及其 commitment；
- 最大已分配 fairness-batch index (eta_r)；
- fragment/state-chain identifiers。

Follower 不保存 UTIG edge set、SCC decomposition、condensation DAG 或 topological order。因此准确定位是 authenticated graph-free follower state，而不是 stateless verification。

### 5.2 两阶段确定性转移

\[
\Sigma_{r-1}\xrightarrow{\mathcal L_r}\widehat\Sigma_r
\xrightarrow{(F_r,\mathsf{Part}_r)}\Sigma_r.
\]

Verifier 只产生 candidate state；底层 BFT commit 对应完整 fragment 后才安装。后状态标识绑定 (F_r) 和 SCC partition，防止相同扁平列表被解释成不同 fairness batches。

### 5.3 Safe finalization

\[
K_r^{\mathsf{safe}}
=\{u\in\mathsf{Solid}_r:\mathsf{Anc}_r(u)\subseteq\mathsf{Solid}_r\}.
\]

它是完整 live graph 上由 Solid transactions 构成的唯一最大 down-closed set。每个 included SCC 获得一个共同的全局 fairness-batch index；空 prefix 不增加批次计数。

### 5.4 Structural certificate

证书包含：

- `Part`: (F_r) 的 SCC partition；
- 每个 included SCC 的 inward/outward spanning trees；
- deterministic condensation ranks；
- `BlockForest`: 所有 excluded Solid transactions 到 Shaded ancestor 的共享证书路径。

Follower 从本地权威 weights 检查 tree edges、所有 (F_r) 内 cross-component edges、完整 incoming frontier，以及 blocker edges。Leader 不再在 fragment 中重复声称 cumulative totals。

### 5.5 Checkpoint、recovery 与 handoff

Checkpoint commitment 覆盖完整 live positions、log heads、Done dictionary、fairness-batch counter 和 state identifier。恢复只 replay BFT-committed fragments。`AvailQC` 中每个 signer 签署完整 tuple ((epoch,r,h_r,d_r))；(n-f) 个 signers 中至少有 (n-2f) 个 correct holders。

## 6. 安全论证结构

主文保留以下 theorem statements 与 proof intuition：

1. Authenticated-state agreement；
2. Safe-fragment soundness；
3. (gamma)-Batch-Order-Fairness；
4. Hard-deletion safety；
5. Recovery equivalence。

Appendix 按以下顺序给完整证明：

1. Position uniqueness and deterministic weights；
2. Committed observation coverage；
3. Fair edge formation；
4. Safe-closure certificate；
5. Main fairness theorem；
6. Hard-deletion safety；
7. Recovery induction。

Fair Edge Formation 使用两个独立界：

\[
W_r(x,y)\ge L=q_h+c_r(y)-n
\]

以及

\[
W_r(y,x)\le c_r(y)-L.
\]

由 (c_r(y)\ge n-2f) 和 (2q_h\ge n+2f+1) 同时推出 (L\ge T_{\mathrm{edge}}) 与 (L>c_r(y)-L)。这避免了“达到 threshold 自动得到严格方向”的错误推理。

## 7. 与原稿联动修改范围

| 原稿位置 | 必须同步修改的内容 |
|---|---|
| Abstract 与 Introduction contribution list | 删除 stateless、self-contained cumulative-weight proof、无条件 liveness 等表述；改为 replicated authenticated evidence + graph-free followers + structural verification |
| Background 中 Themis extraction 描述 | 可保留为 related baseline，但必须明确 AUTIG 不再直接采用 last-Solid prefix |
| System Model 的 Batch-Order-Fairness | 使用 honest-replica (gamma)、(q_h=\lceil\gamma(n-f)\rceil)，并定义 SCC-level fairness batch index |
| `Verifiable Fragments and Proofs` | 移入 Protocol；删除 leader-asserted historical totals，加入 `Part/Trees/Rank/BlockForest` |
| Fig. `fig:protocol` | 重画 follower evidence state、leader UTIG、safe closure、candidate/commit transition；旧 last-solid anchor 图不能保留 |
| Algorithm 1 | 改为 prefix-consistent evidence transition + touched update + safe extraction |
| Algorithm 2 | 改为 equality-by-derivation、SCC maximality、frontier、blocker 与 commit-after-verify |
| `Theorem 1`/`thm:verif_sc` | 用 Safe-fragment soundness 和主 fairness theorem 替换，不再依赖 proof-induced historical totals |
| Pruning appendix | 禁止按 current-round Blank 或 post-anchor Shaded hard prune；只 soft-delete derived caches，hard deletion 仅限 committed finalized incident state |
| Handoff/recovery appendix | 从 committed live-evidence checkpoint 恢复，保留 external--external pairs，不从 last fragment 的 partial proof 重建 |
| Evaluation | 实现若仍是 v1/v2 语义，则现有性能结果不能直接证明 v4；必须明确 prototype/version 对应关系 |

## 8. TDSC 正文组织

建议最终结构为：

1. **Protocol Overview**：一页内说明 evidence replication、leader UTIG、safe closure certificate、candidate/commit transition 和 checkpoint；配一张数据流图。
2. **Formal Protocol**：依次定义 append-only logs、authenticated state、incremental transition、safe extraction、certificate verification、recovery/handoff。
3. **Security Analysis**：主文只保留 theorem statements、三步 fairness proof intuition 与活性范围。
4. **Appendix Proofs**：集合交下界、strict-direction 上界、SCC maximality、safe-closure completeness、recovery induction。
5. **Discussion**：一次性说明 follower evidence state、worst-case quadratic pair inspection、intra-SCC limitation 和 stabilized-ancestry liveness scope。

建议重画的总览图应体现以下单向数据流：

`committed LocalOrder extensions -> replicated Pos/Live/Done state -> leader-only UTIG -> safe SCC batches + structural certificate -> follower local predicate checks -> candidate state -> BFT commit -> installed state/checkpoint`。

可使用的最终图注为：

> **AUTIG protocol flow.** All replicas deterministically extend an authenticated receipt-order state from committed prefix-consistent LocalOrders. The order leader alone materializes the UTIG, updates touched edges, and extracts the maximal Solid down-closed set. Followers derive pair predicates from local evidence and verify the proposed SCC partition, incoming frontier, and blocker forest without maintaining the live dependency graph. Verification produces a candidate state that is installed only after the underlying BFT commits the fragment.

## 9. Claim--evidence 检查

| 计划保留的 claim | 当前理论支持 | 当前实现/实验支持 | 处理 |
|---|---|---|---|
| Followers derive identical cumulative evidence | 有：deterministic transition induction | 尚需实现对应状态 | 可作为 theorem；实验补 state-maintenance cost |
| Accepted (F_r) equals (K_r^{\mathsf{safe}}) | 有：trees + ranks + frontier + blocker forest | 尚需实现 certificate | 可作为 theorem |
| (gamma)-batch-order-fairness | 有：仅在 safe closure 与整数条件下 | 不靠性能实验证明 | 可作为主 theorem |
| Standard liveness under indefinitely extending chains | 无 | 无 | 不得声称；加入 deferred ordering 后重新证明 |
| Followers are graph-free | 有：state definition 不含 (E)/SCC/DAG | 尚需实现确认 | 可保留，不能写 stateless |
| Structural certificate is succinct/subquadratic | 无 | 无 | 删除；只报告 certificate records 与 pair-inspection worst case |
| v4 retains reported AUTIG speedups | 无直接支持 | 原实验对应旧协议 | 重新实现/重测前不得把旧数值作为 v4 结论 |

## 10. 当前仍未解决的事项

1. **P0 -- Standard liveness**：safe closure 可被不断增长的 Shaded ancestor chain 阻塞。投稿前必须在“加入 deferred ordering”和“明确只提供 weaker stabilized-ancestry progress”之间做出定位选择。若论文继续把 Themis 作为相同公平性与活性的直接替代，前者更稳妥。
2. **P0 -- 实现一致性**：如果现有代码仍使用 batch-local states、last-Solid extraction 或 leader-asserted pair totals，论文不能声称实现了第四版协议。
3. **P1 -- End-to-end evaluation**：至少需要 replicated evidence maintenance、safe extraction、blocker certificate、invalid proof、handoff/checkpoint 和 adversarial chain workload 的成本。
4. **P1 -- 图示与 running example 对齐**：旧图中的 last-Solid anchor、stateless follower 和 pair-weight proof 均应替换。
5. **P1 -- Related Work 定位**：需要把 AUTIG 与 Themis deferred ordering、Aequitas weak liveness，以及近期对 fair-order liveness 的重新分析放在同一 taxonomy 中。

## 11. 自审结论

- **正确性**：v3 的 non-Solid ancestor 漏洞已被协议定义而非 theorem wording 修复；threshold 与 strict-direction 分开证明。
- **完整性**：Done membership、fairness-batch counter、empty fragment、eventual admission、checkpoint holder 数量均已进入正式语义。
- **清晰度**：采用 overview/formal/proofs 三层组织，并增加 running example。
- **主张克制**：不使用 stateless、self-contained historical proof、succinct、subquadratic 或无条件 standard liveness。
- **提交风险**：最大剩余风险不是行文，而是 standard-liveness 机制和 v4 implementation/evaluation 尚未闭合。
