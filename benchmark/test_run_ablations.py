import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from run_ablations import main, pair_results, parse_benchmarks


class AblationResultsTest(unittest.TestCase):
    def records(self):
        prefix = "BenchmarkAblationGraphMaintenance/n10_f1_g0.9/common-low/seed1/Core/"
        return (prefix + "Incremental-1 10 100 ns/op 32 B/op 1 allocs/op 12 pre_live 9 new_positions\n"
                + prefix + "Rebuild-1 10 200 ns/op 64 B/op 2 allocs/op 12 pre_live 9 new_positions\n")

    def test_pairs_absolute_costs_and_ratio(self):
        rows = parse_benchmarks(self.records())
        pair = pair_results(rows, 2, "BA")[0]
        self.assertEqual(pair["speedup"], 2)
        self.assertEqual(pair["optimized_bytes"], 32)
        self.assertEqual(pair["order"], "BA")

    def test_missing_duplicate_and_mismatched_pairs_rejected(self):
        rows = parse_benchmarks(self.records())
        for invalid in (rows[:1], rows + rows[:1]):
            with self.assertRaises(ValueError):
                pair_results(invalid, 1, "AB")
        rows[1]["metrics"]["pre_live"] = 13
        with self.assertRaises(ValueError):
            pair_results(rows, 1, "AB")

    def test_experiment_selection_and_metadata(self):
        for experiment, pattern in (("3", "^BenchmarkAblationGraphMaintenance$"),
                                    ("4", "^BenchmarkAblationFollowerVerification$")):
            with self.subTest(experiment=experiment), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "results"
                records = self.records()
                if experiment == "4":
                    records = records.replace("GraphMaintenance", "FollowerVerification").replace("Incremental", "Certificate").replace("Rebuild", "Recompute")

                def run(command, **kwargs):
                    if any(arg.startswith("-test.bench=") for arg in command):
                        kwargs["stdout"].write(records)

                with patch("sys.argv", ["run_ablations.py", "--experiment", experiment, "--runs", "1", "--output", str(output)]), \
                     patch("run_ablations.command_text", return_value="{}"), \
                     patch("run_ablations.subprocess.run", side_effect=run) as execute:
                    main()
                commands = [call.args[0] for call in execute.call_args_list]
                self.assertEqual(commands[0], ["go", "test", "./...", "-count=1"])
                self.assertIn(f"-test.bench={pattern}", commands[-1])
                metadata = json.loads((output / "metadata.json").read_text())
                self.assertEqual(metadata["parameters"]["experiment"], experiment)
                self.assertEqual(metadata["commands"], commands)
                self.assertTrue((output / "summary.csv").exists())


if __name__ == "__main__":
    unittest.main()
