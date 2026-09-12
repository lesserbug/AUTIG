import contextlib
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(root / "benchmark"))
import fabfile

work = Path(tempfile.mkdtemp(prefix="mechanism-smoke-", dir=root / "tmp"))
binary = work / "autig.exe"
keygen = work / "keygen.exe"
subprocess.run(["go", "build", "-o", str(binary), "./pkg"], cwd=root, check=True)
subprocess.run(["go", "build", "-o", str(keygen), "./cmd/autig-keygen"], cwd=root, check=True)
subprocess.run([str(keygen), "-replicas", "5", "-out", str(work / "keys")], check=True)
fabfile.RESULT_DIR = work / "results"

for run, interval in enumerate((25, 150), 1):
    sockets = [socket.socket() for _ in range(5)]
    for sock in sockets:
        sock.bind(("127.0.0.1", 0))
    addresses = {str(i): f"127.0.0.1:{s.getsockname()[1]}" for i, s in enumerate(sockets)}
    config = {
        "nodes": addresses, "epoch": 1, "leader_id": 0,
        "replica_public_keys": {str(i): f"keys/replica_{i}_public.pem" for i in range(5)},
        "replica_private_keys": {str(i): f"keys/replica_{i}_private.pem" for i in range(5)},
        "leader_public_key": "keys/leader_public.pem", "leader_private_key": "keys/leader_private.pem",
        "genesis_state_id": "00" * 32, "genesis_fragment_digest": "00" * 32,
    }
    (work / "config.json").write_text(json.dumps(config), encoding="utf-8")
    parameters = dict(nodes=5, faults=1, gamma=0.9, lo_interval=interval, lo_size=400,
                      rate=100, tx_size=512, duration=4, offered_rate_tolerance=.02)
    for sock in sockets:
        sock.close()
    paths = [work / f"i{interval}-n{i}.log" for i in range(5)]
    handles = [p.open("w", encoding="utf-8") for p in paths]
    processes = []
    try:
        for i, handle in enumerate(handles):
            processes.append(subprocess.Popen(fabfile._command(parameters, [i], binary=binary), cwd=work,
                                             stdout=handle, stderr=subprocess.STDOUT,
                                             creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0))
        for process in processes:
            assert process.wait(timeout=45) == 0
    finally:
        for process in processes:
            if process.poll() is None:
                process.kill()
                process.wait()
        for handle in handles:
            handle.close()
    metrics = fabfile._parse_log(paths[0])
    metrics["mechanism_nodes"] = {}
    metrics["cpu_processes"] = []
    for path in paths:
        text = path.read_text(encoding="utf-8")
        assert "BENCHMARK INVALID" not in text
        import re
        metrics["replica_states"].update({replica: (seq, state, digest) for replica, seq, state, digest in re.findall(
            r"BENCHMARK STATE replica=(\d+) seq=(\d+) state=([0-9a-f]{64}) fragment=([0-9a-f]{64})", text)})
        parsed = fabfile._parse_mechanism(text)
        metrics["mechanism_nodes"].update(parsed["mechanism_nodes"])
        metrics["cpu_processes"].extend(parsed["cpu_processes"])
    capture = io.StringIO()
    with contextlib.redirect_stdout(capture):
        fabfile._write_result("smoke", parameters, run, metrics)
    assert "replica_states" not in capture.getvalue()
    summary = metrics["mechanism"]
    assert summary["lo_fresh"] > 0 and summary["lo_signatures"] > 0
    assert summary["network"]["local_order"]["bytes"] > 0
    assert summary["construct_completed"] > 0 and summary["committed_fragments"] > 0
    assert summary["cpu_seconds"] >= 0 and len(metrics["cpu_processes"]) == 5
    print(json.dumps({"interval": interval, "tps": metrics["average_tps"],
                      "fresh": summary["lo_fresh"], "retransmits": summary["lo_retransmit_attempts"],
                      "signatures": summary["lo_signatures"], "fragments": summary["committed_fragments"],
                      "lo_bytes": summary["network"]["local_order"]["bytes"], "cpu_seconds": summary["cpu_seconds"]}))
print(f"Smoke artifacts: {work}")
