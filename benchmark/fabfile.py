from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from json import dump, dumps, load
from math import ceil
from pathlib import Path
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
    return [
        binary,
        "-config", config,
        "-nodes", ids,
        "-f", str(parameters["faults"]),
        "-gamma", str(parameters["gamma"]),
        "-lo-interval", str(parameters["lo_interval"]),
        "-lo-size", str(parameters["lo_size"]),
        "-tx-rate", str(parameters["rate"]),
        "-tx-size", str(parameters["tx_size"]),
        "-sim-duration", str(parameters["duration"]),
    ]


def _duration_ms(value):
    if value == "N/A":
        return None
    units = {"ns": 0.000001, "us": 0.001, "µs": 0.001, "ms": 1, "s": 1000, "m": 60000, "h": 3600000}
    parts = re.findall(r"([0-9.]+)(ns|us|µs|ms|s|m|h)", value)
    if parts and "".join(number + unit for number, unit in parts) == value:
        return sum(float(number) * units[unit] for number, unit in parts)
    raise RuntimeError(f"unknown Go duration {value!r}")


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
    result = {
        "mode": mode,
        "timestamp": timestamp.isoformat(),
        "run": run,
        **parameters,
        **metrics,
    }
    filename = (
        f"{mode}-n{parameters['nodes']}-f{parameters['faults']}"
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
    print(dumps(result, indent=2))


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
        local = LOG_DIR / (
            f"remote-n{parameters['nodes']}-r{parameters['rate']}"
            f"-run{run}-node{replica_id}.log"
        )
        connection.get(f"{name}/.benchmark/node.log", local=str(local))

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
    leader_log = LOG_DIR / (
        f"remote-n{parameters['nodes']}-r{parameters['rate']}-run{run}-node0.log"
    )
    metrics = _parse_log(leader_log)
    metrics["locally_failed_local_order_send_attempts"] = 0
    metrics["locally_failed_autig_candidate_send_attempts"] = 0
    metrics["locally_failed_benchmark_commit_send_attempts"] = 0
    for replica_id in range(parameters["nodes"]):
        path = LOG_DIR / (
            f"remote-n{parameters['nodes']}-r{parameters['rate']}"
            f"-run{run}-node{replica_id}.log"
        )
        text = path.read_text(encoding="utf-8", errors="replace")
        if any(marker in text for marker in ("Verification FAILED", "BENCHMARK INVALID", "Order leader rejected", "panic:")):
            raise RuntimeError(f"{path} reports a failed benchmark")
        metrics["replica_states"].update({
            replica: (seq, state, digest)
            for replica, seq, state, digest in re.findall(
                r"BENCHMARK STATE replica=(\d+) seq=(\d+) state=([0-9a-f]{64}) fragment=([0-9a-f]{64})", text
            )
        })
        metrics["locally_failed_local_order_send_attempts"] += text.count("BENCHMARK LOCAL SEND FAILURE: LocalOrder")
        metrics["locally_failed_autig_candidate_send_attempts"] += text.count("BENCHMARK LOCAL SEND FAILURE: AUTIGCandidate")
        metrics["locally_failed_benchmark_commit_send_attempts"] += text.count("BENCHMARK LOCAL SEND FAILURE: BenchmarkAUTIGCommit")
    _write_result("remote", parameters, run, metrics)


@task
def local(ctx):
    """Build and run one local AUTIG benchmark."""
    parameters = {
        "nodes": 5,
        "faults": 1,
        "gamma": 0.90,
        "rate": 700,
        "tx_size": 512,
        "lo_interval": 150,
        "lo_size": 200,
        "duration": 20,
        "offered_rate_tolerance": 0.02,
    }
    _validate_parameters(parameters)
    settings = _settings()
    _build_local(include_node=True)
    addresses = [f"127.0.0.1:{settings['port'] + i}" for i in range(parameters["nodes"])]
    _prepare_runtime(parameters["nodes"], addresses)
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    log = LOG_DIR / "local.log"
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
    matrix = {
        "faults": 1,
        "nodes": [5],
        "rate": [700],
        "gamma": 0.90,
        "tx_size": 512,
        "lo_interval": 150,
        "lo_size": 200,
        "duration": 60,
        "runs": 1,
        "offered_rate_tolerance": 0.02,
    }
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
        for rate in matrix["rate"]:
            parameters = {
                "nodes": nodes,
                "faults": matrix["faults"],
                "gamma": matrix["gamma"],
                "rate": rate,
                "tx_size": matrix["tx_size"],
                "lo_interval": matrix["lo_interval"],
                "lo_size": matrix["lo_size"],
                "duration": matrix["duration"],
                "offered_rate_tolerance": matrix["offered_rate_tolerance"],
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
    """Parse all locally available AUTIG logs."""
    for path in sorted(LOG_DIR.glob("*.log")):
        try:
            print(path.name)
            print(dumps(_parse_log(path), indent=2))
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
