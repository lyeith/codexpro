"""Offline ownership guards; these tests launch no server, worker or compiler."""
import contextlib
import hashlib
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

import loaded_startup_proof as m


class Guards(unittest.TestCase):
    def fixture(self, root):
        binary=root/'candidate';binary.write_bytes(b'\x7fELFprivate-fixture');binary.chmod(0o700)
        script=root/'startup.py';script.write_text('unchanged-test-fixture')
        ruling=root/'ruling.json';ruling.write_text('{}')
        args=types.SimpleNamespace(binary=str(binary),expected_binary_sha=hashlib.sha256(binary.read_bytes()).hexdigest(),
            candidate_source_commit='a'*40,startup_script=str(script),state_parent=str(root/'.codexpro-go-proof'/'new'),
            archive=str(root/'archive.sqlite'),ruling=str(ruling),output=str(root/'result.json'))
        return args

    def preflight(self, root, args, *, busy=False, toolchain='go version go1.27.3 linux/amd64'):
        stack=contextlib.ExitStack()
        stack.enter_context(patch.object(Path,'home',return_value=root))
        stack.enter_context(patch.object(m,'STARTUP_SHA',hashlib.sha256(Path(args.startup_script).read_bytes()).hexdigest()))
        stack.enter_context(patch.object(m,'RULING_SHA',hashlib.sha256(Path(args.ruling).read_bytes()).hexdigest()))
        stack.enter_context(patch.object(m,'assert_free_ports',side_effect=m.Failure('occupied') if busy else None))
        stack.enter_context(patch.object(m,'archive_identity',return_value=(Path(args.archive),{})))
        stack.enter_context(patch.object(m.os,'getpriority',return_value=10))
        stack.enter_context(patch.object(m,'toolchain_preflight',return_value={'version':toolchain}))
        stack.enter_context(patch.object(m.Owned,'start',side_effect=AssertionError('unexpected process launch')))
        return stack

    def test_declared_load_and_deadlines(self):
        self.assertEqual((m.WORKERS,m.LOAD_TARGET,m.WARMUP_SECONDS,m.WORKER_SECONDS),(40,28.0,120,180))
        self.assertEqual(m.EXECUTION_SECONDS+m.CLEANUP_SECONDS,300)
        self.assertEqual(m.INNER_CLEANUP_GRACE,60)
        compile(m.WORKER,'owned-worker','exec')

    def test_every_worker_must_progress_without_reuse(self):
        before=[{'pid':i+100,'start_ticks':42,'cpu_ticks':10} for i in range(40)]
        after=[dict(row,cpu_ticks=11) for row in before]
        self.assertTrue(m.worker_progress(before,after))
        self.assertFalse(m.worker_progress(before,after[:-1]))
        after[17]['cpu_ticks']=10;self.assertFalse(m.worker_progress(before,after))
        after[17]['cpu_ticks']=11;after[17]['start_ticks']=43;self.assertFalse(m.worker_progress(before,after))

    def test_occupied_port_refuses_state_creation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);args=self.fixture(root)
            with self.preflight(root,args,busy=True), self.assertRaisesRegex(m.Failure,'occupied'):m.Gate(args)
            self.assertFalse(Path(args.state_parent).exists())

    def test_binary_hash_refuses_state_creation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);args=self.fixture(root);args.expected_binary_sha='0'*64
            with self.preflight(root,args), self.assertRaisesRegex(m.Failure,'current build'):m.Gate(args)
            self.assertFalse(Path(args.state_parent).exists())

    def test_non_elf_refuses_load_before_inner_proof(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);args=self.fixture(root);Path(args.binary).write_bytes(b'#!/bin/sh\n')
            with self.preflight(root,args), self.assertRaisesRegex(m.Failure,'ELF'):m.Gate(args)
            self.assertFalse(Path(args.state_parent).exists())

    def test_wrong_toolchain_refuses_private_writes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);args=self.fixture(root)
            with self.preflight(root,args,toolchain='go version go1.26.0 linux/amd64'),self.assertRaisesRegex(m.Failure,'toolchain'):m.Gate(args)
            self.assertFalse(Path(args.state_parent).exists())

    def test_dangling_nested_receipt_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);args=self.fixture(root);(root/'result-startup.json').symlink_to(root/'absent')
            with self.preflight(root,args),self.assertRaisesRegex(m.Failure,'reused evidence'):m.Gate(args)
            self.assertTrue((root/'result-startup.json').is_symlink());self.assertFalse(Path(args.state_parent).exists())

    def test_nested_cleanup_grace_only_for_witnessed_child(self):
        child=Mock(pid=999);child.poll.return_value=None
        item=types.SimpleNamespace(role='startup-proof',child=child,witness=123,stop=Mock(return_value={'stopped':True}))
        with patch.object(m,'alive',return_value=False),patch.object(m.os,'killpg') as kill:
            with self.assertRaisesRegex(m.Failure,'identity'):m.stop_owned(item)
            kill.assert_not_called();item.stop.assert_not_called()
        with patch.object(m,'alive',return_value=True),patch.object(m.os,'getpgid',return_value=999),patch.object(m.os,'getsid',return_value=999),patch.object(m.os,'killpg') as kill:
            self.assertEqual(m.stop_owned(item),{'stopped':True})
            kill.assert_called_once_with(999,m.signal.SIGTERM);child.wait.assert_called_once_with(timeout=60)

    def test_zero_sidecars_is_literal_not_only_nonempty_wal(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'archive.sqlite';path.touch()
            self.assertEqual(m.sidecars(path),[])
            wal=Path(str(path)+'-wal');wal.touch();self.assertEqual(m.sidecars(path),[str(wal)])


if __name__=='__main__':unittest.main()
