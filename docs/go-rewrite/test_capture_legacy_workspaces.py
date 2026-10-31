import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest

from capture_legacy_workspaces import capture

class CaptureTests(unittest.TestCase):
    def fixture(self, root):
        source=root/"source"; managed=source/"worktrees"; leases=managed/"leases"
        leases.mkdir(parents=True)
        catalog=source/"projects.json"
        catalog.write_text('{"version":1,"projects":[{"id":"fixture","root":"/recorded/repository"}]}\n')
        (managed/"connector-id").write_text("a"*48+"\n")
        lease={"version":1,"workspaceId":"wt_fixture"}
        name=hashlib.sha256(lease["workspaceId"].encode()).hexdigest()+".json"
        (leases/name).write_text(json.dumps(lease)+"\n")
        (leases/"ignored.invalid").write_text("do not alter")
        return catalog,managed

    def test_capture_provenance_and_source_bytes_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); catalog,managed=self.fixture(root)
            files=[catalog,*[p for p in managed.rglob("*") if p.is_file()]]
            before={p:(p.read_bytes(),p.stat().st_mtime_ns,p.stat().st_ino,p.stat().st_mode) for p in files}
            result=capture(catalog,managed,root/"snapshot",root/"archive")
            self.assertEqual(result["leases"],1)
            self.assertEqual(result["ignored_source_entries"],1)
            manifest=json.loads((root/"snapshot"/"capture-manifest.json").read_text())
            plan=json.loads((root/"snapshot"/"probe-plan.json").read_text())
            self.assertTrue(manifest["source_unchanged_verified"])
            self.assertFalse(manifest["reference_manager_instantiated"])
            self.assertEqual(plan["capture_manifest_sha256"],hashlib.sha256((root/"snapshot"/"capture-manifest.json").read_bytes()).hexdigest())
            for record in manifest["files"]:
                copied=root/"snapshot"/record["snapshot_path"]
                self.assertEqual(copied.read_bytes(),Path(record["source_path"]).read_bytes())
                self.assertEqual(copied.stat().st_nlink,1)
                self.assertEqual(copied.stat().st_mode&0o777,0o600)
            self.assertEqual(before,{p:(p.read_bytes(),p.stat().st_mtime_ns,p.stat().st_ino,p.stat().st_mode) for p in files})
            self.assertFalse((root/"archive").exists())

    def test_existing_snapshot_and_overlap_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); catalog,managed=self.fixture(root)
            (root/"occupied").mkdir();(root/"occupied"/"keep").write_text("preserved")
            with self.assertRaises(ValueError):capture(catalog,managed,root/"occupied",root/"archive")
            self.assertEqual((root/"occupied"/"keep").read_text(),"preserved")
            with self.assertRaises(ValueError):capture(catalog,managed,managed/"snapshot",root/"archive")
            with self.assertRaises(ValueError):capture(catalog,managed,root/"snapshot",root/"snapshot"/"archive")

    def test_catalog_symlink_rejected_without_following_or_writing(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);catalog,managed=self.fixture(root)
            actual=catalog.with_suffix(".actual");catalog.rename(actual);catalog.symlink_to(actual)
            with self.assertRaises(OSError):capture(catalog,managed,root/"snapshot",root/"archive")
            self.assertFalse((root/"snapshot").exists())
            self.assertTrue(actual.exists())

    def test_ignored_lease_symlink_is_not_copied(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);catalog,managed=self.fixture(root)
            outside=root/"outside";outside.write_text("not lease data")
            (managed/"leases"/"link.json").symlink_to(outside)
            result=capture(catalog,managed,root/"snapshot",root/"archive")
            self.assertEqual(result["ignored_source_entries"],2)
            self.assertFalse((root/"snapshot"/"worktrees"/"leases"/"link.json").exists())
            self.assertEqual(outside.read_text(),"not lease data")

    def test_source_identity_alias_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);catalog,managed=self.fixture(root)
            identity=managed/"connector-id";identity.unlink()
            target=root/"outside";target.write_text("a"*48+"\n");identity.symlink_to(target)
            with self.assertRaises(OSError):capture(catalog,managed,root/"snapshot",root/"archive")
            self.assertFalse((root/"snapshot").exists())

if __name__=="__main__":
    unittest.main()
