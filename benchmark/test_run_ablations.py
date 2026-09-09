import unittest

from run_ablations import pair_results, parse_benchmarks


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


if __name__ == "__main__":
    unittest.main()
