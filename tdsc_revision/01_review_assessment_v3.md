# 第一轮协议修订复核（v3）

## 结论：导师对 CCS 2023 正式版 Themis 的判断正确

这次分歧来自版本和参数化混用，而不是一个简单的代数笔误。

- CCS 2023 正式版 Themis，Definition 3.1，将 `gamma` 定义为 honest replicas 的比例：若 `gamma` 比例的 honest nodes 收到 `tx` before `tx'`，则所有 honest nodes 不得把 `tx` 输出得更晚。
- 同页 Remark 1 给出 honest-replica 与 all-replica 两种定义的转换：
  `gamma = (gamma_all n - f)/(n-f)`，等价地
  `gamma_all = (gamma(n-f)+f)/n`。
- 正式版 FairPropose 使用
  `n(1-gamma)+gamma f+1`，Table 1 和 Sec. 4.3.1 使用
  `n > 2f(gamma+1)/(2gamma-1)`。
- 较早的 IACR ePrint/full-version 文本采用 all-replica `gamma`，并写成
  `n(1-gamma)+f+1` 以及近似的 `n(2gamma-1)>4f`。两组公式通过上述参数转换相互对应。

因此，v2 把较早 ePrint 的 all-replica 参数化当作 CCS 正式版 Themis 的唯一语义，并据此判定原稿中的 `+gamma f` 错误，这一判断是错误的。导师的核心结论正确：初始 AUTIG 稿真正的问题是，在 “Batch-Order-Fairness” 段把 `gamma` 写成 all replicas 的比例，却在 “Transaction States and Thresholds” 段直接使用了 honest-replica 参数化的阈值和可行性条件。

v3 选择方案 A：全文采用 honest-replica `gamma`。这与论文“沿用 Themis-style fairness objective”的定位最一致。

## v3 的离散阈值与推导

定义

`q_h = ceil(gamma(n-f))`。

为避免 real-valued threshold 的 floor/ceil 歧义，v3 直接冻结离散协议参数：

`T_solid = n-2f`,

`T_edge = T_non-blank = n-q_h+1`,

并要求精确整数条件

`2q_h >= n+2f+1`。

当 `gamma(n-f)` 为整数时，

`n-q_h+1 = n(1-gamma)+gamma f+1`，

且整数条件等价于

`n(2gamma-1) > 2f(gamma+1)`。

这同时给出 coverage 和 strict direction 两部分，而不是只验证阈值：

1. 令 `H_xy` 为至少 `q_h` 个真实收到 `x before y` 的 honest replicas。
2. 令 `Y_r(y)={i: Pos_r(i,y) != bottom}`，`c_r(y)=|Y_r(y)|`。
3. 集合交给出至少
   `L=q_h+c_r(y)-n`
   个 committed prefix-complete logs 贡献 `x before y`。
4. 当 `y` Solid，`c_r(y)>=n-2f`，所以
   `L>=q_h-2f>=T_edge`。
5. 所有 `y before x` 的贡献者都属于 `Y_r(y)`，而上述 `L` 个贡献者不能贡献反方向，因此
   `W_r(y,x)<=c_r(y)-L`。
6. 同一整数条件给出
   `2L-c_r(y)>=1`，从而
   `W_r(x,y)>W_r(y,x)`。

这正面修补了 v2 只证明 support threshold、没有证明严格方向优势的问题。

## 对导师其余意见的判断

| 意见 | 判断 | v3 处理 |
|---|---|---|
| `c` 不能定义为“包含 y 的 committed prefixes 数量” | 正确，P0 | 改为 distinct replica support `c_r(y)=|{i:Pos_r(i,y)!=bottom}|` |
| coverage 与 edge formation 应拆成两个 lemma | 正确，P0 | 新增 “Committed observation coverage” 与 “Fair edge formation” |
| 必须分别证明 `W(x,y)>=T_edge` 和 `W(x,y)>W(y,x)` | 正确，P0 | 给出下界 `L`、反向上界 `c-L` 和严格不等式 |
| 双向 spanning trees 只证明组内强连通，还需 SCC maximality | 正确，P0 | verifier 枚举 `F_r` 内所有跨组 edge，并要求严格沿 rank 前进；这排除跨组环，因此组是 maximal SCCs |
| Next pointer 与 frontier 分别证明 closure 的包含和排除方向 | 正确，P0 | 明确写成 `F subseteq K` 与 `K subseteq F` 两个证明方向 |
| closure 集合唯一不代表 total order 唯一 | 正确，P1，且与 leader discretion 直接相关 | SCC id 取最小 txid；Kahn 算法按 SCC id 破同序；SCC 内按 txid 排序。verifier 重算全部规则 |
| 延迟 chunk 中含 finalized identifier 不应整块拒绝 | 正确，P0 | 区分 log-chain validation 与 live-evidence contribution；finalized ID 仅推进连续日志，不复活、不产生 incident evidence |
| candidate state 只能在 BFT commit 后安装 | 正确，P0 | 保留 v2 两阶段 candidate/commit 规则 |
| objective invalidity 与 silence 必须分开 | 正确，P0 | invalid signed fragment 是客观 fault evidence；silence 使用 `n-f` timeout certificate |
| timeout certificate commit 后旧 leader 的迟到 proposal 不可接受 | 正确，P0 | 已加入相同 `(epoch,r,h_prev)` tuple 的排他规则 |
| checkpoint acknowledgement 必须签完整 tuple | 正确，P0 | 每个 signer 签 `(epoch,r,h_r,d_r)` |
| `W_r` 是 cache，不必成为 authoritative snapshot 内容 | 正确，P0 | authoritative snapshot 保存 positions；恢复时派生 `W_r`；若实现携带 cache，必须逐项校验 |
| hard deletion 暂时只能是 proof obligation | 正确，P0 | v3 不把它伪装成已完成 theorem |
| 新论文身份是 graph-free follower，而不是 stateless follower | 正确，P1 | claim scope 已重写；保留标题是否修改为后续定位决策 |

唯一没有原样照搬的是“继续使用 Hamiltonian cycle”。v3 的 cumulative graph 尚未证明为 tournament；一般强连通有向图不保证 Hamiltonian cycle。因此 v3 采用任何 SCC 都可执行的 deterministic txid order，并明确只声明 batch-order-fairness，不声明 Themis 的 consequent-transaction fairness。若后续希望恢复 Hamiltonian linearization，必须先证明每个待线性化 SCC 是 strong tournament，并完整规定算法与 tie-breaking。

## v3 修改内容与原稿定位

### 1. Fairness convention 和阈值

- 原稿 “Batch-Order-Fairness” 开头：`Following Themis..., gamma denotes the fraction of all replicas...`
- 原稿紧接的系统条件：`n>2f(gamma+1)/(2gamma-1)`
- 原稿 “Transaction States and Thresholds”：`T_non-blank=floor(n(1-gamma)+gamma f+1)`

三处必须一起替换。不能只改定义或只改阈值。

### 2. Append-only LocalOrder

- 原稿 “Local Order” 开头：`The primary input ... is the local order from each replica.`
- 原稿 “Phase 1: Local Order Generation and Collection”
- Evaluation 中 `lo-size: the maximum number of transaction identifiers included in each LocalOrder`

v3 把 `lo-size` 定义成 contiguous transport chunk size。若当前实现仍产生可跳过早期位置的 window，则实验只能代表旧 dataplane，不能作为 repaired protocol 的实现证据。

### 3. Authenticated follower state 与状态转移

- 替换原稿的 per-round state recomputation、`W <- W+W_batch` 和 `DirtyPairs` 仅首次越阈值逻辑。
- 新状态至少包含 `Seq, Head, Live, Pos, W-cache, DoneRoot, h`。
- 新算法处理 unique evidence、absent-node persistence、touched-pair reorientation 和 delayed finalized entries。

### 4. Structural certificate 和 verifier

- 替换原稿 “Verifiable Fragments and Proofs” 中 state/internal-weight/frontier-weight records。
- 替换原算法 `Extract Fair Prefix and Generate Proof` 的 Step 6。
- 替换 `Fragment-Local Verification at a Follower`。

proof 不再携带 leader 自报 cumulative totals。followers 从自己的 authoritative positions 派生 pair weights；certificate 只证明 SCC partition、condensation order 和 solid reachability。

### 5. Progress、candidate state 和 handoff

- 原稿 “Dual leadership” 的 `f+1 complaints for the same digest` 必须删除。
- verifier 返回 candidate state；BFT commit 后才安装。
- `Solid` 非空时必须提交完整 `K_r`；只有 `Solid` 为空时允许空 `F_r`。
- timeout/handoff decision commit 后，旧 leader 的同轮 proposal 失效。

### 6. Checkpoint、recovery 和 pruning

- 原稿从 `last finalized fragment` 恢复部分 UTIG 的路线必须删除。
- 新 leader 从 last committed live-evidence checkpoint 加 committed-only replay 恢复 exact state。
- external-external positions 不得清零。
- soft pruning 仅删除 derived graph caches；hard deletion 等待独立 theorem。

### 7. Security analysis

旧 “Proof-Induced Graph Correctness”、主 fairness theorem、pruning theorem 和 recovery theorem 不能通过改写措辞继续使用。顺序应为：

1. Position uniqueness；
2. Deterministic weight derivation / committed-state agreement；
3. Committed observation coverage；
4. Fair edge formation；
5. Structural-certificate soundness；
6. Hard-deletion safety；
7. Main batch-order-fairness；
8. Recovery/handoff liveness。

## 可以冻结与仍不能声称完成的内容

可以冻结：honest-replica `gamma`；`Pos` 作为 authoritative evidence；append-only prefix-consistent chunks；followers 不维护 `E/SCC/condensation`；structural certificate；solid-ancestor closure；deterministic total order；candidate state commit 后安装；objective fault 与 timeout 分离；full checkpoint 与 committed-only replay；soft pruning 仅删 derived caches。

仍不能声称已完成：当前 Go prototype 已实现新 follower state；现有 throughput/latency 数字已覆盖新 verifier；hard deletion 已证明；end-to-end BFT liveness 已验证；state/checkpoint overhead 已测量；worst-case verification 是 subquadratic；followers 是 stateless 或 strictly lightweight。

## 建议的后续顺序

1. 把 v3 的 fairness definition、LocalOrder 和 state transition 合入主稿。
2. 合入 structural certificate、verifier 和 progress rule。
3. 完成 hard-deletion 与主 fairness theorem，不再复用旧 theorem 文本。
4. 合入 checkpoint/recovery/handoff。
5. 最后统一 Abstract、Introduction、Related Work、complexity table 和 Conclusion 的论文身份。
6. 在没有修改代码与实验的情况下，把现有结果明确标注为 legacy ordering-dataplane evaluation，并把 repaired protocol 的 state/certificate/recovery 成本列为尚未实现和评估的限制。对 TDSC 来说，这会削弱完整性，但比声称实验已验证一个未实现协议更可信。
