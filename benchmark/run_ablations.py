"""Run local AUTIG ablations in independent, sequential Go test processes.

Standard library only; no AWS provisioning, network workload, or production flags.
"""

import argparse
import csv
import json
import os
import platform
from pathlib import Path
import re
import statistics
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parents[1]


def parse_benchmarks(text):
    rows = []
    for line in text.splitlines():
        if not line.startswith("BenchmarkAblation"):
            continue
        fields = line.split()
        # Benchmark name, iterations, then Go's value/unit pairs.
        if len(fields) < 8 or len(fields) % 2:
            raise ValueError(f"Incomplete benchmark record: {line}")
        name = re.sub(r"-\d+$", "", fields[0])
        metrics = {fields[i + 1]: float(fields[i]) for i in range(2, len(fields), 2)}
        for key in ("ns/op", "B/op", "allocs/op", "new_positions", "pre_live"):
            if key not in metrics:
                raise ValueError(f"Missing {key} in {name}")
        rows.append({"name": name, "iterations": int(fields[1]), "metrics": metrics})
    if not rows:
        raise ValueError("No ablation measurements found")
    return rows


def pair_results(rows, run, order):
    groups = {}
    for row in rows:
        sample, branch = row["name"].rsplit("/", 1)
        branches = groups.setdefault(sample, {})
        if branch in branches:
            raise ValueError(f"Duplicate branch for {sample}; use test.count=1")
        branches[branch] = row
    pairs = []
    for sample, branches in groups.items():
        optimized, baseline = (("Incremental", "Rebuild") if "GraphMaintenance" in sample
                               else ("Certificate", "Recompute"))
        if set(branches) != {optimized, baseline}:
            raise ValueError(f"Unpaired branches for {sample}")
        a, b = branches[optimized]["metrics"], branches[baseline]["metrics"]
        structural = lambda m: {k: v for k, v in m.items() if k not in {"ns/op", "B/op", "allocs/op"}}
        if structural(a) != structural(b):
            raise ValueError(f"Branches measured different sample structures: {sample}")
        if a["ns/op"] <= 0 or b["ns/op"] <= 0:
            raise ValueError(f"Nonpositive measurement: {sample}")
        pairs.append({
            "run": run, "order": order, "sample": sample,
            "optimized": optimized, "baseline": baseline,
            "optimized_ns": a["ns/op"], "baseline_ns": b["ns/op"],
            "speedup": b["ns/op"] / a["ns/op"],
            "optimized_bytes": a["B/op"], "baseline_bytes": b["B/op"],
            "optimized_allocs": a["allocs/op"], "baseline_allocs": b["allocs/op"],
            "sample_metrics": json.dumps(structural(a), sort_keys=True),
        })
    return pairs


def write_csv(path, rows):
    with path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(rows)


def command_text(args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--nodes", default="10,50")
    parser.add_argument("--faults", type=int, default=1)
    parser.add_argument("--gamma", type=float, default=.9)
    parser.add_argument("--seeds", default="1,7,19")
    parser.add_argument("--history", type=int, default=480)
    parser.add_argument("--lo-size", type=int, default=200)
    parser.add_argument("--runs", type=int, default=4, help="Independent processes; normally 3-5")
    parser.add_argument("--benchtime", default="1s", help="Timed work per leaf; use 1x only for smoke tests")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--instance-type", default="unrecorded")
    parser.add_argument("--region", default="unrecorded")
    args = parser.parse_args()
    if args.runs < 1:
        parser.error("--runs must be positive")
    output = (args.output or ROOT / "benchmark" / "results" / ("ablation-" + time.strftime("%Y%m%d-%H%M%S"))).resolve()
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, GOMAXPROCS="1")
    cpu = platform.processor()
    if Path("/proc/cpuinfo").exists():
        cpu = next((line.split(":", 1)[1].strip() for line in Path("/proc/cpuinfo").read_text().splitlines()
                    if line.startswith("model name")), cpu)
    metadata = {
        "commit": command_text(["git", "rev-parse", "HEAD"]),
        "git_status": command_text(["git", "status", "--short"]),
        "go_version": command_text(["go", "version"]),
        "go_env": json.loads(command_text(["go", "env", "-json", "GOOS", "GOARCH", "GOEXPERIMENT"])),
        "platform": platform.platform(), "cpu": cpu, "logical_cpus": os.cpu_count(),
        "gomaxprocs": 1, "GOGC": env.get("GOGC", "default"), "GOMEMLIMIT": env.get("GOMEMLIMIT", "default"),
        "parameters": {k: str(v) if isinstance(v, Path) else v for k, v in vars(args).items()},
        "commands": [],
    }
    metadata_path = output / "metadata.json"
    def execute(command, log):
        metadata["commands"].append(command)
        metadata_path.write_text(json.dumps(metadata, indent=2), encoding="utf-8")
        print(f"Running {log.name}", flush=True)
        with log.open("w", encoding="utf-8") as handle:
            subprocess.run(command, cwd=ROOT, env=env, stdout=handle, stderr=subprocess.STDOUT, check=True)

    # Test first, then compile once. Compilation and fixture preflight are never
    # counted in ns/op. Each run launches a fresh process from this same binary.
    execute(["go", "test", "./...", "-count=1"], output / "correctness.log")
    execute([sys.executable, "-m", "unittest", "discover", "-s", "benchmark", "-p", "test_run_ablations.py"], output / "runner-tests.log")
    binary = output / ("ofo.test.exe" if os.name == "nt" else "ofo.test")
    execute(["go", "test", "-c", "-o", str(binary), "./pkg/ofo"], output / "build.log")
    all_pairs, raw = [], []
    for run in range(1, args.runs + 1):
        order = "AB" if run % 2 else "BA"
        log = output / f"run-{run:02d}-{order}.log"
        command = [str(binary), "-test.run=^$", "-test.bench=^BenchmarkAblation",
                   "-test.benchmem", f"-test.benchtime={args.benchtime}", "-test.count=1", "-test.cpu=1",
                   f"-ablation-nodes={args.nodes}", f"-ablation-f={args.faults}", f"-ablation-gamma={args.gamma}",
                   f"-ablation-seeds={args.seeds}", f"-ablation-history={args.history}", f"-ablation-lo-size={args.lo_size}", f"-ablation-order={order}"]
        execute(command, log)
        rows = parse_benchmarks(log.read_text(encoding="utf-8"))
        raw.extend(dict(row, run=run, order=order) for row in rows)
        all_pairs.extend(pair_results(rows, run, order))
        (output / "measurements.json").write_text(json.dumps(raw, indent=2), encoding="utf-8")
        write_csv(output / "pairs.csv", all_pairs)
    # Keep seeds separate: spread across processes is not sample diversity.
    summaries = []
    for sample in sorted({p["sample"] for p in all_pairs}):
        values = [p for p in all_pairs if p["sample"] == sample]
        summaries.append({
            "sample": sample, "processes": len(values),
            "median_optimized_ns": statistics.median(p["optimized_ns"] for p in values),
            "median_baseline_ns": statistics.median(p["baseline_ns"] for p in values),
            "median_paired_speedup": statistics.median(p["speedup"] for p in values),
            "min_paired_speedup": min(p["speedup"] for p in values),
            "max_paired_speedup": max(p["speedup"] for p in values),
        })
    write_csv(output / "summary.csv", summaries)
    print(f"Results: {output}")


if __name__ == "__main__":
    main()
