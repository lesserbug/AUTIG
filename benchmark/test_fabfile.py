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
            with patch.object(fabfile, "RESULT_DIR", Path(directory)), contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(warning):
                fabfile._write_result("local", self.parameters, 1, self.metrics)
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


if __name__ == "__main__":
    unittest.main()
