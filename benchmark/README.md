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
