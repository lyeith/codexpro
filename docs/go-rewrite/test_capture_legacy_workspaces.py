import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import capture_legacy_workspaces
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

    def absent_fixture(self, root):
        source = root / "source"
        source.mkdir()
        catalog = source / "projects.json"
        catalog.write_text('{"version":1,"projects":[{"id":"actual","root":"/recorded/repository"}]}\n')
        return catalog, source / "worktrees"

    def test_absent_root_requires_explicit_opt_in_and_never_initializes_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            catalog, managed = self.absent_fixture(root)
            before = (catalog.read_bytes(), catalog.stat().st_mtime_ns, catalog.stat().st_ino, managed.parent.stat().st_mtime_ns)
            with self.assertRaises(FileNotFoundError):
                capture(catalog, managed, root/"snapshot", root/"archive")
            self.assertFalse(managed.exists())
            self.assertFalse((root/"snapshot").exists())
            result = capture(catalog, managed, root/"snapshot", root/"archive", allow_absent_managed_root=True)
            self.assertEqual(result["leases"], 0)
            self.assertEqual(result["files"], 1)
            self.assertTrue(result["source_managed_root_absent"])
            self.assertTrue((root/"snapshot"/"worktrees"/"leases").is_dir())
            self.assertEqual(list((root/"snapshot"/"worktrees"/"leases").iterdir()), [])
            self.assertFalse((root/"snapshot"/"worktrees"/"connector-id").exists())
            self.assertFalse(managed.exists())
            self.assertEqual(before, (catalog.read_bytes(), catalog.stat().st_mtime_ns, catalog.stat().st_ino, managed.parent.stat().st_mtime_ns))
            manifest = json.loads((root/"snapshot"/"capture-manifest.json").read_text())
            self.assertEqual(manifest["source_lease_document_count"], 0)
            self.assertTrue(manifest["empty_managed_snapshot_layout"])
            absence = manifest["managed_source_absence"]
            self.assertEqual(absence["before"], absence["after"])
            self.assertTrue(absence["verified_before_and_after_stable_catalog_copy"])
            self.assertFalse(absence["historical_lease_documents_present"])
            self.assertFalse(absence["source_directory_created"])

    def test_absent_opt_in_does_not_accept_an_existing_incomplete_or_aliased_store(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            catalog, managed = self.absent_fixture(root)
            managed.mkdir()
            with self.assertRaises(ValueError):
                capture(catalog, managed, root/"snapshot", root/"archive", allow_absent_managed_root=True)
            managed.rmdir()
            managed.symlink_to(root/"missing-target")
            with self.assertRaises(FileNotFoundError):
                capture(catalog, managed, root/"snapshot", root/"archive", allow_absent_managed_root=True)
            self.assertTrue(managed.is_symlink())
            self.assertFalse((root/"snapshot").exists())

    def test_absent_capture_rejects_a_root_that_appears_during_catalog_copy(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            catalog, managed = self.absent_fixture(root)
            original = capture_legacy_workspaces.read_stable
            def changed(path, limit=1024*1024):
                result = original(path, limit)
                if not managed.exists(): managed.mkdir()
                return result
            with patch("capture_legacy_workspaces.read_stable", side_effect=changed):
                with self.assertRaises(ValueError):
                    capture(catalog, managed, root/"snapshot", root/"archive", allow_absent_managed_root=True)
            self.assertTrue(managed.is_dir())
            self.assertFalse((root/"snapshot").exists())

    def test_absent_capture_rejects_parent_changes_even_when_root_is_missing_again(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            catalog, managed = self.absent_fixture(root)
            original = capture_legacy_workspaces.read_stable
            changed_once = False
            def changed(path, limit=1024*1024):
                nonlocal changed_once
                result = original(path, limit)
                if not changed_once:
                    changed_once = True
                    managed.mkdir(); managed.rmdir()
                    parent = managed.parent.stat()
                    os.utime(managed.parent, ns=(parent.st_atime_ns, parent.st_mtime_ns + 1000000))
                return result
            with patch("capture_legacy_workspaces.read_stable", side_effect=changed):
                with self.assertRaises(ValueError):
                    capture(catalog, managed, root/"snapshot", root/"archive", allow_absent_managed_root=True)
            self.assertFalse(managed.exists())
            self.assertFalse((root/"snapshot").exists())

    def test_absent_capture_still_rejects_a_changed_catalog(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            catalog, managed = self.absent_fixture(root)
            original = capture_legacy_workspaces.read_stable
            changed_once = False
            def changed(path, limit=1024*1024):
                nonlocal changed_once
                result = original(path, limit)
                if not changed_once:
                    changed_once = True
                    catalog.write_bytes(result[0] + b" ")
                return result
            with patch("capture_legacy_workspaces.read_stable", side_effect=changed):
                with self.assertRaises(ValueError):
                    capture(catalog, managed, root/"snapshot", root/"archive", allow_absent_managed_root=True)
            self.assertFalse(managed.exists())
            self.assertFalse((root/"snapshot").exists())

if __name__=="__main__":
    unittest.main()
