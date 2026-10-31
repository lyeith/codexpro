import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from batch_migration_proof import (LIMIT, bounded_regular, copy_definition,
                                   validate_definition, verify_resume)


class BatchMigrationGuards(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.definition = {"version": 1, "mode": "serial", "continue_on_error": False,
                           "operations": [{"id": "read-" + str(i), "tool": "read", "args": {"path": str(i) + ".txt"}} for i in range(3)]}
        self.body = (json.dumps(self.definition, indent=2) + "\n").encode()
        self.source, self.target = self.root / "source.json", self.root / "copy.json"
        self.source.write_bytes(self.body)

    def test_exact_bytes_hash_mode_and_source_preserved(self):
        before = self.source.stat()
        result = copy_definition(self.source, self.target, self.definition)
        self.assertEqual(result["operations"], 3)
        self.assertEqual(result["bytes"], len(self.body))
        self.assertEqual(self.target.read_bytes(), self.body)
        self.assertEqual(self.target.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.source.stat().st_mtime_ns, before.st_mtime_ns)

    def test_executable_definition_rejected_before_copy(self):
        self.definition["operations"][0] = {"id": "read-0", "tool": "bash", "args": {"command": "touch forbidden"}}
        self.source.write_text(json.dumps(self.definition))
        with self.assertRaisesRegex(AssertionError, "executable"):
            copy_definition(self.source, self.target, self.definition)
        self.assertFalse(self.target.exists())

    def test_source_symlink_and_existing_destination_refused(self):
        alias = self.root / "alias.json"
        alias.symlink_to(self.source.name)
        with self.assertRaisesRegex(AssertionError, "symlinks"):
            copy_definition(alias, self.target, self.definition)
        self.target.write_text("keep existing")
        with self.assertRaisesRegex(AssertionError, "existing"):
            copy_definition(self.source, self.target, self.definition)
        self.assertEqual(self.target.read_text(), "keep existing")

    def test_changed_recipe_rejected_before_copy(self):
        changed = json.loads(self.body)
        changed["operations"][1]["args"]["path"] = "unrelated.txt"
        self.source.write_text(json.dumps(changed))
        with self.assertRaisesRegex(AssertionError, "differs"):
            copy_definition(self.source, self.target, self.definition)
        self.assertFalse(self.target.exists())

    def test_bounded_source(self):
        self.source.write_bytes(b" " * (LIMIT + 1))
        with self.assertRaisesRegex(AssertionError, "bounded"):
            bounded_regular(self.source)

    def test_resume_preserves_source_indices_not_renumbered_slice(self):
        data = {"batch_source": "file", "persisted": True, "auto_stored": False, "mutating": False,
                "start_index": 1, "total_operation_count": 3, "operation_count": 2,
                "executed_operation_count": 2, "succeeded_count": 2, "failed_count": 0, "skipped_count": 0,
                "results": [{"id": "read-1", "index": 1}, {"id": "read-2", "index": 2}]}
        verify_resume({"structuredContent": data}, 1)
        data["results"][0]["index"] = 0
        with self.assertRaisesRegex(AssertionError, "identities"):
            verify_resume({"structuredContent": data}, 1)


if __name__ == "__main__":
    unittest.main()
