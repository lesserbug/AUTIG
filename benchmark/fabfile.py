from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from json import dump, dumps, load, loads
from itertools import product
from math import ceil
from pathlib import Path
import hashlib
import os
import re
import shlex
import shutil
import subprocess
import sys
import time

import boto3
from botocore.exceptions import ClientError
from fabric import Connection, task


BENCHMARK_DIR = Path(__file__).resolve().parent
REPO_ROOT = BENCHMARK_DIR.parent
SETTINGS_FILE = BENCHMARK_DIR / "settings.json"
BUILD_DIR = BENCHMARK_DIR / ".build"
RUNTIME_DIR = BENCHMARK_DIR / ".runtime"
LOG_DIR = BENCHMARK_DIR / "logs"
RESULT_DIR = BENCHMARK_DIR / "results"
GENESIS = "0" * 64
GO_VERSION = "1.22.12"


def _controller_identity():
    path = Path(__file__).resolve()
    return {"controller_script": str(path), "controller_sha256": hashlib.sha256(path.read_bytes()).hexdigest()}


def _print_metrics(metrics):
    hidden = {"replica_states", "replica_builds", "replica_instances", "ip_mode", "mechanism_nodes"}
    print(dumps({key: value for key, value in metrics.items() if key not in hidden}, indent=2))


def _settings():
    with SETTINGS_FILE.open("r", encoding="utf-8") as source:
        settings = load(source)
    for key in ("key", "port", "repo", "instances"):
        if key not in settings:
            raise RuntimeError(f"settings.json is missing {key!r}")
    return settings


def _testbed(settings):
    return settings.get("testbed", "autig")


def _binary(name):
    suffix = ".exe" if os.name == "nt" else ""
    return BUILD_DIR / f"{name}{suffix}"


def _build_local(include_node=True):
    BUILD_DIR.mkdir(parents=True, exist_ok=True)
    if include_node:
        subprocess.run(
            ["go", "build", "-o", str(_binary("autig")), "./pkg"],
            cwd=REPO_ROOT,
            check=True,
        )
    subprocess.run(
        ["go", "build", "-o", str(_binary("autig-keygen")), "./cmd/autig-keygen"],
        cwd=REPO_ROOT,
        check=True,
    )


def _validate_parameters(parameters):
    n, f, gamma = parameters["nodes"], parameters["faults"], parameters["gamma"]
    if n <= 0 or f < 0 or f >= n or n < 3 * f + 1:
        raise RuntimeError("AUTIG requires n >= 3f+1")
    if not 0.5 < gamma <= 1:
        raise RuntimeError("AUTIG requires gamma in (1/2, 1]")
    q_h = ceil(gamma * (n - f))
    if 2 * q_h < n + 2 * f + 1:
        raise RuntimeError("parameters violate 2*ceil(gamma*(n-f)) >= n+2f+1")
    if parameters["tx_size"] < 16:
        raise RuntimeError("tx_size must be at least 16 bytes")
    _fault_parameters(parameters)


def _fault_parameters(parameters):
    count = parameters.get("byzantine_count")
    if count is None:
        count = parameters["faults"]
    delay = parameters.get("byzantine_lo_delay_ms", 0)
    if type(count) is not int or not 0 <= count <= parameters["faults"]:
        raise RuntimeError("byzantine_count must be an integer between 0 and faults (or None to use faults)")
    if type(delay) is not int or not 0 <= delay <= (2**63 - 1) // 1000000:
        raise RuntimeError("byzantine_lo_delay_ms must be a nonnegative integer representable as a Go duration")
    return {"byzantine_count": count, "byzantine_lo_delay_ms": delay}


def _remote_log_prefix(parameters, run):
    faults = _fault_parameters(parameters)
    return (f"remote-n{parameters['nodes']}-r{parameters['rate']}-f{parameters['faults']}"
            f"-b{faults['byzantine_count']}-d{faults['byzantine_lo_delay_ms']}-run{run}")


def _prepare_runtime(nodes, addresses):
    if RUNTIME_DIR.exists():
        shutil.rmtree(RUNTIME_DIR)
    key_dir = RUNTIME_DIR / "keys"
    key_dir.mkdir(parents=True)
    subprocess.run(
        [str(_binary("autig-keygen")), "-replicas", str(nodes), "-out", str(key_dir)],
        check=True,
    )
    config = {
        "nodes": {str(i): address for i, address in enumerate(addresses)},
        "epoch": 1,
        "leader_id": 0,
        "replica_public_keys": {
            str(i): f"keys/replica_{i}_public.pem" for i in range(nodes)
        },
        "replica_private_keys": {
            str(i): f"keys/replica_{i}_private.pem" for i in range(nodes)
        },
        "leader_public_key": "keys/leader_public.pem",
        "leader_private_key": "keys/leader_private.pem",
        "genesis_state_id": GENESIS,
        "genesis_fragment_digest": GENESIS,
    }
    with (RUNTIME_DIR / "config.json").open("w", encoding="utf-8") as target:
        dump(config, target, indent=2)


def _command(parameters, node_ids, config="config.json", binary=None):
    binary = str(binary or _binary("autig"))
    ids = ",".join(str(i) for i in node_ids)
    faults = _fault_parameters(parameters)
    command = [
        binary,
        "-config", config,
        "-nodes", ids,
        "-f", str(parameters["faults"]),
        "-byzantine-count", str(faults["byzantine_count"]),
        "-byzantine-lo-delay", str(faults["byzantine_lo_delay_ms"]) + "ms",
        "-gamma", str(parameters["gamma"]),
        "-lo-interval", str(parameters["lo_interval"]),
        "-lo-size", str(parameters["lo_size"]),
        "-tx-rate", str(parameters["rate"]),
        "-tx-size", str(parameters["tx_size"]),
        "-sim-duration", str(parameters["duration"]),
    ]
    if parameters.get("stage_timing", False):
        command.append("-stage-timing")
    if parameters.get("cpuprofile", False):
        command.append("-cpuprofile")
    return command


def _duration_ms(value):
    if value == "N/A":
        return None
    units = {"ns": 0.000001, "us": 0.001, "µs": 0.001, "ms": 1, "s": 1000, "m": 60000, "h": 3600000}
    parts = re.findall(r"([0-9.]+)(ns|us|µs|ms|s|m|h)", value)
    if parts and "".join(number + unit for number, unit in parts) == value:
        return sum(float(number) * units[unit] for number, unit in parts)
    raise RuntimeError(f"unknown Go duration {value!r}")


def _parse_build(text):
    match = re.search(r"BENCHMARK BUILD revision=(\S+) modified=(\S+) go=(\S+)", text)
    if match is None:
        return {"git_commit": None, "git_modified": None, "go_version": None}
    revision, modified, version = match.groups()
    return {
        "git_commit": None if revision == "unknown" else revision,
        "git_modified": {"true": True, "false": False}.get(modified),
        "go_version": None if version == "unknown" else version,
    }


def _parse_mechanism(text):
    nodes = {}
    for raw in re.findall(r"BENCHMARK MECHANISM (\{[^\n]+\})", text):
        record = loads(raw)
        key = str(record["replica"])
        if key in nodes:
            raise RuntimeError(f"duplicate mechanism report for replica {key}")
        nodes[key] = record
    return {
        "mechanism_nodes": nodes,
        "cpu_processes": [loads(raw) for raw in re.findall(r"BENCHMARK CPU (\{[^\n]+\})", text)],
    }


def _mechanism_summary(metrics, nodes):
    reports = metrics.get("mechanism_nodes", {})
    if not reports:  # Preserve support for old benchmark logs.
        return None
    if set(reports) != {str(i) for i in range(nodes)}:
        raise RuntimeError("missing replica mechanism reports")
    if any(r["window_seconds"] <= 0 for r in reports.values()):
        raise RuntimeError("invalid mechanism measurement window")
    def count(name):
        return sum(r["counts"].get(name, 0) for r in reports.values())
    def rate(name):
        return sum(r["counts"].get(name, 0) / r["window_seconds"] for r in reports.values())
    def sample(name, key):
        return sum(r["samples"].get(name, {}).get(key, 0) for r in reports.values())
    def ratio(a, b):
        return a / b if b else None
    leader = reports["0"]  # The harness explicitly configures leader_id=0.
    construct = leader["samples"].get("construct_wall_ns", {})
    fragments = leader["counts"].get("committed_fragments", 0)
    traffic = {}
    for kind in ("local_order", "protocol", "transaction"):
        prefix = "network_" + kind
        traffic[kind] = {
            "messages": count(prefix + "_messages"),
            "messages_per_second": rate(prefix + "_messages"),
            "bytes": count(prefix + "_bytes"),
            "bytes_per_second": rate(prefix + "_bytes"),
            "bytes_per_committed_tx": ratio(count(prefix + "_bytes"), metrics["finalized"]),
            "failed_sends": count(prefix + "_failures"),
        }
    cpu = metrics.get("cpu_processes", [])
    cpu_ids = [str(i) for p in cpu for i in p["replicas"]]
    if sorted(cpu_ids) != sorted(reports):
        raise RuntimeError("missing or duplicate process CPU reports")
    cpu_seconds = None if any(p.get("cpu_seconds") is None for p in cpu) else sum(p["cpu_seconds"] for p in cpu)
    return {
        "lo_fresh": count("lo_fresh"),
        "lo_fresh_per_second": rate("lo_fresh"),
        "lo_retransmit_attempts": count("lo_retransmit_attempts"),
        "lo_retransmit_attempts_per_second": rate("lo_retransmit_attempts"),
        "lo_signatures": count("lo_signatures"),
        "lo_signatures_per_second": rate("lo_signatures"),
        "lo_fresh_ids_mean": ratio(sample("lo_fresh_ids", "sum"), sample("lo_fresh_ids", "count")),
        "receipt_queue_peak": max(r["samples"].get("receipt_queue", {}).get("max", 0) for r in reports.values()),
        "receipt_queue_at_cutoff": {i: r["samples"].get("receipt_queue", {}).get("last", 0) for i, r in reports.items()},
        "committed_fragments": fragments,
        "committed_fragments_per_second": fragments / leader["window_seconds"],
        "fragment_output_tx_mean": ratio(leader["samples"].get("fragment_output_transactions", {}).get("sum", 0), fragments),
        "construct_completed": construct.get("count", 0),
        "construct_per_second": construct.get("count", 0) / leader["window_seconds"],
        "construct_success": leader["counts"].get("construct_success", 0),
        "construct_wall_total_ms": construct.get("sum", 0) / 1e6,
        "construct_wall_mean_ms": ratio(construct.get("sum", 0) / 1e6, construct.get("count", 0)),
        "network": traffic,
        "cpu_seconds": cpu_seconds,
        "cpu_ms_per_committed_tx": ratio(cpu_seconds * 1000, metrics["finalized"]) if cpu_seconds is not None else None,
    }


def _parse_log(path):
    text = Path(path).read_text(encoding="utf-8", errors="replace")
    if any(marker in text for marker in ("Verification FAILED", "BENCHMARK INVALID", "Order leader rejected", "panic:")):
        raise RuntimeError(f"{path} reports a failed benchmark")
    patterns = {
        "measurement_duration": r"Measurement Duration:\s*(\S+)",
        "submitted": r"Total Submitted:\s*(\d+)",
        "finalized": r"Total Finalized:\s*(\d+)",
        "tps": r"Average TPS:\s*([0-9.]+)",
        "offered_rate": r"Actual Offered Rate:\s*([0-9.]+)",
        "failed_send_attempts": r"Locally Failed Transaction Send Attempts:\s*(\d+)",
        "latency": r"Mean Completed-Transaction Latency:\s*(\S+)",
        "latency_samples": r"Latency Samples:\s*(\d+)",
        "outstanding": r"Outstanding:\s*(\d+)",
        "completion_ratio": r"Completion Ratio:\s*([0-9.]+)",
    }
    matches = {name: re.search(pattern, text) for name, pattern in patterns.items()}
    missing = [name for name, match in matches.items() if match is None]
    if missing:
        raise RuntimeError(f"{path} is missing final metrics: {', '.join(missing)}")
    metrics = {
        **_parse_build(text),
        **_parse_mechanism(text),
        "measurement_duration_ms": _duration_ms(matches["measurement_duration"].group(1)),
        "submitted": int(matches["submitted"].group(1)),
        "finalized": int(matches["finalized"].group(1)),
        "average_tps": float(matches["tps"].group(1)),
        "actual_offered_rate": float(matches["offered_rate"].group(1)),
        "locally_failed_transaction_send_attempts": int(matches["failed_send_attempts"].group(1)),
        "mean_completed_transaction_latency_ms": _duration_ms(matches["latency"].group(1)),
        "latency_samples": int(matches["latency_samples"].group(1)),
        "outstanding": int(matches["outstanding"].group(1)),
        "completion_ratio": float(matches["completion_ratio"].group(1)),
        "locally_failed_local_order_send_attempts": text.count("BENCHMARK LOCAL SEND FAILURE: LocalOrder"),
        "locally_failed_autig_candidate_send_attempts": text.count("BENCHMARK LOCAL SEND FAILURE: AUTIGCandidate"),
        "locally_failed_benchmark_commit_send_attempts": text.count("BENCHMARK LOCAL SEND FAILURE: BenchmarkAUTIGCommit"),
        "replica_states": {
            replica: (seq, state, digest)
            for replica, seq, state, digest in re.findall(
                r"BENCHMARK STATE replica=(\d+) seq=(\d+) state=([0-9a-f]{64}) fragment=([0-9a-f]{64})", text
            )
        },
    }
    return metrics


def _parse_run_logs(paths):
    missing = [str(path) for path in paths if not path.is_file()]
    if missing:
        raise RuntimeError("missing node logs: " + ", ".join(missing))
    metrics = _parse_log(paths[0])
    metrics.update(replica_states={}, replica_builds={}, mechanism_nodes={}, cpu_processes=[])
    failure_markers = {
        "locally_failed_local_order_send_attempts": "BENCHMARK LOCAL SEND FAILURE: LocalOrder",
        "locally_failed_autig_candidate_send_attempts": "BENCHMARK LOCAL SEND FAILURE: AUTIGCandidate",
        "locally_failed_benchmark_commit_send_attempts": "BENCHMARK LOCAL SEND FAILURE: BenchmarkAUTIGCommit",
    }
    for key in failure_markers:
        metrics[key] = 0
    for replica_id, path in enumerate(paths):
        text = path.read_text(encoding="utf-8", errors="replace")
        if any(marker in text for marker in ("Verification FAILED", "BENCHMARK INVALID", "Order leader rejected", "panic:")):
            raise RuntimeError(f"{path} reports a failed benchmark")
        metrics["replica_builds"][str(replica_id)] = _parse_build(text)
        mechanism = _parse_mechanism(text)
        if set(metrics["mechanism_nodes"]) & set(mechanism["mechanism_nodes"]):
            raise RuntimeError("duplicate replica mechanism reports across logs")
        metrics["mechanism_nodes"].update(mechanism["mechanism_nodes"])
        metrics["cpu_processes"].extend(mechanism["cpu_processes"])
        metrics["replica_states"].update({
            replica: (seq, state, digest)
            for replica, seq, state, digest in re.findall(
                r"BENCHMARK STATE replica=(\d+) seq=(\d+) state=([0-9a-f]{64}) fragment=([0-9a-f]{64})", text
            )
        })
        for key, marker in failure_markers.items():
            metrics[key] += text.count(marker)
    states = metrics["replica_states"]
    if set(states) != {str(i) for i in range(len(paths))} or len(set(states.values())) != 1:
        raise RuntimeError("replicas did not report the same final committed sequence, state and fragment")
    return metrics


def _write_result(mode, parameters, run, metrics):
    states = metrics["replica_states"]
    if set(states) != {str(i) for i in range(parameters["nodes"])} or len(set(states.values())) != 1:
        raise RuntimeError("replicas did not report the same final committed sequence, state and fragment")
    cutoff_seconds = metrics["measurement_duration_ms"] / 1000
    configured_rate = parameters["rate"]
    actual_rate = metrics["submitted"] / cutoff_seconds
    tolerance = parameters["offered_rate_tolerance"]
    if configured_rate == 0:
        # A nonzero observation has no relative deviation from a zero target.
        # Use JSON null rather than Infinity, and flag the unexpected workload.
        relative_deviation = 0.0 if actual_rate == 0 else None
        rate_within_tolerance = actual_rate == 0
    else:
        relative_deviation = abs(actual_rate - configured_rate) / configured_rate
        rate_within_tolerance = relative_deviation <= tolerance
    metrics["average_tps"] = metrics["finalized"] / cutoff_seconds
    metrics["actual_offered_rate"] = actual_rate
    metrics["configured_offered_rate"] = configured_rate
    metrics["offered_rate_relative_deviation"] = relative_deviation
    metrics["offered_rate_tolerance"] = tolerance
    metrics["offered_rate_within_tolerance"] = rate_within_tolerance
    mechanism = _mechanism_summary(metrics, parameters["nodes"])
    if mechanism is None and "mechanism_nodes" in metrics:
        raise RuntimeError(
            "No BENCHMARK MECHANISM records were found. Update the controller fabfile.py "
            "and rebuild/deploy the node binary; stage_timing/cpuprofile are not required. "
            "Raw logs have been retained."
        )
    if mechanism is not None:
        metrics["mechanism"] = mechanism
    if metrics["locally_failed_transaction_send_attempts"] != 0:
        raise RuntimeError(
            f"benchmark reports {metrics['locally_failed_transaction_send_attempts']} "
            "locally failed transaction send attempts"
        )
    for key in (
        "locally_failed_local_order_send_attempts",
        "locally_failed_autig_candidate_send_attempts",
        "locally_failed_benchmark_commit_send_attempts",
    ):
        if metrics[key] != 0:
            raise RuntimeError(f"benchmark reports {metrics[key]} {key}")
    RESULT_DIR.mkdir(parents=True, exist_ok=True)
    timestamp = datetime.now(timezone.utc)
    faults = _fault_parameters(parameters)
    result = {
        "mode": mode,
        "timestamp": timestamp.isoformat(),
        **_controller_identity(),
        "run": run,
        **parameters,
        **faults,
        **metrics,
    }
    filename = (
        f"{mode}-n{parameters['nodes']}-f{parameters['faults']}"
        f"-b{faults['byzantine_count']}-d{faults['byzantine_lo_delay_ms']}"
        f"-r{parameters['rate']}-run{run}-{timestamp.strftime('%Y%m%dT%H%M%SZ')}.json"
    )
    with (RESULT_DIR / filename).open("w", encoding="utf-8") as target:
        dump(result, target, indent=2)
    if not rate_within_tolerance:
        deviation = "undefined (zero configured rate)" if relative_deviation is None else f"{relative_deviation:.2%}"
        print(
            f"WARNING: actual offered rate {actual_rate:.2f} differs from configured "
            f"rate {configured_rate}; relative deviation {deviation}, tolerance "
            f"{tolerance:.2%}. Saved this run with offered_rate_within_tolerance=false; "
            "use actual_offered_rate for load plots and inspect generator capacity.",
            file=sys.stderr,
        )
    # Keep reproducibility and state-validation data in JSON, without flooding the console.
    _print_metrics(result)
    print(f"Result saved: {RESULT_DIR / filename}")


def _aws_records(settings, states=("running",)):
    records = []
    for region in settings["instances"]["regions"]:
        client = boto3.client("ec2", region_name=region)
        response = client.describe_instances(
            Filters=[
                {"Name": "tag:Name", "Values": [_testbed(settings)]},
                {"Name": "instance-state-name", "Values": list(states)},
            ]
        )
        for reservation in response["Reservations"]:
            for instance in reservation["Instances"]:
                records.append(
                    {
                        "region": region,
                        "id": instance["InstanceId"],
                        "public": instance.get("PublicIpAddress"),
                        "private": instance.get("PrivateIpAddress"),
                        "availability_zone": instance.get("Placement", {}).get("AvailabilityZone"),
                        "instance_type": instance.get("InstanceType"),
                        "vpc_id": instance.get("VpcId"),
                    }
                )
    records.sort(key=lambda record: (record["region"], record["id"]))
    return records


def _spread(records, regions):
    grouped = {region: [] for region in regions}
    for record in records:
        grouped[record["region"]].append(record)
    ordered = []
    offset = 0
    while True:
        added = False
        for region in regions:
            if offset < len(grouped[region]):
                ordered.append(grouped[region][offset])
                added = True
        if not added:
            return ordered
        offset += 1


def _connection(record, settings):
    if not record["public"]:
        raise RuntimeError(f"instance {record['id']} has no public IP")
    return Connection(
        record["public"],
        user=settings.get("user", "ubuntu"),
        connect_kwargs={"key_filename": settings["key"]["path"]},
    )


def _parallel(records, function):
    with ThreadPoolExecutor(max_workers=max(1, len(records))) as executor:
        futures = [executor.submit(function, index, record) for index, record in enumerate(records)]
        for future in futures:
            future.result()


def _require_repo(settings):
    url = settings["repo"].get("url", "").strip()
    if not url:
        raise RuntimeError("settings.json repo.url is empty; set it before fab install/remote")
    return url


def _update_remote(records, settings, install_packages=False):
    url = _require_repo(settings)
    name = settings["repo"]["name"]
    branch = settings["repo"]["branch"]
    if not re.fullmatch(r"[A-Za-z0-9_.-]+", name):
        raise RuntimeError("repo.name contains unsupported shell characters")
    quoted_url, quoted_branch = shlex.quote(url), shlex.quote(branch)

    def update(_, record):
        connection = _connection(record, settings)
        commands = []
        if install_packages:
            commands.extend(
                [
                    "sudo apt-get update",
                    "sudo apt-get install -y build-essential git tmux curl",
                    f"curl -fsSL https://go.dev/dl/go{GO_VERSION}.linux-amd64.tar.gz -o /tmp/autig-go.tgz",
                    "sudo rm -rf /usr/local/go",
                    "sudo tar -C /usr/local -xzf /tmp/autig-go.tgz",
                ]
            )
        commands.extend(
            [
                f"if [ -d {name}/.git ]; then (cd {name} && git fetch origin && git checkout {quoted_branch} && git pull --ff-only origin {quoted_branch}); else git clone --branch {quoted_branch} {quoted_url} {name}; fi",
                f"cd {name} && export PATH=/usr/local/go/bin:$PATH && go build -o autig ./pkg",
            ]
        )
        connection.run(" && ".join(commands), hide=False)

    _parallel(records, update)


def _upload_remote(records, settings, nodes):
    name = settings["repo"]["name"]
    config = RUNTIME_DIR / "config.json"

    def upload(replica_id, record):
        connection = _connection(record, settings)
        remote = f"{name}/.benchmark"
        connection.run(f"rm -rf {remote} && mkdir -p {remote}/keys", hide=True)
        connection.put(str(config), remote=f"{remote}/config.json")
        for other in range(nodes):
            public = RUNTIME_DIR / "keys" / f"replica_{other}_public.pem"
            connection.put(str(public), remote=f"{remote}/keys/{public.name}")
        private = RUNTIME_DIR / "keys" / f"replica_{replica_id}_private.pem"
        connection.put(str(private), remote=f"{remote}/keys/{private.name}")
        leader_public = RUNTIME_DIR / "keys" / "leader_public.pem"
        connection.put(str(leader_public), remote=f"{remote}/keys/{leader_public.name}")
        if replica_id == 0:
            leader_private = RUNTIME_DIR / "keys" / "leader_private.pem"
            connection.put(str(leader_private), remote=f"{remote}/keys/{leader_private.name}")
        connection.run(f"chmod 600 {remote}/keys/*_private.pem", hide=True)

    _parallel(records, upload)


def _run_remote_once(records, settings, parameters, run):
    name = settings["repo"]["name"]

    def start(replica_id, record):
        connection = _connection(record, settings)
        arguments = _command(parameters, [replica_id], binary="../autig")
        command = " ".join(shlex.quote(value) for value in arguments)
        remote_command = (
            f"tmux kill-session -t autig 2>/dev/null || true; "
            f"rm -f {name}/.benchmark/node.log; "
            f"tmux new-session -d -s autig "
            f"'cd {name}/.benchmark && {command} > node.log 2>&1'"
        )
        connection.run(remote_command, hide=True)

    _parallel(records, start)
    leader = _connection(records[0], settings)
    deadline = time.monotonic() + parameters["duration"] + 60
    finished = False
    while time.monotonic() < deadline:
        result = leader.run(
            f"grep -q -- '--- END FINAL RESULTS ---' {name}/.benchmark/node.log",
            warn=True,
            hide=True,
        )
        if result.ok:
            finished = True
            break
        time.sleep(1)

    exited = False
    while finished and time.monotonic() < deadline:
        running = False
        for record in records:
            result = _connection(record, settings).run(
                "tmux has-session -t autig",
                warn=True,
                hide=True,
            )
            if result.ok:
                running = True
        if not running:
            exited = True
            break
        time.sleep(1)

    LOG_DIR.mkdir(parents=True, exist_ok=True)

    def download(replica_id, record):
        connection = _connection(record, settings)
        local = LOG_DIR / f"{_remote_log_prefix(parameters, run)}-node{replica_id}.log"
        connection.get(f"{name}/.benchmark/node.log", local=str(local))
        if parameters.get("cpuprofile", False) and exited:
            connection.get(
                f"{name}/.benchmark/cpu_profile_nodes_{replica_id}.pprof",
                local=str(local.with_suffix(".cpu.pprof")),
            )

    _parallel(records, download)
    if not finished or not exited:
        def cleanup(_, record):
            _connection(record, settings).run(
                "tmux kill-session -t autig 2>/dev/null || true",
                warn=True,
                hide=True,
            )

        _parallel(records, cleanup)
        raise RuntimeError("remote AUTIG did not exit before the benchmark timeout; logs were downloaded")
    paths = [LOG_DIR / f"{_remote_log_prefix(parameters, run)}-node{i}.log"
             for i in range(parameters["nodes"])]
    metrics = _parse_run_logs(paths)
    metrics["ip_mode"] = "public"  # Actual transport configuration below.
    metrics["replica_instances"] = {str(i): record for i, record in enumerate(records)}
    _write_result("remote", parameters, run, metrics)


@task
def local(ctx):
    """Build and run one local AUTIG benchmark."""
    print("BENCHMARK CONTROLLER " + dumps(_controller_identity()))
    parameters = {
        "nodes": 5,
        "faults": 1,
        "byzantine_count": None,  # None preserves the previous b=f behavior.
        "byzantine_lo_delay_ms": 0,
        "gamma": 0.90,
        "rate": 700,
        "tx_size": 512,
        "lo_interval": 150,
        "lo_size": 200,
        "duration": 20,
        "offered_rate_tolerance": 0.02,
        "stage_timing": False,
        "cpuprofile": False,
    }
    _validate_parameters(parameters)
    settings = _settings()
    _build_local(include_node=True)
    addresses = [f"127.0.0.1:{settings['port'] + i}" for i in range(parameters["nodes"])]
    _prepare_runtime(parameters["nodes"], addresses)
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    log = LOG_DIR / (_remote_log_prefix(parameters, 1).replace("remote-", "local-", 1) + ".log")
    with log.open("w", encoding="utf-8") as output:
        completed = subprocess.run(
            _command(parameters, range(parameters["nodes"])),
            cwd=RUNTIME_DIR,
            stdout=output,
            stderr=subprocess.STDOUT,
            timeout=parameters["duration"] + 45,
            check=False,
        )
    if completed.returncode != 0:
        raise RuntimeError(f"local AUTIG exited with status {completed.returncode}; see {log}")
    _write_result("local", parameters, 1, _parse_log(log))


@task
def remote(ctx):
    """Run the benchmark matrix on existing AWS instances."""
    print("BENCHMARK CONTROLLER " + dumps(_controller_identity()))
    matrix = {
        "faults": 1,
        "byzantine_count": [None],  # e.g. [0, 1, 2] with fixed faults=2, nodes=[10].
        "byzantine_lo_delay_ms": 0,  # Same per-node delay at every b; zero disables it.
        "nodes": [5],
        "rate": [700],
        "gamma": 0.90,
        "tx_size": 512,
        "lo_interval": 150,
        "lo_size": 200,
        "duration": 60,
        "runs": 1,
        "offered_rate_tolerance": 0.02,
        "stage_timing": False,
        "cpuprofile": False,
    }
    for nodes, rate, count in product(matrix["nodes"], matrix["rate"], matrix["byzantine_count"]):
        _validate_parameters({**matrix, "nodes": nodes, "rate": rate, "byzantine_count": count})
    settings = _settings()
    _require_repo(settings)
    available = _spread(
        _aws_records(settings), settings["instances"]["regions"]
    )
    required = max(matrix["nodes"])
    if len(available) < required:
        raise RuntimeError(f"need {required} running AWS instances, found {len(available)}")
    selected = available[:required]
    _update_remote(selected, settings, install_packages=False)
    _build_local(include_node=False)
    for nodes in matrix["nodes"]:
        records = selected[:nodes]
        addresses = [f"{record['public']}:{settings['port']}" for record in records]
        _prepare_runtime(nodes, addresses)
        _upload_remote(records, settings, nodes)
        for rate, byzantine_count in product(matrix["rate"], matrix["byzantine_count"]):
            parameters = {
                "nodes": nodes,
                "faults": matrix["faults"],
                "byzantine_count": byzantine_count,
                "byzantine_lo_delay_ms": matrix["byzantine_lo_delay_ms"],
                "gamma": matrix["gamma"],
                "rate": rate,
                "tx_size": matrix["tx_size"],
                "lo_interval": matrix["lo_interval"],
                "lo_size": matrix["lo_size"],
                "duration": matrix["duration"],
                "offered_rate_tolerance": matrix["offered_rate_tolerance"],
                "stage_timing": matrix["stage_timing"],
                "cpuprofile": matrix["cpuprofile"],
            }
            _validate_parameters(parameters)
            for run in range(1, matrix["runs"] + 1):
                _run_remote_once(records, settings, parameters, run)


@task
def install(ctx):
    """Install Go/tooling and clone/build AUTIG on all running instances."""
    settings = _settings()
    records = _aws_records(settings)
    if not records:
        raise RuntimeError("no running AUTIG instances")
    _update_remote(records, settings, install_packages=True)


@task
def kill(ctx):
    """Stop benchmark tmux sessions on all running instances."""
    settings = _settings()
    records = _aws_records(settings)

    def stop(_, record):
        _connection(record, settings).run(
            "tmux kill-session -t autig 2>/dev/null || true", hide=True
        )

    _parallel(records, stop)


@task
def logs(ctx):
    """Reparse local logs, grouping remote replicas without rerunning AWS."""
    for path in sorted(LOG_DIR.glob("*.log")):
        remote = re.fullmatch(r"(remote-n(\d+)-r\d+(?:-f\d+-b\d+-d\d+)?-run\d+)-node(\d+)\.log", path.name)
        if remote and remote.group(3) != "0":
            continue
        try:
            print(path.name)
            if remote:
                nodes = int(remote.group(2))
                paths = [path.with_name(f"{remote.group(1)}-node{i}.log") for i in range(nodes)]
                metrics = _parse_run_logs(paths)
            else:
                metrics = _parse_log(path)
                nodes = len(metrics["replica_states"])
            summary = _mechanism_summary(metrics, nodes)
            if summary is not None:
                metrics["mechanism"] = summary
            else:
                print("  WARNING: no mechanism records in these logs; resource costs cannot be recovered.")
            _print_metrics(metrics)
        except RuntimeError as error:
            print(f"  skipped: {error}")


def _security_group(client, settings):
    vpcs = client.describe_vpcs(Filters=[{"Name": "isDefault", "Values": ["true"]}])["Vpcs"]
    if not vpcs:
        raise RuntimeError("AWS region has no default VPC")
    vpc = vpcs[0]["VpcId"]
    name = _testbed(settings)
    groups = client.describe_security_groups(
        Filters=[
            {"Name": "group-name", "Values": [name]},
            {"Name": "vpc-id", "Values": [vpc]},
        ]
    )["SecurityGroups"]
    if groups:
        group = groups[0]
    else:
        group = client.create_security_group(
            GroupName=name, Description="AUTIG benchmark", VpcId=vpc
        )
    permissions = [
        {
            "IpProtocol": "tcp",
            "FromPort": port,
            "ToPort": port,
            "IpRanges": [{"CidrIp": "0.0.0.0/0"}],
        }
        for port in (22, settings["port"])
    ]
    for permission in permissions:
        try:
            client.authorize_security_group_ingress(
                GroupId=group["GroupId"], IpPermissions=[permission]
            )
        except ClientError as error:
            if error.response["Error"]["Code"] != "InvalidPermission.Duplicate":
                raise
    return group["GroupId"]


def _ubuntu_ami(client):
    images = client.describe_images(
        Owners=["099720109477"],
        Filters=[
            {
                "Name": "name",
                "Values": ["ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server-*"],
            },
            {"Name": "architecture", "Values": ["x86_64"]},
            {"Name": "state", "Values": ["available"]},
        ],
    )["Images"]
    if not images:
        raise RuntimeError("no Ubuntu 22.04 AMI found")
    return max(images, key=lambda image: image["CreationDate"])["ImageId"]


@task
def create(ctx, nodes=1):
    """Create `nodes` instances in each configured AWS region."""
    settings = _settings()
    nodes = int(nodes)
    if nodes <= 0:
        raise RuntimeError("nodes must be positive")
    for region in settings["instances"]["regions"]:
        client = boto3.client("ec2", region_name=region)
        group = _security_group(client, settings)
        client.run_instances(
            ImageId=_ubuntu_ami(client),
            InstanceType=settings["instances"]["type"],
            KeyName=settings["key"]["name"],
            MinCount=nodes,
            MaxCount=nodes,
            SecurityGroupIds=[group],
            TagSpecifications=[
                {
                    "ResourceType": "instance",
                    "Tags": [{"Key": "Name", "Value": _testbed(settings)}],
                }
            ],
        )
        print(f"requested {nodes} instance(s) in {region}")


@task
def info(ctx):
    """Print AWS instance and SSH information."""
    settings = _settings()
    for record in _aws_records(settings, states=("pending", "running", "stopped")):
        print(
            f"{record['region']} {record['id']} public={record['public']} "
            f"private={record['private']}"
        )


@task
def stop(ctx):
    """Stop all running benchmark instances."""
    settings = _settings()
    records = _aws_records(settings)
    for region in settings["instances"]["regions"]:
        client = boto3.client("ec2", region_name=region)
        ids = [
            record["id"]
            for record in records
            if record["region"] == region
        ]
        if ids:
            client.stop_instances(InstanceIds=ids)


@task
def start(ctx):
    """Start all stopped benchmark instances."""
    settings = _settings()
    records = _aws_records(settings, states=("stopped",))
    for region in settings["instances"]["regions"]:
        ids = [record["id"] for record in records if record["region"] == region]
        if ids:
            boto3.client("ec2", region_name=region).start_instances(InstanceIds=ids)


@task
def destroy(ctx):
    """Terminate all benchmark instances."""
    settings = _settings()
    records = _aws_records(
        settings, states=("pending", "running", "stopping", "stopped")
    )
    for region in settings["instances"]["regions"]:
        ids = [record["id"] for record in records if record["region"] == region]
        if ids:
            boto3.client("ec2", region_name=region).terminate_instances(InstanceIds=ids)
