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
        # This fixture tests later preflight refusals. Its files now live under
        # Projects per rule12; isolate the independently tested private-path policy.
        stack.enter_context(patch.object(m,'private_path',side_effect=lambda value,missing=False:Path(value).resolve(strict=False)))
        stack.enter_context(patch.object(m,'STARTUP_SHA',hashlib.sha256(Path(args.startup_script).read_bytes()).hexdigest()))
        stack.enter_context(patch.object(m,'RULING_SHA',hashlib.sha256(Path(args.ruling).read_bytes()).hexdigest()))
        stack.enter_context(patch.object(m,'assert_free_ports',side_effect=m.Failure('occupied') if busy else None))
        stack.enter_context(patch.object(m,'archive_identity',return_value=(Path(args.archive),{})))
        stack.enter_context(patch.object(m.os,'getpriority',return_value=10))
        stack.enter_context(patch.object(m,'toolchain_preflight',return_value={'version':toolchain}))
        stack.enter_context(patch.object(m.GuardedOwned,'start',side_effect=AssertionError('unexpected process launch')))
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

    def native_fixture(self,root):
        directory=root/'pkg'/'tool'/'linux_amd64';directory.mkdir(parents=True)
        go=root/'bin'/'go';go.parent.mkdir();go.write_bytes(b'\x7fELFfixture-native-go');go.chmod(0o700)
        compiler=directory/'compile';compiler.write_bytes(b'\x7fELFfixture-compiler');compiler.chmod(0o700)
        return directory,go,compiler

    def test_native_build_uses_verified_elf_not_allocating_shim(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory,go,compiler=self.native_fixture(Path(tmp))
            completed=types.SimpleNamespace(stdout=b'go version go1.27.3 linux/amd64\n',stderr=b'')
            with patch.object(m,'NATIVE_GO_SHA',hashlib.sha256(go.read_bytes()).hexdigest()),patch.object(m,'NATIVE_COMPILE_SHA',hashlib.sha256(compiler.read_bytes()).hexdigest()),patch.object(m.subprocess,'run',return_value=completed) as run:
                result=m.native_toolchain({'tool_directory':str(directory),'invoked_go':'/not-a-native/shim'}, {'GOMAXPROCS':'2'})
                self.assertEqual(result['binary'],str(go));self.assertEqual(len(result['images']),2)
                self.assertEqual(run.call_args.args[0],['nice','-n','10',str(go),'version'])

    def test_native_image_hash_mismatch_never_executes(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory,go,compiler=self.native_fixture(Path(tmp))
            with patch.object(m.subprocess,'run') as run,self.assertRaisesRegex(m.Failure,'previously observed'):
                m.native_toolchain({'tool_directory':str(directory)}, {})
            run.assert_not_called()

    def test_shell_toolchain_image_never_executes(self):
        with tempfile.TemporaryDirectory() as tmp:
            directory,go,compiler=self.native_fixture(Path(tmp));go.write_bytes(b'#!/bin/sh\n')
            with patch.object(m.subprocess,'run') as run,self.assertRaisesRegex(m.Failure,'ELF'):
                m.native_toolchain({'tool_directory':str(directory)}, {})
            run.assert_not_called()



    def test_guard_term_is_owned_and_not_repeated(self):
        child=Mock(pid=999);child.poll.return_value=None
        item=types.SimpleNamespace(role='load-0',child=child,witness=123,signal_lock=m.threading.RLock())
        with patch.object(m,'alive',return_value=True),patch.object(m.os,'getpgid',return_value=999),patch.object(m.os,'getsid',return_value=999),patch.object(m.os,'killpg') as kill:
            self.assertTrue(m.request_owned_term(item))
            self.assertFalse(m.request_owned_term(item))
            kill.assert_called_once_with(999,m.signal.SIGTERM)

    def test_guard_term_refuses_group_reassignment(self):
        child=Mock(pid=999);child.poll.return_value=None
        item=types.SimpleNamespace(role='load-0',child=child,witness=123,signal_lock=m.threading.RLock())
        with patch.object(m,'alive',return_value=True),patch.object(m.os,'getpgid',return_value=1234),patch.object(m.os,'killpg') as kill:
            with self.assertRaisesRegex(m.Failure,'group/session'):
                m.request_owned_term(item)
            kill.assert_not_called()

    def test_aborted_guard_prevents_any_new_launch(self):
        gate=m.Gate.__new__(m.Gate);gate.owned_lock=m.threading.RLock()
        gate.guard=Mock();gate.guard.check.side_effect=m.safety.GuardAbort('live health failed')
        with patch.object(m,'GuardedOwned') as owned:
            with self.assertRaises(m.safety.GuardAbort):
                gate.start('load-0',['not-executed'])
            owned.assert_not_called()

    def test_guard_after_launch_preserves_owned_cleanup_handle(self):
        gate=m.Gate.__new__(m.Gate);gate.owned_lock=m.threading.RLock();gate.owned=[]
        gate.guard=Mock();gate.guard.check.side_effect=[None,m.safety.GuardAbort('health failed during launch')]
        item=Mock();item.receipt={};item.child=Mock(pid=999)
        with patch.object(m,'GuardedOwned',return_value=item):
            with self.assertRaises(m.safety.GuardAbort):
                gate.start('load-0',['not-executed'])
        self.assertEqual(gate.owned,[item]);item.start.assert_called_once()

    def test_passing_measurements_with_guard_failure_are_aborted(self):
        gate=m.Gate.__new__(m.Gate);gate.begin=m.time.monotonic()
        gate.archive=Path('/not-opened/private-archive');gate.output=Path('/not-written/receipt.json')
        gate.owned=[];gate.workers=[]
        gate.exercise=Mock();gate.cleanup=Mock();gate.guard=Mock()
        gate.guard.stop.return_value={'aborted':True,'passed':False,'failure':{'reason':'live health'}}
        gate.report={'archive_before':{k:1 for k in ('sha256','bytes','device','inode','mtime_ns','ctime_ns')},'cleanup_errors':[],'guard_stop_errors':[],
            'compiler_wrapper_overlapped_entire_startup':True,
            'compiler_identity':{'actual_go_compiler_observed':True,'compile_cpu_ticks_observed':100,
                'identity_errors':[],'leader':{'pid':999,'cgroup':'owned'},
                'processes':[{'process_group':999,'session':999,'cgroup':'owned'}]}}
        with patch.object(m,'archive_identity',return_value=(gate.archive,gate.report['archive_before'].copy())),patch.object(m,'sidecars',return_value=[]),patch.object(m,'write_launch_receipt') as publish:
            report=gate.run()
        self.assertFalse(report['passed']);self.assertTrue(report['aborted'])
        self.assertEqual(report['cleanup_errors'],[]);self.assertTrue(report['compiler_observed_processes_contained'])
        self.assertEqual(report['status'],'aborted');gate.cleanup.assert_called_once();publish.assert_called_once()

    def launching_gate(self, root):
        gate=m.Gate.__new__(m.Gate)
        gate.parent=gate.state=root
        gate.launches=root/'launches';gate.launches.mkdir()
        gate.binary=root/'candidate';gate.binary.write_bytes(b'\x7fELFfixture')
        gate.binary_sha=hashlib.sha256(gate.binary.read_bytes()).hexdigest()
        gate.environment={}
        gate.owned_lock=m.threading.RLock();gate.owned=[]
        gate.report={'boot_id':'offline-fixture','guard_stop_actions':[],'guard_stop_errors':[]}
        aborted=m.threading.Event()
        def check_guard():
            if aborted.is_set():raise m.safety.GuardAbort('offline abort')
        gate.guard=types.SimpleNamespace(check=check_guard)
        return gate,aborted

    @contextlib.contextmanager
    def fake_launch(self, *, popen=None):
        child=Mock(pid=999);child.poll.return_value=None
        with contextlib.ExitStack() as stack:
            process=stack.enter_context(patch.object(m.subprocess,'Popen',
                side_effect=popen if popen is not None else lambda *a,**k:child))
            stack.enter_context(patch.object(m,'process_identity',
                side_effect=lambda pid:{'pid':pid,'identity':'123','state':'S'}))
            stack.enter_context(patch.object(m,'cgroup_for',return_value='/offline-owned'))
            stack.enter_context(patch.object(m,'alive',return_value=True))
            stack.enter_context(patch.object(m.os,'getpgid',side_effect=lambda pid:pid))
            stack.enter_context(patch.object(m.os,'getsid',side_effect=lambda pid:pid))
            killed=stack.enter_context(patch.object(m.os,'killpg'))
            yield child,process,killed

    def start_thread(self, gate, errors):
        def run():
            try:gate.start('load-0',['offline-not-executed'])
            except BaseException as error:errors.append(error)
        thread=m.threading.Thread(target=run)
        thread.start()
        return thread

    def close_offline_logs(self, gate):
        for item in gate.owned:
            if getattr(item,'log',None):item.log.close()

    def test_guard_stops_existing_load_while_preparation_fsync_is_blocked(self):
        with tempfile.TemporaryDirectory() as tmp:
            gate,aborted=self.launching_gate(Path(tmp))
            existing=types.SimpleNamespace(role='previous-load',child=Mock(pid=998),
                witness=123,signal_lock=m.threading.RLock())
            existing.child.poll.return_value=None;gate.owned.append(existing)
            entered=m.threading.Event();release=m.threading.Event();done=m.threading.Event()
            errors=[]
            def persist(path,receipt,new=False):
                if new:
                    entered.set()
                    self.assertTrue(release.wait(5))
            with self.fake_launch() as (_,popen,killed),patch.object(m,'write_launch_receipt',side_effect=persist):
                thread=self.start_thread(gate,errors)
                try:
                    self.assertTrue(entered.wait(5))
                    aborted.set()
                    stopper=m.threading.Thread(target=lambda:(gate.abort_owned_load(),done.set()))
                    stopper.start()
                    self.assertTrue(done.wait(2),'guard stop waited for prepared-receipt IO')
                    killed.assert_called_once_with(998,m.signal.SIGTERM)
                finally:
                    release.set();thread.join(5)
                    if 'stopper' in locals():stopper.join(5)
                    self.close_offline_logs(gate)
                self.assertFalse(thread.is_alive())
                popen.assert_not_called()
                self.assertEqual(len(errors),1);self.assertIsInstance(errors[0],m.safety.GuardAbort)
                self.assertEqual(gate.report['guard_stop_errors'],[])

    def test_abort_during_inflight_spawn_binds_and_stops_new_handle_once(self):
        with tempfile.TemporaryDirectory() as tmp:
            gate,aborted=self.launching_gate(Path(tmp))
            existing=types.SimpleNamespace(role='previous-load',child=Mock(pid=998),
                witness=123,signal_lock=m.threading.RLock())
            existing.child.poll.return_value=None;gate.owned.append(existing)
            entered=m.threading.Event();release=m.threading.Event();old_stopped=m.threading.Event()
            child=Mock(pid=999);child.poll.return_value=None;errors=[]
            def spawn(*args,**kwargs):
                entered.set()
                self.assertTrue(release.wait(5))
                return child
            def signal_group(pid,sig):
                if pid==998:old_stopped.set()
            with self.fake_launch(popen=spawn) as (_,popen,killed),patch.object(m,'write_launch_receipt'):
                killed.side_effect=signal_group
                thread=self.start_thread(gate,errors)
                try:
                    self.assertTrue(entered.wait(5))
                    aborted.set()
                    stopper=m.threading.Thread(target=gate.abort_owned_load);stopper.start()
                    self.assertTrue(old_stopped.wait(2),'inflight spawn blocked existing-worker stop')
                finally:
                    release.set();thread.join(5)
                    if 'stopper' in locals():stopper.join(5)
                    self.close_offline_logs(gate)
                self.assertFalse(thread.is_alive());self.assertFalse(stopper.is_alive())
                self.assertEqual(popen.call_count,1)
                self.assertEqual([call.args[0] for call in killed.call_args_list].count(998),1)
                self.assertEqual([call.args[0] for call in killed.call_args_list].count(999),1)
                self.assertEqual(gate.owned[-1].witness,123)
                self.assertTrue(gate.owned[-1].term_requested)
                self.assertEqual(len(errors),1);self.assertIsInstance(errors[0],m.safety.GuardAbort)
                self.assertEqual(gate.report['guard_stop_errors'],[])

    def test_guard_sees_bound_handle_before_postlaunch_receipt_fsync(self):
        with tempfile.TemporaryDirectory() as tmp:
            gate,aborted=self.launching_gate(Path(tmp))
            entered=m.threading.Event();release=m.threading.Event();done=m.threading.Event()
            errors=[]
            def persist(path,receipt,new=False):
                if not new and receipt.get('phase')=='identified':
                    entered.set()
                    self.assertTrue(release.wait(5))
            with self.fake_launch() as (_,popen,killed),patch.object(m,'write_launch_receipt',side_effect=persist):
                thread=self.start_thread(gate,errors)
                try:
                    self.assertTrue(entered.wait(5))
                    self.assertEqual(gate.owned[0].witness,123)
                    aborted.set()
                    stopper=m.threading.Thread(target=lambda:(gate.abort_owned_load(),done.set()))
                    stopper.start()
                    self.assertTrue(done.wait(2),'guard stop waited for postlaunch receipt IO')
                    killed.assert_called_once_with(999,m.signal.SIGTERM)
                finally:
                    release.set();thread.join(5)
                    if 'stopper' in locals():stopper.join(5)
                    self.close_offline_logs(gate)
                self.assertFalse(thread.is_alive());self.assertEqual(popen.call_count,1)
                killed.assert_called_once_with(999,m.signal.SIGTERM)
                self.assertEqual(len(errors),1);self.assertIsInstance(errors[0],m.safety.GuardAbort)
                self.assertEqual(gate.report['guard_stop_errors'],[])

    def test_unknown_spawn_outcome_stays_failed_and_is_not_signalled(self):
        with tempfile.TemporaryDirectory() as tmp:
            gate,_=self.launching_gate(Path(tmp))
            item=m.GuardedOwned(gate,'load-0',['offline-not-executed'])
            with self.fake_launch(popen=OSError('offline unknown launch')) as (_,popen,killed),patch.object(m,'write_launch_receipt') as persist:
                try:
                    with self.assertRaises(OSError):item.start()
                    self.assertEqual(item.receipt['phase'],'launch_outcome_unknown')
                    with self.assertRaisesRegex(AssertionError,'unknown'):item.stop()
                    killed.assert_not_called();self.assertEqual(popen.call_count,1)
                    self.assertEqual(persist.call_count,2)
                finally:
                    if item.log:item.log.close()

    def test_refused_exclusive_preparation_does_not_overwrite_on_cleanup(self):
        with tempfile.TemporaryDirectory() as tmp:
            gate,_=self.launching_gate(Path(tmp))
            item=m.GuardedOwned(gate,'load-0',['offline-not-executed'])
            with self.fake_launch() as (_,popen,killed),patch.object(m,'write_launch_receipt',side_effect=FileExistsError) as persist:
                with self.assertRaises(FileExistsError):item.start()
                self.assertEqual(item.stop(),{'started':False,'prepared_receipt_created':False})
                self.assertEqual(persist.call_count,1)
                popen.assert_not_called();killed.assert_not_called()

if __name__=='__main__':unittest.main()
