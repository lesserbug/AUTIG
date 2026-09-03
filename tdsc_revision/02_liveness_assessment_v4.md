# AUTIG v4 活性机制评估：为什么不能直接采用年龄截断

## 1. 结论

导师意见对问题来源的判断具有很高参考价值：只要 extraction 必须等待一个不断扩展的 ancestor closure 完全成熟，攻击者就可能通过连续 Condorcet chaining 造成 weak liveness。AUTIG 必须在以下两条路线之间明确选择：

1. 保留当前 γ-batch-order-fairness，只证明 stabilized-ancestry progress；或
2. 加入 Themis-style deferred ordering 和 batch unspooling，在不删除公平依赖的情况下证明 standard liveness。

导师提出的 age-bounded truncation 不能直接作为第二条路线。给出的 predicate 不仅会削弱当前公平定义，而且本身不足以保证 ancestry 有界。因此，不应把 v4 的 Proposition 直接升级为 Standard Liveness Theorem。

## 2. 年龄截断为什么破坏当前公平定理

考虑 (n=5,f=1,γ=1)，于是

\[
q_h=4,\qquad T_{\mathrm{edge}}=2,\qquad T_{\mathrm{solid}}=3.
\]

令老交易 (u) 已经 Solid。四个诚实副本都实际观察到 (v\prec u)，但当前只有两个诚实日志提交了包含 (u) 的前缀，另有一个 Byzantine replica 报告 (u)。此时可以同时满足：

\[
c(u)=3,\qquad c(v)=2,
\]

所以 (u) 为 Solid 而 (v) 仍为 Shaded。Prefix completeness 给出

\[
W(v,u)=2=T_{\mathrm{edge}},
\]

并且 Fair Edge Formation lemma 要求 (v\to u)。导师建议的年龄 predicate 却会因为 (u) 已超时且 (v) 为 Shaded 而删除该边。随后 (u) 可以在 (v) 之前终结，直接违反

\[
b(v)\le b(u).
\]

因此，这不是“只牺牲攻击者的局部公平性”。协议无法根据 Shaded 状态判断 (v) 是攻击者交易还是延迟的诚实交易。若采用该规则，必须把安全性质改名并重定义为 bounded/age-scoped fairness，不能继续声称原 γ-batch-order-fairness。

## 3. 给出的年龄 predicate 也不足以证明 standard liveness

该 predicate 只禁止 Shaded (v) 指向老 (u)，仍有两个反例。

### 3.1 Shaded predecessor 变为 Solid 后边会重新出现

如果 predicate 每轮按当前状态重新计算，那么 (v) 从 Shaded 变为 Solid 后，原来的 (v\to u) 又会满足 predicate。于是 (u) 的 ancestor set 仍可在超时后扩展。

若协议规定被截断的边永久不可恢复，则必须提交一个 irreversible cutoff record；这会使 delayed honest evidence 永久失效，并明确改变公平定义。

### 3.2 攻击者可以持续加入新的 Solid predecessor

该规则完全不禁止新的 Solid transaction 指向老 (u)。攻击者可以广泛传播其交易，使其快速达到 Solid，然后建立

\[
v_1\to u,
\quad v_2\to v_1,
\quad v_3\to v_2,
\ldots
\]

每个新节点在形成相关边时都已经 Solid。年龄规则不会删除这些边，因此不能推出 ancestry stabilization。

## 4. 对另外两种建议的判断

### 4.1 Epoch-banded fairness

Epoch isolation 可以真正形成有限 ordering domain，但必须满足：

- epoch membership 由 committed protocol state 决定，而不是客户端声明；
- closed epoch 不再接收新顶点；
- 每个 epoch 的 admission 有确定上界；
- 系统只在前一个 epoch 关闭或按公开规则并行推进后续 epoch。

如果只允许同一 epoch 内的公平依赖，standard liveness 可以在 bounded-load 假设下证明，但公平性质会变成 epoch-scoped batch-order-fairness。若允许相邻 epoch 的反向边，则仍可形成

\[
u_e\leftarrow v_{e+1}\leftarrow x_{e+2}\leftarrow\cdots,
\]

所以“相邻 epoch”本身并不保证 transitive ancestry 有界。

该方案的协议改动大于表面看起来的程度：fairness definition、transaction admission、edge predicate、checkpoint、batch numbering、theorem 和 evaluation 都要改变。它不适合作为保持 Themis-style fairness 定位的最小补丁。

### 4.2 Client-accountable rate limiting

Rate limiting 是有用的 DoS 防御和实验变量，但不是 Byzantine protocol liveness proof：

- permissionless clients 可以创建新身份；
- 即使身份固定，较大的 (k) 仍允许多个身份持续构造链；
- quota 不能约束 Byzantine replicas 选择性传播不同交易；
- 没有 bounded offered load 时，任何有限处理能力的系统都不能给每笔交易有界确认时间。

只有在固定、不可伪造的有限客户端集合和全局 outstanding-transaction cap 下，它才能帮助证明 live set 有界。该假设显著强于当前系统模型，不能作为核心 standard-liveness theorem 的默认前提。

## 5. Themis 实际采用的机制

Themis 没有通过 age-based edge deletion 获得 standard liveness。其核心是：

1. `FairPropose` 提交一个有限的 partial proposal graph；
2. 图包含 Solid transactions 以及必须随其进入 proposal 的 Shaded transactions；
3. 已确定的边不可被后续 leader 修改；
4. 尚未确定的 Shaded--Shaded relations 由后续 `FairUpdate` 补齐；
5. proposal graph 成为 tournament 后，`FairFinalize` 输出剩余交易；
6. batch unspooling 允许同一个公平批次分段输出，但不允许后续批次插入其间。

Themis 还明确指出：SCC 及其所有 predecessor SCC 都只包含 Solid transactions 时，可以在整个 proposal fully specified 之前立即终结。这一优化正好对应 v4 的 (K_r^{\mathsf{safe}})。因此，v4 safe closure 更适合定位为 deferred frame 内的 early-finalization rule，而不是独立承担 standard liveness 的完整 extraction mechanism。

## 6. 对 AUTIG 结构影响最小且保持原公平性的方案

推荐方案是 **single-open committed deferred frame**。它比完整支持多个并行 deferred proposals 简单，同时保留 v4 的绝大部分状态和验证架构。

### 6.1 Frame creation

当不存在 open frame 时，order leader 使用一个 committed LocalOrder batch 按 Themis 的 `FairPropose` 规则生成有限 frame：

\[
\Phi_j=\langle j,V_j,E_j^{\mathsf{fixed}},M_j,
                 \mathsf{Emitted}_j,H_{\Phi_j}\rangle.
\]

其中：

- (V_j) 在 frame commit 后固定；
- (E_j^{\mathsf{fixed}}) 是创建时已经确定且不可改变的边；
- (M_j) 是尚未确定的 Shaded--Shaded pair set；
- (mathsf{Emitted}_j) 记录已经通过 safe closure 输出的交易。

新交易不能加入已提交 frame，而是在该 frame 完成后进入下一个 frame。避免 cascading 的关键不是删除旧边，而是使每个 ordering frame 的 vertex membership 有限且不可扩展。

### 6.2 Frame-local evidence

Frame creation 和 update 必须使用 Themis-compatible one-sided ordering evidence。对 frame 采用的 sender set (I_j)，定义

\[
Q_j(u,v)=
\sum_{i\in I_j}
\mathbf 1[
  \mathsf{Pos}(i,u)\ne\bot\land
  (\mathsf{Pos}(i,v)=\bot\lor
   \mathsf{Pos}(i,u)<\mathsf{Pos}(i,v))].
\]

这里“只出现 (u)”表示该 committed append-only prefix 已经声明 (u) 先于任何未来追加的 (v)。不能直接冻结当前 v4 的 two-sided (W(u,v))，否则 Themis 的 proposal-exclusion lemma 不会自动成立。

### 6.3 Early finalization

在 oldest open frame 内继续使用 v4 的定义：

\[
K_{j,r}^{\mathsf{safe}}
=\{u\in\mathsf{Solid}_{j,r}:
   \mathsf{Anc}_{G_{j,r}}(u)\subseteq
   \mathsf{Solid}_{j,r}\}.
\]

该集合由现有 `Part/InTree/OutTree/Rank/BlockForest` certificate 验证。已经输出的 safe prefix 不会被后续 update 改写，因为 unresolved pairs 只涉及仍为 Shaded 的 frame members。

### 6.4 Forced completion

后续 fragment 携带 frame-member update evidence，只能为 (M_j) 中的 pair 增加确定方向。正确副本最终收到 frame 中的所有有效交易后，每个 frame member 达到所需可见性，(M_j) 变为空，frame graph fully specified。Follower 随后验证剩余 SCC order 并关闭 frame。

只有 oldest open frame 可以向 execution interface 输出交易；因此一个被分段输出的 fairness batch 保持 contiguous。新交易不能延长旧 frame，但其 evidence 仍在全局 authenticated follower state 中累积，供下一 frame 使用。

### 6.5 Follower state 与 graph-free 定位

Follower 不需要永久保存 frame adjacency graph。Committed state 只增加：

\[
\mathsf{OpenFrameRef}_r
=\langle j,H_{\Phi_j},H_{\mathsf{updates}},
         \mathsf{EmittedRoot}_j\rangle.
\]

Frame creation fragment 和 updates 由 BFT data availability 保留。Follower 在验证时临时重放 oldest frame 的相关记录，验证结构证书后释放临时图。Checkpoint 必须绑定 `OpenFrameRef` 并保证相关 frame data 可恢复。

## 7. 该方案仍需新增的证明

不能只增加一个 Standard Liveness theorem statement。至少需要：

1. **Frame-admission fairness**：被排除在 (Phi_j) 外的交易不可能属于比 frame 内交易更早的公平批次；
2. **Fixed-edge immutability**：后续 evidence 不会要求反转 committed fixed edge；
3. **Safe-prefix stability**：frame update 不会改变已输出 (K_{j,r}^{\mathsf{safe}})；
4. **Frame completion**：在 partial synchrony、eventual honest leader 和 admissible-load 假设下，有限 (M_j) 在 (L(\Delta)) 内清空；
5. **Contiguous unspooling**：同一公平批次的分段输出之间不会插入后续批次；
6. **Cross-frame fairness**：frame commit order 与 proposal-exclusion lemma 共同保证全局 (b(x)\le b(y))；
7. **Recovery equivalence**：checkpoint 恢复 open frame、updates 和 emitted boundary 后得到相同状态。

在这些 lemma 完成前，不能把 v4 的 conditional progress proposition 升级为 standard-liveness theorem。

## 8. 最小改动建议

按当前投稿期限，建议分两级处理。

### 可立即冻结

- 保留 v4 的 safe closure、BlockForest 和 `Progress under stabilized ancestry`；
- 在 Discussion 明确 age expiration 会改变公平性质；
- 不采用 Δ_max，避免与网络延迟 Δ 冲突；
- 不把 quota 或 adjacent-epoch filtering 写成 protocol-level liveness proof。

### 若必须声称 Themis-style standard liveness

- 新增 single-open committed deferred frame；
- 将 v4 safe closure 改成 frame-local early-finalization rule；
- 加入 FrameCreate、FrameUpdate 和 FrameClose transitions；
- 扩展 fragment、checkpoint、handoff 和 recovery；
- 移植并重证 Themis proposal-exclusion 与 batch-unspooling lemmas；
- 对 deferred-frame creation/update/replay 成本补实验。

这是保持当前公平定义时影响最小的完整路线，但它仍然是一次真实协议扩展，不能被描述成一段 age predicate 的局部修改。
