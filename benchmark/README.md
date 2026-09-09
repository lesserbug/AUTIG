# AUTIG Fabric benchmark

The benchmark parameters are defined directly in `fabfile.py`. AWS, SSH,
repository, and instance settings are defined in `settings.json`.

Install the controller dependencies and list tasks:

```bash
cd benchmark
python -m pip install -r requirements.txt
fab --list
```

Run and parse a local benchmark:

```bash
fab local
fab logs
```

For AWS, first set `repo.url` and the SSH key fields in `settings.json`.
The default deployment uses one AUTIG replica per AWS instance and TCP port
`settings.port` on every instance.

AWS credentials are read by `boto3` from the normal environment or AWS
configuration files. Every configured region must contain the named EC2 key
pair, and the instances need public IPv4 addresses because the default region
matrix communicates across regions.

```bash
fab create --nodes=1
fab info
fab install
fab remote
fab logs
fab kill
fab stop
```

`fab create --nodes=1` creates one instance in each configured region. The
`nodes` matrix in `fabfile.py` is the AUTIG replica count, so enough running
instances must exist for the largest configured value. All `n` replicas are
started; `faults` marks the last `f` replicas as the benchmark's malicious
LocalOrder producers and does not mean that those instances are omitted.

Logs are stored in `benchmark/logs` and parsed JSON results in
`benchmark/results`. `fab destroy` permanently terminates all AWS instances
tagged with the default testbed name `autig`.

The fixed-leader benchmark waits for verification acknowledgements from
`n-f` distinct replicas (including the constructing leader) before its adapter
simulates commit. These acknowledgements are benchmark control messages, not a
BFT quorum certificate. Reported latency ends at that leader-side simulated
commit; it includes quorum verification but excludes a real hosting-BFT round
and follower commit-notification delivery.

Only submissions and commit events before the leader's fixed measurement
deadline enter the metrics. Followers continue serving the final prefix until
the finish barrier; this shutdown time is excluded. The controller rejects
verification failures, send failures, missing replica reports, and differences
in final committed sequence, state ID, or fragment digest. TCP writes have a
five-second deadline; a timed-out send invalidates the run.

Evidence senders rotate by `(replica_id - fragment_seq) mod n`; the first `n-f`
priorities are required each round. All configured replicas must keep sending
LocalOrders in these experiments. A silent scheduled replica can stall this
prototype because order-leader handoff is not implemented. The existing
graph/cache implementation is otherwise retained.

Workload pacing uses the leader's measurement start and the cumulative target
`floor(rate * elapsed_seconds)`. A ticker only wakes the generator; delayed or
dropped ticks do not discard arrivals. When behind, it submits the difference
and immediately recomputes the target. At rates above 1000 tx/s, wakeups are
spaced by 1 ms and may submit several transactions. Each transaction retains its
actual submission timestamp; no transactions are generated after the cutoff to
fill a shortfall. A counted transaction's in-progress fanout is completed, as
before. Minor endpoint shortfalls and catch-up bursts are therefore possible;
this is a paced workload, not an exact inter-arrival-time guarantee.

This minimal change retains one generator and synchronous sends to all replicas.
`Send` includes a per-peer lock, Gob encoding and TCP write, so aggregate fanout
cost grows with replica count and can still limit the offered rate. Catch-up
fixes lost scheduling opportunities only when spare sending capacity exists.
There is no new client queue that can accumulate transactions while reporting
the configured rate as achieved. If profiling at 10/20/50 replicas shows fanout
is the bottleneck, a follow-up should use bounded per-replica FIFO send workers
and separately measure generated, queued, dispatched and pending work, queue
delay and drops. Simply counting asynchronous enqueues would obscure the load
actually reaching replicas.

For throughput/latency experiments, JSON results retain three distinct rates:

| Field | Meaning |
| --- | --- |
| `rate`, `configured_offered_rate` | Requested transaction input rate, not multiplied by replica count. |
| `actual_offered_rate` | `submitted / measurement_seconds`: actual transaction submission attempts initiated before the cutoff, not confirmed receipt by every replica. |
| `average_tps` | `finalized / measurement_seconds`: leader-side simulated commits before the same cutoff. |

`offered_rate_relative_deviation` is the absolute difference between actual and
configured rate divided by configured rate. `offered_rate_tolerance` keeps its
existing default of 0.02, but is now a diagnostic threshold: an excess prints a
warning and saves the result with `offered_rate_within_tolerance=false`, allowing
the benchmark matrix to continue. This flag concerns target-load fidelity,
not protocol correctness or a limit on finalized throughput. A zero target with
zero submissions has zero deviation; unexpected submissions at a zero target
produce a warning, a false flag, and JSON `null` for the undefined relative
deviation. Verification, send and final-state consistency failures still reject
the run.

Use `actual_offered_rate` as the load-axis value, retain the configured value and
flag, and identify flagged points in plots/tables. A finalized-throughput plateau
or decline while actual offered load grows is consistent with saturation and
must not fail this rate check. If actual load itself plateaus, those points do
not establish behavior at the higher configured loads; investigate the sender
and transport before attributing the plateau to AUTIG. Matching average offered
rate alone does not establish smooth arrivals or absence of client contention;
the generator shares the leader process and catch-up can introduce bursts.
Always report `outstanding`, `completion_ratio` and `latency_samples` alongside
mean completed-transaction latency: under overload, unfinished transactions are
censored at the cutoff, and the reported mean is not the latency of all offers.

Regression checks (controller dependencies must be installed for Python):

```bash
go test ./...
python -m unittest discover -s benchmark -p 'test_*.py'
```

The first evidence optimization preserves the validated append-only positions:
it enumerates new/old and new/new pairs once, instead of visiting old/old pairs.
Followers execute the same authentication, admission, weight and visibility
updates but skip the graph-refresh pair set. Changed nodes are still tracked
internally to update Solid/Shaded/Blank state. Quorums, certificate checks,
snapshots, the single network worker and fragment sequencing are unchanged.

The pre-optimization reference is commit
`edf39655853139d77e6e405c3ba705b1b1d2fb66` (local tag
`perf-baseline-before-pair-opt`). A frozen test-only evidence implementation
supports differential state/output tests and a repeatable local microbenchmark:

```bash
go test ./pkg/ofo -run TestEvidenceOptimizationMatchesReference
go test ./pkg/ofo -run '^$' -bench BenchmarkEvidenceEnumeration -benchtime=3x
```

For one diagnostic run, set `stage_timing: True` in the `local` parameters or
`remote` matrix in `fabfile.py`. Set `cpuprofile: True` to collect CPU profiles.
Both default to false; the equivalent Go flags are `-stage-timing` and
`-cpuprofile`. Avoid mixing profiled and unprofiled results in a performance
comparison. Remote profiles are downloaded alongside node logs as
`remote-nN-rR-runK-nodeI.cpu.pprof`; local profiles remain in `.runtime` as
`cpu_profile_nodes_0_1_2_3_4.pprof` for a five-node run. The existing download
step retrieves profiles only after all processes have exited and flushed them.

Stage records are JSON lines prefixed with `BENCHMARK STAGE`. Events are:

- `collection_since_first_order`: first eligible LO to a complete evidence
  batch. It excludes time before that first LO and is not the whole round time.
- `construct`: lock acquisition, committed-state/manager clones, evidence,
  graph refresh, proposal, post-state clones/finalization and signing.
- `verify`: handler entry, lock acquisition, state clone, evidence replay,
  certificate verification and post-state clone/finalization.
- `hosting`: broadcast start/end, observed verification quorum, leader commit
  and commit fanout completion.

All `*_us` marks are offsets from that record's start, measured with Go's local
monotonic clock. Subtract adjacent marks for stage durations. For example,
`evidence_applied_us - manager_cloned_us` is leader evidence time;
`quorum_reached_us - broadcast_end_us` is the remaining acknowledgement wait
after broadcast, not the full follower verification time. Do not add follower
verification to broadcast time: they overlap. `start_unix_ns` aids correlation,
but subtracting timestamps across machines requires clock synchronization.
Partial/failed operations can omit later marks. Only records with `accepted`
true completed construction/verification; this does not mean hosting commit.

Construction/verification records include pre-finalization Live/active counts,
weight count, mean/max positions, receipt queue length and cumulative local
fresh/retransmit counters. These counters describe generated/retransmitted LO
attempts, not successful delivery or globally unique fresh transactions.
Construction records also include evidence ID occurrences, output transaction
count and certificate SCC/tree/block counts. These are structural sizes, not
Gob wire byte counts. Network queue delay/length and wire serialization size
are deliberately not instrumented in this first change; verification starts
when the existing single message worker invokes the verifier.

Every binary now logs its embedded Go build revision, dirty status and Go
version. Results preserve them as `git_commit`, `git_modified`, `go_version`;
old logs or builds lacking VCS metadata use null, never the controller's SHA.
Remote results also record `replica_builds`, actual instance type, region/AZ,
VPC and addresses under `replica_instances`, plus the current `ip_mode=public`.
This change records the existing network placement; it does not switch routes
or choose a different AZ. Reject mixed/unknown/dirty deployments when selecting
formal comparison data, and rerun all node counts on the same committed build.
These diagnostic fields do not change throughput, latency or tolerance rules.

## Local computation ablations (experiments 3 and 4)

These test-only benchmarks measure one leader or follower's local computation.
`n=50` means 50 logical replicas' evidence/state, not 50 communicating machines.
No production code, thresholds, ordering rules, commit semantics, Fabric tasks,
or deployment modes are changed. One EC2 instance in one region is sufficient.

Use the **final main-experiment parameters** for formal collection. Development
defaults are `n=10,50`, `f=1`, `gamma=0.9`, matching the current main experiment;
there is no gamma sweep. Test-binary flags `-ablation-nodes`, `-ablation-f` and
`-ablation-gamma` permit freezing the final parameters without editing production
code. The normal service constructor validates them. Fixture checks also fail if
a requested structure (e.g. a retained SCC) is not actually obtained.

### Samples and correctness gate

Both experiments use the same generator and fixed seeds (default `1,7,19`).
Canonical transaction content is 512 bytes. Seed changes content/TxIDs and hence
public-ID ordering and proof traversal. The reception profiles are controlled,
not sampled AWS traces; seeds do not simulate independent network schedules.
Real Ed25519 test keys are derived deterministically from the fixture seed outside
timing, using the existing test authenticator's signing/verification methods.
Both experiments and independent processes reproduce the same signed candidate;
both branches share keys and signed evidence. Key derivation/file loading
is not part of an operation. The test authenticator uses the same Ed25519
primitives as `fileAuthenticator`; admission uses an in-memory locked content
store with copying and a no-op `ValidateAdmission`, like the standalone adapter.
Full results therefore measure this implementation, not arbitrary business
admission or full BFT consensus.

Each fixture executes a nonempty bootstrap commit, a history round, and another
committed round retaining that history. Every round uses the collector's rotating
`n-f` sender set, contiguous signed extensions, normal production construction,
follower verification and `CommitPending`. No authoritative maps are fabricated.

| Case | Purpose |
| --- | --- |
| `common-low` | 12 retained transactions, then release plus 96 fresh transactions; consistent reception and nonempty output. A low-backlog proxy, **not a recorded AWS workload**. |
| `synthetic-small-many` | 24 retained Shaded transactions, 192 fresh transactions. |
| `synthetic-large-few` / `synthetic-large-many` | Large retained Shaded history, 8 / 128 fresh transactions. |
| `synthetic-blocked-solid` | Three Shaded predecessors retain a large Solid history; 8 fresh transactions. |
| `synthetic-empty-extension` | Signed empty extensions; no effective new positions. |
| `synthetic-delayed-done` | The replica omitted from bootstrap reports those finalized transactions late; LO positions advance, effective positions do not. |
| `synthetic-cycle-held` / `synthetic-cycle-release` | Three cyclic block reception orders form a large SCC; keep it blocked or add missing first positions and release it. |

Large history defaults to 480 transactions (`-ablation-history`). Fresh counts
are unique new transactions, **not** new `(replica, transaction)` positions.
The release cases additionally report existing live transactions at replicas
that have not assigned them first positions. The high-backlog/cycle cases are
legal controlled synthetic sensitivity samples, not claims about workload
frequency. They cannot attribute a particular AWS throughput change; that needs
the run's stage logs or replayable inputs.

Before any leaf benchmark timer starts, the exact sample passes graph, output,
certificate, authoritative/cache state, commit and following-round checks, plus
production-vs-recompute verifier acceptance. Cache counts are independently
derived from retained positions outside timing. Graph comparison ignores absent
zero weights and empty adjacency representation. StateID alone is insufficient:
`PostStateIdentifier` covers batches and Part membership/rank, whereas
`FragmentDigest` also covers trees and BlockForest.

The separate differential tests reject malformed evidence, context/signatures,
wrong batches/order/SCC membership, incomplete or unsafe output, bad trees,
bad forests and wrong pre/post IDs. Internal mutations are re-signed and tested
at their intended stage. Alternative valid trees and blocker roots are accepted
by both verifiers. The tests compare decisions and state effects, not error text.

### Timing boundaries

| Experiment | Core | Full |
| --- | --- | --- |
| Graph maintenance | `refresh` versus allocation and complete materialization from the **same already-updated state**. Evidence and restoration of the incremental graph/touch snapshot are outside timing. | Production `constructCandidate` versus a test-only rebuild wrapper: sorting, state copies, evidence checks/update, graph work, output/certificate generation, post-state processing, evidence copy, digest, leader signing and pending state. |
| Follower verification | Production certificate verification versus complete graph materialization, SCC/maximal safe output/deterministic order recomputation and checks of the **supplied** proof using graph edges. | Production `verifyCandidate` versus a test-only wrapper retaining context, digest/signature checks, evidence validation/update, state copies, pre/post IDs, finalization and pending state. |

Full incremental construction includes **both** production graph copies. The
rebuild constructor does not collect unneeded graph touches, so its Full ratio
includes that difference; Core isolates graph maintenance. Both constructors
retain the same post-state/post-manager copying sequence. A rebuild stores
nonzero weight entries; the incremental cache can also contain explicit zeros.
Both cache sizes are reported; no artificial padding is added for map equality.

The recompute verifier generates no new proof and does not call the entire
production certificate verifier after recomputation. Exact recomputed batches
establish safety, maximality and order; graph-based tree/forest checks still
reject a bad certificate attached to a correct output. Temporary follower graphs
are discarded, not copied or persisted during finalization. Test-only proof
checks/wrappers mirror their production counterparts; retain differential tests
when those production functions change.

Every iteration starts from the same immutable committed snapshot and clears
`pending` outside timing, avoiding duplicate-candidate fast returns. Full calls
perform their actual state/cache copies **inside** timing. Core setup copies,
fixture generation, assertions, disk I/O, network, collector waits and hosting
commit installation are excluded. Required algorithm allocations are included.
Untimed setup allocations may still affect GC/cache conditions; no per-branch
forced GC is used. Core and Full ratios must be reported separately.

The output includes `ns/op`, `B/op`, `allocs/op` and per-sample counts: before and
after-evidence Live/Done, classifications, positions/max positions, nonzero
weights, nodes/edges, SCC count/max/nontrivial count, new effective positions,
new Live, LO occurrences, touched pairs, output batches/transactions, tree edges,
forest records and the two graph-cache weight counts. Structural counts are
sample metadata, not rates or per-iteration accumulated counts.

### Development and collection

From the repository root (quote comma-containing arguments in PowerShell):

```text
go test ./... -count=1
python -m unittest discover -s benchmark -p test_run_ablations.py
go test ./pkg/ofo -run '^$' -bench '^BenchmarkAblation' -benchmem -benchtime=1x -count=1 -cpu=1 '-ablation-nodes=10,50' -ablation-seeds=1 -ablation-history=30
```

`1x` is only a smoke test. For formal collection use one otherwise idle
**m5.xlarge**, matching the main experiment's Go version. No concurrent benchmark,
stage logging, race instrumentation or profiling. The following standard-library
Python script runs correctness checks, compiles the test binary once, then
executes four independent processes sequentially with branch order AB/BA/AB/BA:

```text
python3 benchmark/run_ablations.py --nodes 10,50 --faults 1 --gamma 0.9 --seeds 1,7,19 --history 480 --runs 4 --benchtime 10x --instance-type m5.xlarge --region YOUR_REGION
```

Use the main experiment's final `f/gamma` in that command. Each process covers
both experiments, Core/Full and every sample. Normally use 3-5 processes; more
iterations improve within-sample precision but do not replace distinct seeds or
processes. The command only uses the current machine; it creates no AWS resource.
Instance type/region are operator-supplied metadata, not independently detected.

The runner explicitly sets `GOMAXPROCS=1` and `-test.cpu=1`. This is a single-P
microbenchmark setting, not CPU affinity and not the main program's default
parallelism. Its absolute times must not be presented as distributed latency.

Outputs go to a new `benchmark/results/ablation-TIMESTAMP` directory (or a new
directory supplied with `--output`):

- `metadata.json`: commit, dirty status, Go version/target, CPU/OS, GC settings,
  GOMAXPROCS, parameters and exact commands. Commit the code before formal data
  collection; retain development runs separately.
- `correctness.log`, `build.log`, `run-*-AB/BA.log`: complete raw records.
- `measurements.json`: all branch measurements and structural metrics.
- `pairs.csv`: same-process/same-seed ratios (`Rebuild/Incremental` or
  `Recompute/Certificate`), absolute times and allocations. Missing, duplicate or
  structurally mismatched branch pairs fail collection.
- `summary.csv`: per-seed medians and min/max **paired** speedup across processes;
  seed variation is kept separate from repeated measurement variation. These
  ranges are not confidence intervals. The compiled test binary is also retained.

Report results as sorting construction/verification microbenchmark gains on the
same hardware. A verification speedup does not imply an equal finalized-TPS gain;
the distributed main experiment measures that conversion.
