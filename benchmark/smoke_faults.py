import contextlib
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time

root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(root / "benchmark"))
import fabfile

work = Path(tempfile.mkdtemp(prefix="utig-faults-smoke-"))
binary = work / "autig.exe"
keygen = work / "keygen.exe"
subprocess.run(["go", "build", "-o", str(binary), "./pkg"], cwd=root, check=True)
subprocess.run(["go", "build", "-o", str(keygen), "./cmd/autig-keygen"], cwd=root, check=True)
subprocess.run([str(keygen), "-replicas", "10", "-out", str(work / "keys")], check=True)
fabfile.RESULT_DIR = work / "results"
cases = [("legacy", None, 0, 4), ("b0", 0, 0, 4), ("b1", 1, 0, 4),
         ("b2", 2, 0, 4), ("delay-b0", 0, 200, 4),
         ("delay-b1", 1, 200, 4), ("delay-b2", 2, 200, 4),
         ("cancel-long-delay", 2, 3600000, 1)]
summaries = []
for run, (label, count, delay, duration) in enumerate(cases, 1):
    sockets = [socket.socket() for _ in range(10)]
    for sock in sockets:
        sock.bind(("127.0.0.1", 0))
    config = {
        "nodes": {str(i): f"127.0.0.1:{s.getsockname()[1]}" for i, s in enumerate(sockets)},
        "epoch": 1, "leader_id": 0,
        "replica_public_keys": {str(i): f"keys/replica_{i}_public.pem" for i in range(10)},
        "replica_private_keys": {str(i): f"keys/replica_{i}_private.pem" for i in range(10)},
        "leader_public_key": "keys/leader_public.pem", "leader_private_key": "keys/leader_private.pem",
        "genesis_state_id": "00" * 32, "genesis_fragment_digest": "00" * 32,
    }
    (work / "config.json").write_text(json.dumps(config), encoding="utf-8")
    parameters = dict(nodes=10, faults=2, gamma=1, byzantine_count=count,
                      byzantine_lo_delay_ms=delay, lo_interval=25, lo_size=40,
                      rate=100, tx_size=512, duration=duration, offered_rate_tolerance=.02)
    fabfile._validate_parameters(parameters)
    for sock in sockets:
        sock.close()
    paths = [work / f"{label}-node{i}.log" for i in range(10)]
    handles = [p.open("w", encoding="utf-8") for p in paths]
    processes = []
    start = time.monotonic()
    try:
        for i, handle in enumerate(handles):
            command = fabfile._command(parameters, [i], binary=binary)
            if label == "legacy":
                for flag in ["-byzantine-count", "-byzantine-lo-delay"]:
                    index = command.index(flag)
                    del command[index:index+2]
            processes.append(subprocess.Popen(command, cwd=work, stdout=handle, stderr=subprocess.STDOUT,
                                              creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0))
        for process in processes:
            assert process.wait(timeout=30) == 0
    finally:
        for process in processes:
            if process.poll() is None:
                process.kill()
                process.wait()
        for handle in handles:
            handle.close()
    wall = time.monotonic() - start
    metrics = fabfile._parse_run_logs(paths)
    with contextlib.redirect_stdout(io.StringIO()):
        fabfile._write_result("faults-smoke", parameters, run, metrics)
    actual = 2 if count is None else count
    for i, path in enumerate(paths):
        log = path.read_text(encoding="utf-8")
        assert f"BENCHMARK FAULTS tolerated=2 byzantine={actual}" in log
        assert (f"Node {i} is starting as a MALICIOUS replica." in log) == (i >= 10-actual)
        counts = metrics["mechanism_nodes"][str(i)]["counts"]
        assert (counts.get("byzantine_lo_delay_attempts", 0) > 0) == (i >= 10-actual and delay > 0)
    if label == "cancel-long-delay":
        assert wall < 10, wall
        assert metrics["finalized"] == 0
        assert all(node["counts"].get("byzantine_lo_delay_completed", 0) == 0
                   for node in metrics["mechanism_nodes"].values())
    else:
        assert metrics["finalized"] > 0
    summary = dict(case=label, b=actual, delay_ms=delay, tps=metrics["average_tps"],
                   latency_ms=metrics["mean_completed_transaction_latency_ms"],
                   completion_ratio=metrics["completion_ratio"], wall_seconds=round(wall, 2),
                   replicas_agree=True)
    summaries.append(summary)
    print(json.dumps(summary), flush=True)
(work / "summary.json").write_text(json.dumps(summaries, indent=2), encoding="utf-8")
print(f"Smoke artifacts: {work}", flush=True)
