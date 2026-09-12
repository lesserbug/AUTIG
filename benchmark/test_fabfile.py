import contextlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import fabfile


class OfferedRateResultTests(unittest.TestCase):
    def setUp(self):
        self.parameters = {
            "nodes": 5, "faults": 1, "gamma": 0.9, "rate": 1000,
            "tx_size": 512, "duration": 20, "offered_rate_tolerance": 0.02,
        }
        self.metrics = {
            "replica_states": {str(i): ("1", "a" * 64, "b" * 64) for i in range(5)},
            "measurement_duration_ms": 20000,
            "submitted": 19223,
            "finalized": 18000,
            "locally_failed_transaction_send_attempts": 0,
            "locally_failed_local_order_send_attempts": 0,
            "locally_failed_autig_candidate_send_attempts": 0,
            "locally_failed_benchmark_commit_send_attempts": 0,
        }

    def write_result(self):
        with tempfile.TemporaryDirectory() as directory:
            warning = io.StringIO()
            console = io.StringIO()
            with patch.object(fabfile, "RESULT_DIR", Path(directory)), contextlib.redirect_stdout(console), contextlib.redirect_stderr(warning):
                fabfile._write_result("local", self.parameters, 1, self.metrics)
            self.console = console.getvalue()
            paths = list(Path(directory).glob("*.json"))
            self.assertEqual(len(paths), 1)
            return json.loads(paths[0].read_text(encoding="utf-8")), warning.getvalue()

    def test_underload_is_saved_with_warning_and_separate_rates(self):
        result, warning = self.write_result()
        self.assertEqual(result["configured_offered_rate"], 1000)
        self.assertEqual(result["actual_offered_rate"], 961.15)
        self.assertEqual(result["average_tps"], 900)
        self.assertAlmostEqual(result["offered_rate_relative_deviation"], 0.03885)
        self.assertFalse(result["offered_rate_within_tolerance"])
        self.assertIn("WARNING", warning)

    def test_saturated_finalization_does_not_fail_rate_check(self):
        self.metrics.update(submitted=19999, finalized=100)
        result, warning = self.write_result()
        self.assertTrue(result["offered_rate_within_tolerance"])
        self.assertEqual(result["average_tps"], 5)
        self.assertEqual(warning, "")

    def test_threshold_boundary_and_excess_load(self):
        for submitted, within in [(19600, True), (19599, False), (20400, True), (20401, False)]:
            with self.subTest(submitted=submitted):
                self.metrics["submitted"] = submitted
                result, warning = self.write_result()
                self.assertEqual(result["offered_rate_within_tolerance"], within)
                self.assertEqual(bool(warning), not within)

    def test_zero_target_is_json_safe(self):
        self.parameters["rate"] = 0
        self.metrics["finalized"] = 0
        for submitted in [0, 1]:
            with self.subTest(submitted=submitted):
                self.metrics["submitted"] = submitted
                result, warning = self.write_result()
                self.assertEqual(result["offered_rate_within_tolerance"], submitted == 0)
                self.assertEqual(result["offered_rate_relative_deviation"], 0.0 if submitted == 0 else None)
                self.assertEqual(bool(warning), submitted != 0)

    def test_send_failures_still_reject_results(self):
        for key in [key for key in self.metrics if key.startswith("locally_failed_")]:
            with self.subTest(key=key):
                with patch.dict(self.metrics, {key: 1}), self.assertRaisesRegex(RuntimeError, "locally.failed"):
                    self.write_result()

    def test_inconsistent_replica_states_still_reject_results(self):
        self.metrics["replica_states"]["4"] = ("2", "c" * 64, "d" * 64)
        with self.assertRaisesRegex(RuntimeError, "same final committed"):
            self.write_result()

    def test_console_omits_state_and_addresses_but_saved_result_retains_them(self):
        self.metrics["replica_instances"] = {"0": {"public_ip": "192.0.2.1"}}
        result, _ = self.write_result()
        self.assertNotIn("replica_states", self.console)
        self.assertNotIn("192.0.2.1", self.console)
        self.assertIn("replica_states", result)
        self.assertEqual(result["replica_instances"]["0"]["public_ip"], "192.0.2.1")

    def test_live_result_rejects_missing_mechanism_records(self):
        self.metrics.update(mechanism_nodes={}, cpu_processes=[])
        with self.assertRaisesRegex(RuntimeError, "rebuild/deploy"):
            self.write_result()


class DiagnosticMetadataTests(unittest.TestCase):
    def test_build_identity_is_from_binary_log_and_unknown_is_explicit(self):
        self.assertEqual(fabfile._parse_build("old log")["git_commit"], None)
        result = fabfile._parse_build("BENCHMARK BUILD revision=abc123 modified=true go=go1.22.12")
        self.assertEqual(result, {"git_commit": "abc123", "git_modified": True, "go_version": "go1.22.12"})
        self.assertIsNone(fabfile._parse_build("BENCHMARK BUILD revision=unknown modified=unknown go=go1.24.5")["git_modified"])

    def test_diagnostics_are_opt_in(self):
        parameters = dict(faults=1, gamma=.9, lo_interval=150, lo_size=200, rate=500, tx_size=512, duration=60)
        command = fabfile._command(parameters, [0], binary="autig")
        self.assertNotIn("-stage-timing", command)
        self.assertNotIn("-cpuprofile", command)
        parameters.update(stage_timing=True, cpuprofile=True)
        enabled = fabfile._command(parameters, [0], binary="autig")
        self.assertEqual(enabled, command + ["-stage-timing", "-cpuprofile"])


class MechanismTests(unittest.TestCase):
    def records(self):
        return {
            "finalized": 100,
            "mechanism_nodes": {
                str(i): {
                    "replica": i, "window_seconds": 10,
                    "counts": {"lo_fresh": 5, "lo_retransmit_attempts": 10,
                               "lo_signatures": 6, "committed_fragments": 5,
                               "network_local_order_messages": 15, "network_local_order_bytes": 1000},
                    "samples": {"lo_fresh_ids": {"count": 5, "sum": 100},
                                "fragment_output_transactions": {"count": 5, "sum": 100},
                                "receipt_queue": {"max": 40, "last": 3},
                                **({"construct_wall_ns": {"count": 5, "sum": 10000000}} if i == 0 else {})},
                } for i in range(2)
            },
            "cpu_processes": [{"replicas": [0, 1], "sample_seconds": 10, "cpu_seconds": 2}],
        }

    def test_aggregate_uses_unique_leader_commits_and_process_cpu(self):
        result = fabfile._mechanism_summary(self.records(), 2)
        self.assertEqual(result["lo_fresh"], 10)
        self.assertEqual(result["lo_fresh_per_second"], 1)
        self.assertEqual(result["lo_signatures"], 12)
        self.assertEqual(result["lo_fresh_ids_mean"], 20)
        self.assertEqual(result["committed_fragments"], 5)
        self.assertEqual(result["construct_wall_mean_ms"], 2)
        self.assertEqual(result["network"]["local_order"]["bytes_per_committed_tx"], 20)
        self.assertEqual(result["cpu_ms_per_committed_tx"], 20)

    def test_missing_reports_and_zero_completions(self):
        records = self.records()
        records["finalized"] = 0
        self.assertIsNone(fabfile._mechanism_summary(records, 2)["cpu_ms_per_committed_tx"])
        records["cpu_processes"].append(records["cpu_processes"][0])
        with self.assertRaisesRegex(RuntimeError, "CPU reports"):
            fabfile._mechanism_summary(records, 2)
        del records["mechanism_nodes"]["1"]
        with self.assertRaisesRegex(RuntimeError, "mechanism reports"):
            fabfile._mechanism_summary(records, 2)

    def test_parse_and_reject_duplicate_report(self):
        records = self.records()
        text = "\n".join("BENCHMARK MECHANISM " + json.dumps(r) for r in records["mechanism_nodes"].values())
        text += "\nBENCHMARK CPU " + json.dumps(records["cpu_processes"][0])
        parsed = fabfile._parse_mechanism(text)
        self.assertEqual(parsed["mechanism_nodes"], records["mechanism_nodes"])
        self.assertEqual(parsed["cpu_processes"], records["cpu_processes"])
        with self.assertRaisesRegex(RuntimeError, "duplicate"):
            fabfile._parse_mechanism(text + "\n" + text)

    def test_logs_recovers_all_replicas_and_hides_state(self):
        records = self.records()
        with tempfile.TemporaryDirectory() as directory:
            paths = [Path(directory) / f"remote-n2-r100-run1-node{i}.log" for i in range(2)]
            for i, path in enumerate(paths):
                path.write_text(
                    f"BENCHMARK STATE replica={i} seq=5 state={'a' * 64} fragment={'b' * 64}\n"
                    + "BENCHMARK MECHANISM " + json.dumps(records["mechanism_nodes"][str(i)]) + "\n"
                    + "BENCHMARK CPU " + json.dumps({"replicas": [i], "cpu_seconds": 1, "sample_seconds": 10}),
                    encoding="utf-8",
                )
            # Base throughput parsing has separate coverage; use real per-node log merging here.
            with patch.object(fabfile, "_parse_log", side_effect=lambda path: {"finalized": 100}):
                metrics = fabfile._parse_run_logs(paths)
                self.assertEqual(fabfile._mechanism_summary(metrics, 2)["lo_fresh"], 10)
                output = io.StringIO()
                with patch.object(fabfile, "LOG_DIR", Path(directory)), contextlib.redirect_stdout(output):
                    fabfile.logs.body(None)
                self.assertIn('"lo_fresh": 10', output.getvalue())
                self.assertEqual(output.getvalue().count('"mechanism":'), 1)
                self.assertNotIn('"replica_states"', output.getvalue())
                self.assertNotIn('"mechanism_nodes"', output.getvalue())
                with self.assertRaisesRegex(RuntimeError, "missing node logs"):
                    fabfile._parse_run_logs(paths + [Path(directory) / "missing.log"])
                with paths[1].open("a", encoding="utf-8") as handle:
                    handle.write("\nBENCHMARK INVALID: bad commit\n")
                with self.assertRaisesRegex(RuntimeError, "failed benchmark"):
                    fabfile._parse_run_logs(paths)


if __name__ == "__main__":
    unittest.main()
