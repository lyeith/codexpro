"""Provenance/ownership-boundary guards; these tests never run a profile."""
import ast
import hashlib
import os
from pathlib import Path
import tempfile
import unittest

import final_profile as profile


class FinalProfileGuards(unittest.TestCase):
    def test_ordinary_form_is_exact_executed_source(self):
        self.assertEqual(hashlib.sha256(profile.variant_source(False).encode()).hexdigest(),
                         "bd65cbf7a8dbbfb8d6eefdef5870487f08c5bea28e1744d7a80c801f5811e378")
        ast.parse(profile.variant_source(False))

    def test_build_form_changes_only_the_three_executed_transformations(self):
        expected = profile.ORDINARY_SOURCE.replace("'docs/go-rewrite/profile.py'", "'docs/go-rewrite/profile_under_build_observed.py'")
        expected = expected.replace("'--count','160','--concurrency','2','--duration','180','--delay-ms','50',", "")
        expected = expected.replace("timeout=200", "timeout=240")
        self.assertEqual(profile.variant_source(True), expected)
        self.assertNotIn("'--count'", expected)
        self.assertIn("'docs/go-rewrite/profile_under_build_observed.py'", expected)
        self.assertIn("timeout=240", expected)
        self.assertIn("len(profile['measurements'])==160", expected)
        self.assertIn("'expected_audit_actions':2783", expected)
        ast.parse(expected)

    def test_binding_preserves_path_as_data_and_fixes_side_ports(self):
        unusual = Path("/private/proof/quote'; raise RuntimeError('not code')")
        for side, port in (("go", 7210), ("typescript", 7211)):
            tree = profile.bound_program(unusual, side, "repro-safe-1", False)
            values = {target.id: node.value.value for node in ast.walk(tree)
                      if isinstance(node, ast.Assign) and isinstance(node.value, ast.Constant)
                      for target in node.targets if isinstance(target, ast.Name)}
            self.assertEqual(values["side"], side)
            self.assertEqual(values["port"], port)
            self.assertEqual(values["label"], "repro-safe-1")
            self.assertTrue(any(isinstance(node, ast.Constant) and node.value == str(unusual) for node in ast.walk(tree)))
            compile(tree, "guard-only-never-executed", "exec")
        with self.assertRaises(ValueError):
            profile.bound_program(unusual, "live", "repro-safe-1", False)

    def test_private_state_rejects_anchor_outside_and_alias(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            anchor = root / ".codexpro-go-proof"
            owned = anchor / "owned"
            outside = root / "outside"
            owned.mkdir(parents=True)
            outside.mkdir()
            self.assertEqual(profile.private_state(owned, anchor), owned)
            for path in (anchor, outside):
                with self.assertRaises(ValueError):
                    profile.private_state(path, anchor)
            alias = anchor / "alias"
            alias.symlink_to(owned, target_is_directory=True)
            with self.assertRaises(ValueError):
                profile.private_state(alias, anchor)

    def test_output_guard_rejects_traversal_existing_and_dangling_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            repository = Path(temp)
            output = repository / "docs/go-rewrite/evidence"
            output.mkdir(parents=True)
            profile.check_outputs("repro-ordinary-1", False, repository)
            for label in ("../outside", "with/slash", "", "-leading"):
                with self.assertRaises(ValueError):
                    profile.check_outputs(label, False, repository)
            (output / "existing-context.json").write_text("private fixture")
            with self.assertRaises(ValueError):
                profile.check_outputs("existing", False, repository)
            (output / "dangling.json").symlink_to(repository / "missing")
            with self.assertRaises(ValueError):
                profile.check_outputs("dangling", False, repository)
            (output / "build-load.json").write_text("private fixture")
            with self.assertRaises(ValueError):
                profile.check_outputs("build", True, repository)
            self.assertFalse((output / "repro-ordinary-1.json").exists())


if __name__ == "__main__":
    unittest.main()
