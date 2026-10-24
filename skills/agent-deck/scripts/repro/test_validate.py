#!/usr/bin/env python3
"""Contract checks for evidence acceptance and false fixed claims."""
import hashlib
import importlib.util
import json
import pathlib
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("validator", pathlib.Path(__file__).with_name("validate.py"))
validator = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validator)


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = pathlib.Path(self.tmp.name)
        self.report = {"schema_version": 1, "candidate_id": "fixture", "status": "fixed", "oracle": "fixture expectation",
                       "affected_revision": "a" * 40, "fixed_revision": "b" * 40,
                       "reproduction": {"old_receipt": "old/receipt.json", "fixed_receipt": "good/receipt.json"},
                       "regression": {"test": "TestFixture", "old_receipt": "red/receipt.json", "fixed_receipt": "green/receipt.json"}}
        for name, code in (("old", 1), ("good", 0), ("red", 1), ("green", 0)):
            folder = self.root / name
            folder.mkdir()
            output = b"fixture oracle output\n"
            (folder / "output.log").write_bytes(output)
            receipt = {"exit_code": code, "timed_out": False, "command": ["fixture"], "output_sha256": hashlib.sha256(output).hexdigest(),
                       "oracle_matched": True, "oracle_evidence": "fixture oracle output", "script_sha256": "a" * 64,
                       "binary_sha256": "b" * 64, "fixtures": {}, "experiment": {"image_id": "sha256:" + "c" * 64, "timeout_seconds": 20}}
            (folder / "receipt.json").write_text(json.dumps(receipt))

    def check(self):
        path = self.root / "result.json"
        path.write_text(json.dumps(self.report))
        return validator.validate(path)

    def change_receipt(self, name, **changes):
        path = self.root / name / "receipt.json"
        receipt = json.loads(path.read_text())
        receipt.update(changes)
        path.write_text(json.dumps(receipt))

    def test_complete_evidence(self):
        self.assertEqual(self.check(), "fixed")

    def test_missing_regression(self):
        self.report["regression"] = {}
        with self.assertRaises(ValueError):
            self.check()

    def test_same_revision(self):
        self.report["fixed_revision"] = self.report["affected_revision"]
        with self.assertRaises(ValueError):
            self.check()

    def test_receipt_rejections(self):
        for change in ({"timed_out": True}, {"harness_error": "setup failed"}, {"exit_code": 1},
                       {"output_sha256": "0" * 64}, {"script_sha256": "0" * 64},
                       {"experiment": {"image_id": "another-image", "timeout_seconds": 20}},
                       {"oracle_matched": False}):
            with self.subTest(change=change):
                path = self.root / "good/receipt.json"
                before = path.read_text()
                self.change_receipt("good", **change)
                with self.assertRaises(ValueError):
                    self.check()
                path.write_text(before)

    def test_regression_drift(self):
        self.change_receipt("green", fixtures={"input": "d" * 64})
        with self.assertRaises(ValueError):
            self.check()

    def test_blocked_requires_attempt(self):
        self.report["status"] = "blocked"
        with self.assertRaises(ValueError):
            self.check()
        self.report["attempts"] = [{"reason": "No compatible binary; source inspection only"}]
        self.assertEqual(self.check(), "blocked")


if __name__ == "__main__":
    unittest.main()
