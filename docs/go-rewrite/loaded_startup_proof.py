#!/usr/bin/env python3
"""Unchanged startup gate under observed compiler load and bounded own workers."""
from __future__ import annotations
import argparse
import concurrent.futures
import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time

from startup_recovery_proof import Owned, archive_identity, exclusive, thresholds_pass, valid_cpu_witness, valid_helper_witness
from lifecycle_proof import alive, process_identity, write_launch_receipt, cgroup_for
from managed_contract_suite import assert_free_ports, sanitized_environment, process_group_members
from paired_contract_suite import private_path, artifact_path
from build_identity import BuildIdentity, toolchain_preflight
from profile_under_build_observed import LoadObserver, overlap_observation
import loaded_startup_guards as safety

WORKERS = 40
LOAD_TARGET = 28.0
WARMUP_SECONDS = 120
WORKER_SECONDS = 180
STARTUP_SECONDS = 45
EXECUTION_SECONDS = 210
CLEANUP_SECONDS = 90
INNER_CLEANUP_GRACE = 60
RULING_SHA = '039108a65d004c0c46896c495f21a3fe94ad2fc3843baeec8e9e748065d84e67'
STARTUP_SHA = '3c3147ba5e7037e628e329d7eb8e6c8b772586ff60b1c0098b7b3c9ed28067a4'
NATIVE_GO_SHA = '5f5eb6d0b2fb6aa0bb5d5f8c5f1c944e0de955c925f0111af92a9d8f79680b5b'
NATIVE_COMPILE_SHA = 'c331d8fc6c4ae9dbe20e8198435db0455f2a5ca55d2d02fc6d7337c906be8283'


class Failure(AssertionError): pass


def check(ok, message):
    if not ok: raise Failure(message)


WORKER = r'''#!/usr/bin/env python3
import hashlib,json,os,pathlib,sys,tempfile,time
root,nonce,role,deadline=sys.argv[1:];root=pathlib.Path(root);deadline=float(deadline)
tail=pathlib.Path('/proc/self/stat').read_text().rsplit(')',1)[1].split()
group=next(line[3:] for line in pathlib.Path('/proc/self/cgroup').read_text().splitlines() if line.startswith('0::/'))
value={'pid':os.getpid(),'identity':tail[19],'nonce':nonce,'role':role,'group':os.getpgrp(),
       'session':os.getsid(0),'nice':os.getpriority(os.PRIO_PROCESS,0),'cgroup':group,
       'entered_monotonic':time.monotonic(),'self_deadline_monotonic':deadline}
path=root/(role+'.json');fd,temporary=tempfile.mkstemp(prefix='.'+role+'.',dir=root)
try:
    with os.fdopen(fd,'w') as out:
        os.fchmod(out.fileno(),0o600);json.dump(value,out);out.flush();os.fsync(out.fileno())
    os.link(temporary,path,follow_symlinks=False)
    directory=os.open(root,os.O_RDONLY|os.O_DIRECTORY)
    try:os.fsync(directory)
    finally:os.close(directory)
finally:os.unlink(temporary)
payload=b'bounded-own-low-priority-startup-load'*4096
while time.monotonic()<deadline:
    payload=hashlib.sha256(payload).digest()*4096
'''


def ticks(pid):
    fields=Path(f'/proc/{pid}/stat').read_text().rsplit(')',1)[1].split()
    return int(fields[11])+int(fields[12])


def sidecars(path):
    return [str(Path(str(path)+suffix)) for suffix in ('-wal','-shm','-journal') if Path(str(path)+suffix).exists()]


def read_json(path, limit=1024*1024):
    info=path.stat();check(info.st_size<=limit,'private receipt exceeded its bound')
    raw=path.read_bytes();check(len(raw)<=limit,'private receipt exceeded its bound')
    return json.loads(raw),hashlib.sha256(raw).hexdigest()


def worker_progress(before, after):
    return (len(before)==len(after)==WORKERS and
            all(a['pid']==b['pid'] and a['start_ticks']==b['start_ticks'] and b['cpu_ticks']>a['cpu_ticks']
                for a,b in zip(before,after)))


def native_toolchain(selected, environment):
    """Use the already observed native toolchain, never a resource-allocating shim."""
    directory=Path(selected['tool_directory']).resolve(strict=True)
    check(directory.parts[-3:-1]==('pkg','tool'),'selected GOTOOLDIR has an unexpected layout')
    go=directory.parents[2]/'bin'/'go'
    records=[]
    for path,expected in ((go,NATIVE_GO_SHA),(directory/'compile',NATIVE_COMPILE_SHA)):
        check(path.is_file() and os.access(path,os.X_OK),'selected native toolchain image is not executable')
        with path.open('rb') as file:
            before=os.fstat(file.fileno())
            check(before.st_size<=268435456 and file.read(4)==b'\x7fELF','selected native toolchain image is not a bounded ELF')
            file.seek(0);sha=hashlib.file_digest(file,'sha256').hexdigest();after=os.fstat(file.fileno())
        signature=lambda item:(item.st_dev,item.st_ino,item.st_size,item.st_mtime_ns,item.st_ctime_ns)
        check(signature(before)==signature(after)==signature(path.stat()),'native toolchain image changed during binding')
        check(sha==expected,'native toolchain image differs from the previously observed Go1.27.3 build')
        records.append({'path':str(path),'sha256':sha,'device':before.st_dev,'inode':before.st_ino,'bytes':before.st_size})
    result=subprocess.run(['nice','-n','10',str(go),'version'],env=environment,stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=10,check=True)
    check(len(result.stdout)<=16384 and len(result.stderr)<=16384,'native toolchain version output exceeded its bound')
    check(result.stdout.decode().strip()=='go version go1.27.3 linux/amd64','native toolchain reports a different version')
    return {'binary':str(go),'version':result.stdout.decode().strip(),'images':records,
            'allocation':'native ELF in the owned process group; resource-allocating Go shim is not invoked for build'}


def request_owned_term(item):
    """Signal one witnessed owned group once; never select by name or port."""
    with getattr(item, 'signal_lock', contextlib.nullcontext()):
        if item.child is None or item.child.poll() is not None:
            return False
        if getattr(item, 'term_requested', False):
            return False
        witness={'pid':item.child.pid,'identity':str(item.witness)}
        check(item.witness is not None and alive(witness), 'owned child identity changed before signal')
        check(os.getpgid(item.child.pid)==item.child.pid and os.getsid(item.child.pid)==item.child.pid,
              'owned child no longer owns its witnessed group/session')
        os.killpg(item.child.pid,signal.SIGTERM)
        item.term_requested=True
        return True


def stop_owned(item):
    # The child owns separately sessioned server/helper processes. A guard TERM
    # is not repeated while its own cleanup runs.
    if item.role == 'startup-proof' and item.child is not None and item.child.poll() is None:
        request_owned_term(item)
        try:item.child.wait(timeout=INNER_CLEANUP_GRACE)
        except subprocess.TimeoutExpired:pass
    return item.stop()


class GuardedOwned(Owned):
    """Publish an in-flight launch handle without serializing guard stops on IO."""
    def __init__(self, proof, role, argv):
        super().__init__(proof, role, argv)
        self.signal_lock = threading.RLock()
        self.prepared_persisted = False

    def check_abort_after_binding(self):
        try:
            self.proof.guard.check()
        except safety.GuardAbort:
            # The monitor may already have passed this handle while child=None.
            # Once Popen returns, this path binds and stops that admitted spawn.
            request_owned_term(self)
            raise

    def start(self):
        self.proof.guard.check()
        write_launch_receipt(self.receipt_path, self.receipt, new=True)
        self.prepared_persisted = True
        self.log = (self.proof.parent / (self.role + ".log")).open("xb")
        os.chmod(self.log.name, 0o600)
        # Preparation can block on SSD. No registry or signal lock spans it.
        self.proof.guard.check()
        try:
            with self.signal_lock:
                self.proof.guard.check()
                try:
                    self.child = subprocess.Popen(
                        self.argv, cwd=self.proof.parent, env=self.proof.environment,
                        stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log,
                        start_new_session=True)
                except Exception as error:
                    self.receipt.update(phase="launch_outcome_unknown",
                                        error_class=type(error).__name__)
                    raise
                self.receipt.update(phase="spawned_unverified", pid=self.child.pid)
                identity = process_identity(self.child.pid)
                check(identity and self.child.poll() is None,
                      "owned process exited before identity verification")
                self.witness = int(identity["identity"])
                check(os.getpgid(self.child.pid) == self.child.pid
                      and os.getsid(self.child.pid) == self.child.pid,
                      "owned process lacks its private group/session")
                self.receipt.update(
                    phase="identified", process_start_ticks=self.witness,
                    cgroup=cgroup_for(self.child.pid), process_identity_verified=True)
            # The exact in-memory witness is visible before any postlaunch fsync.
        except Exception:
            if self.child is not None or self.receipt.get("phase") == "launch_outcome_unknown":
                write_launch_receipt(self.receipt_path, self.receipt)
            raise
        self.check_abort_after_binding()
        write_launch_receipt(self.receipt_path, self.receipt)
        self.check_abort_after_binding()

    def stop(self):
        if not self.prepared_persisted and self.child is None:
            if self.log:
                self.log.close()
            # An exclusive prepared-receipt refusal grants no right to replace it.
            return {"started": False, "prepared_receipt_created": False}
        return super().stop()


class Gate:
    def __init__(self,args):
        self.begin=time.monotonic();self.deadline=self.begin+EXECUTION_SECONDS
        self.args=args
        self.parent=private_path(args.state_parent,missing=True)
        check(self.parent==Path(args.state_parent).expanduser().absolute() and not self.parent.exists(),
              'loaded startup requires a fresh canonical private parent')
        check(self.parent.is_relative_to(Path.home().resolve()/'.codexpro-go-proof'),'state must be a private proof child')
        check(not Path(args.output).expanduser().is_symlink(),'evidence cannot be an existing symlink')
        self.output=artifact_path(args.output)
        self.inner_output=self.output.with_name(self.output.stem+'-startup.json')
        check(not self.output.exists() and not self.output.is_symlink() and not self.inner_output.exists()
              and not self.inner_output.is_symlink(),'refusing reused evidence')
        check(re.fullmatch(r'[0-9a-f]{40}',args.candidate_source_commit),'candidate source commit must be an exact Git commit')
        assert_free_ports((7210,))
        self.binary=Path(args.binary).resolve(strict=True)
        check(self.binary.is_file() and os.access(self.binary,os.X_OK) and self.binary.stat().st_size<=268435456,
              'candidate must be an explicit bounded executable')
        with self.binary.open('rb') as executable:
            check(executable.read(4)==b'\x7fELF','candidate must be a built ELF executable')
        self.binary_sha=hashlib.sha256(self.binary.read_bytes()).hexdigest()
        check(self.binary_sha==args.expected_binary_sha,'candidate binary does not match the declared current build')
        self.script=Path(args.startup_script).resolve(strict=True)
        check(hashlib.sha256(self.script.read_bytes()).hexdigest()==STARTUP_SHA,'startup proof must remain byte-for-byte unchanged')
        ruling_raw=Path(args.ruling).read_bytes()
        check(hashlib.sha256(ruling_raw).hexdigest()==RULING_SHA,'coordinator ruling receipt changed')
        self.archive,archive_before=archive_identity(args.archive)
        check(not sidecars(self.archive),'private immutable archive has sidecars before loaded gate')
        check(os.getpriority(os.PRIO_PROCESS,0)>=10,'run the wrapper under nice -n10')
        self.environment=sanitized_environment();self.environment.update(GOMAXPROCS='2',GOTOOLCHAIN='go1.27.3')
        toolchain=toolchain_preflight(self.environment)
        check(toolchain['version'].split()[2]=='go1.27.3','compiler toolchain differs from the declared load')
        self.native_toolchain=native_toolchain(toolchain,self.environment)
        self.parent.mkdir(parents=True,mode=0o700)
        self.state,self.launches,self.control=self.parent,self.parent/'launches',self.parent/'control'
        self.launches.mkdir(mode=0o700);self.control.mkdir(mode=0o700)
        self.output.parent.mkdir(parents=True,exist_ok=True)
        self.nonce=os.urandom(16).hex()
        self.worker_script=self.control/'cpu-worker.py';exclusive(self.worker_script,WORKER,0o700)
        evidence_root=Path.home()/'Projects'/'sp6-evidence'
        check(evidence_root.resolve()==evidence_root and evidence_root.is_dir(),'evidence root must be canonical under Projects')
        self.cache=Path(tempfile.mkdtemp(prefix='loaded-startup-cache-',dir=evidence_root))
        (self.cache/'tmp').mkdir(mode=0o700)
        self.environment.update(GOCACHE=str(self.cache/'gocache'),TMPDIR=str(self.cache/'tmp'))
        self.report={'schema_version':1,'kind':'observed-compiler-load-startup-gate',
            'started_at':safety.utc_now(),'started_monotonic':self.begin,
            'driver_sha256':hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            'guard_module_sha256':hashlib.sha256(Path(safety.__file__).read_bytes()).hexdigest(),
            'compiler_cache':str(self.cache),
            'candidate_binary':str(self.binary),'candidate_binary_sha256':self.binary_sha,
            'candidate_source_commit':args.candidate_source_commit,'ruling_sha256':RULING_SHA,
            'startup_script_sha256':STARTUP_SHA,'boot_id':Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
            'archive_before':archive_before,'archive_sidecars_before':[],
            'declared':{'workers':WORKERS,'minimum_loadavg_1m':LOAD_TARGET,'warmup_max_seconds':WARMUP_SECONDS,
                        'worker_self_deadline_seconds':WORKER_SECONDS,'startup_child_limit_seconds':STARTUP_SECONDS,
                        'compiler_command_deadline_seconds':90,'compiler_kill_grace_seconds':3,
                        'execution_max_seconds':EXECUTION_SECONDS,'cleanup_max_seconds':CLEANUP_SECONDS,
                        'internal_total_seconds':EXECUTION_SECONDS+CLEANUP_SECONDS,
                        'external_wrapper_seconds':300,'external_kill_grace_seconds':90,
                        'startup_child_term_grace_seconds':INNER_CLEANUP_GRACE},
            'host':{'logical_cpus':os.cpu_count(),'affinity_cpus':len(os.sched_getaffinity(0))},
            'toolchain':toolchain,'native_toolchain':self.native_toolchain,
            'load_samples':[],'cleanup':[],'cleanup_errors':[],
            'live_service_actions':[],'original_startup_receipt_preserved':True,'stage4_complete':False}
        self.owned=[];self.workers=[];self.compiler=None;self.observer=None;self.proof=None
        self.inner_parent=self.parent/'candidate'
        self.owned_lock=threading.RLock()
        self.guard=safety.SafetyGuard(health_probe=lambda:safety.probe_live_health(live_token_file=args.live_token_file))
        self.report.update(guard_stop_actions=[],guard_stop_errors=[])

    def abort_owned_load(self):
        # Snapshot handles without holding this registry lock through signals,
        # process binding, receipt fsync, or any other filesystem operation.
        with self.owned_lock:
            existing = tuple(self.owned)
        for item in existing:
            try:
                sent=request_owned_term(item)
                self.report['guard_stop_actions'].append({'role':item.role,
                    'pid':item.child.pid if item.child else None,'signal_sent':sent,
                    'utc':safety.utc_now(),'monotonic':time.monotonic()})
            except Exception as error:
                self.report['guard_stop_errors'].append(type(error).__name__+': '+item.role)

    def start(self,role,argv):
        self.guard.check()
        item=GuardedOwned(self,role,argv)
        item.receipt.update(kind='sp6-loaded-startup-owned-process',role=role)
        if role=='compiler':
            go=Path(self.native_toolchain['binary'])
            item.receipt.update(binary=str(go),binary_sha256=hashlib.sha256(go.read_bytes()).hexdigest(),
                                 binary_identity_scope='pinned native Go ELF; actual descendant images in compiler_identity')
        # Publish before preparation/launch: even a failed in-flight start is
        # retained for cleanup. GuardedOwned checks again immediately at Popen.
        with self.owned_lock:
            self.owned.append(item)
        item.start()
        self.guard.check()
        return item

    def sample_workers(self):
        rows=[]
        for worker in self.workers:
            check(worker.child.poll() is None,'an external CPU worker exited during the required loaded gate')
            witness={'pid':worker.child.pid,'identity':str(worker.witness)}
            check(alive(witness),'external CPU worker incarnation changed')
            rows.append({'pid':worker.child.pid,'start_ticks':worker.witness,'cpu_ticks':ticks(worker.child.pid)})
        return rows

    def observe(self,phase):
        rows=self.sample_workers()
        self.report['load_samples'].append({'phase':phase,'monotonic':time.monotonic(),
            'elapsed_seconds':time.monotonic()-self.begin,'loadavg':os.getloadavg(),'workers':rows})
        return rows

    def wait(self,condition,seconds,phase):
        until=min(self.deadline,time.monotonic()+seconds)
        next_sample=0
        while time.monotonic()<until:
            self.guard.check()
            if condition(): return
            if self.workers and time.monotonic()>=next_sample:
                self.observe(phase);next_sample=time.monotonic()+1
            time.sleep(.1)
        raise Failure('bounded wait expired: '+phase)

    def exercise(self):
        self.guard.before_load()
        self.guard.start_monitor(self.abort_owned_load)
        self.report['load_workers_started_at']=safety.utc_now()
        self.report['load_workers_started_monotonic']=time.monotonic()
        self.worker_deadline=time.monotonic()+WORKER_SECONDS
        for index in range(WORKERS):
            role='load-'+str(index)
            worker=self.start(role,['nice','-n','19',sys.executable,str(self.worker_script),str(self.control),
                self.nonce,role,str(self.worker_deadline)])
            self.workers.append(worker)
        warmup_started=time.monotonic()
        self.wait(lambda:all((self.control/(w.role+'.json')).exists() for w in self.workers),15,'worker-witnesses')
        markers=[]
        for worker in self.workers:
            value,_=read_json(self.control/(worker.role+'.json'))
            valid_cpu_witness(value,self.nonce,worker.role,worker.child.pid,worker.witness)
            check(value['nice']==19,'external load worker is not at the declared nice19 priority')
            check(value['self_deadline_monotonic']==self.worker_deadline,'worker self-deadline changed')
            markers.append({k:v for k,v in value.items() if k!='nonce'})
        self.report['worker_witnesses']=markers
        self.wait(lambda:os.getloadavg()[0]>=LOAD_TARGET and all(ticks(w.child.pid)>0 for w in self.workers),
                  max(0,WARMUP_SECONDS-(time.monotonic()-warmup_started)),'near-incident-load')
        self.report['load_at_compiler_launch']=os.getloadavg()
        self.report['warmup_seconds']=time.monotonic()-warmup_started
        command=['nice','-n','19','timeout','--kill-after=3s','90s',self.native_toolchain['binary'],'build','-p','2','-a',
                 '-o',str(self.cache/'compiler'),'cmd/compile']
        self.report['compiler_command']=command
        self.compiler=self.start('compiler',command)
        self.observer=LoadObserver(self.compiler.child,BuildIdentity(self.compiler.child.pid))
        self.observer.observe();self.observer.thread.start()
        def compiler_seen():
            check(self.compiler.child.poll() is None,'compiler exited before the startup interval')
            with self.observer.identity.lock:
                return any(Path(row['executable']).name=='compile' and row['last_cpu_ticks']>0
                           for row in self.observer.identity.rows.values())
        self.wait(compiler_seen,15,'actual-compiler-entry')
        check(time.monotonic()+STARTUP_SECONDS<self.worker_deadline,'insufficient worker lifetime remains for the unchanged startup proof')
        before=self.observe('startup-before')
        self.report['startup_loadavg_before']=os.getloadavg()
        check(self.report['startup_loadavg_before'][0]>=LOAD_TARGET,'host load fell below the fixed threshold before startup')
        self.gate_started=time.monotonic()
        self.proof=self.start('startup-proof',[sys.executable,str(self.script),'--binary',str(self.binary),
            '--expected-binary-sha',self.binary_sha,'--state-parent',str(self.inner_parent),
            '--archive',str(self.archive),'--output',str(self.inner_output)])
        self.wait(lambda:self.proof.child.poll() is not None,STARTUP_SECONDS,'unchanged-startup-proof')
        self.gate_finished=time.monotonic();after=self.observe('startup-after')
        self.observer.observe()
        overlap=overlap_observation(self.gate_started,self.gate_finished,self.observer.last_alive_mono,self.observer.first_exit_mono)
        self.report.update(startup_started_monotonic=self.gate_started,startup_finished_monotonic=self.gate_finished,
            compiler_wrapper_overlapped_entire_startup=overlap,workers_progressed_across_startup=worker_progress(before,after),
            startup_loadavg_after=os.getloadavg(),startup_exit_code=self.proof.child.returncode)
        check(overlap is True,'compiler wrapper did not remain observed alive through the complete startup proof')
        check(self.report['workers_progressed_across_startup'],'not every owned worker demonstrated CPU progress across startup')
        check(self.report['startup_loadavg_after'][0]>=LOAD_TARGET,'host load fell below the threshold during startup')
        inner,sha=read_json(self.inner_output,4*1024*1024)
        self.report['startup_receipt']={'path':str(self.inner_output),'sha256':sha,'unchanged_receipt':inner}
        check(self.proof.child.returncode==0 and inner.get('passed') and thresholds_pass(inner),
              'the unchanged startup3-second gate failed under the declared load')
        check(inner.get('binary_sha256')==self.binary_sha,'inner startup proof measured another binary')
        check(inner.get('cleanup_verified') and not inner.get('cleanup_errors'),'inner proof did not verify its owned cleanup')

    def cleanup(self):
        # Let the unchanged child run its own cleanup on TERM; stop the external
        # CPU groups concurrently so its listener/helper cleanup is not starved.
        began=time.monotonic()
        with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
            futures={pool.submit(stop_owned,item):item for item in reversed(self.owned)}
            for future,item in futures.items():
                try:self.report['cleanup'].append({'role':item.role,**future.result(timeout=max(1,85-(time.monotonic()-began)))})
                except Exception as error:self.report['cleanup_errors'].append(type(error).__name__+': '+item.role+' cleanup failed')
        if self.observer:
            try:
                self.observer.close();self.report['compiler_identity']=self.observer.identity.report
                self.report['compiler_exit_code']=self.compiler.child.returncode
                self.report['compiler_completion']='owned load stopped after measured startup; completion is not required or claimed'
            except Exception as error:self.report['cleanup_errors'].append(type(error).__name__+': compiler observer cleanup failed')
        if self.inner_parent.exists():
            for receipt in (self.inner_parent/'launches').glob('*.json'):
                value,_=read_json(receipt)
                if value.get('pid') and value.get('process_start_ticks'):
                    witness={'pid':value['pid'],'identity':str(value['process_start_ticks'])}
                    if alive(witness) or process_group_members(value['pid']):
                        self.report['cleanup_errors'].append('inner owned launch still alive: '+receipt.name)
                elif value.get('phase')=='launch_outcome_unknown':
                    self.report['cleanup_errors'].append('inner launch outcome remains unknown: '+receipt.name)
            config_path=self.inner_parent/'control'/'helper-config.json'
            if config_path.exists():
                config,_=read_json(config_path)
                for path in (self.inner_parent/'control').glob('helper-entered-*.json'):
                    marker,_=read_json(path)
                    valid_helper_witness(marker,config['nonce'],self.inner_parent/'control'/'ssd-dev-standin')
                    if alive(marker):self.report['cleanup_errors'].append('exact inner helper still alive: '+path.name)
        self.report['cleanup_seconds']=time.monotonic()-began
        self.report['load_cleanup_finished_at']=safety.utc_now()
        self.report['load_cleanup_finished_monotonic']=time.monotonic()
        if not self.report['cleanup_errors']:
            shutil.rmtree(self.cache)
            self.report['compiler_cache_removed']=not self.cache.exists()
        check(self.report['cleanup_seconds']<=90,'cleanup exceeded its declared budget')

    def run(self):
        try:self.exercise()
        except Exception as error:self.report['failure']=type(error).__name__+': '+str(error)[:1800]
        finally:
            try:self.cleanup()
            except Exception as error:self.report['cleanup_errors'].append(type(error).__name__+': cleanup incomplete')
            self.report['safety_guard']=self.guard.stop()
            self.report['aborted']=self.report['safety_guard']['aborted']
            self.report['launched_worker_count']=sum(item.role.startswith('load-') and item.child is not None for item in self.owned)
            try:
                _,after=archive_identity(self.archive)
                self.report['archive_after']=after;self.report['archive_sidecars_after']=sidecars(self.archive)
                before=self.report['archive_before']
                self.report['archive_unchanged']=all(before[k]==after[k] for k in ('sha256','bytes','device','inode','mtime_ns','ctime_ns'))
                check(self.report['archive_unchanged'] and not self.report['archive_sidecars_after'],'private archive or sidecars changed')
            except Exception as error:self.report['cleanup_errors'].append(type(error).__name__+': archive postcheck failed')
            identity=self.report.get('compiler_identity',{})
            rows=identity.get('processes',[])
            leader=identity.get('leader') or {}
            self.report['compiler_observed_processes_contained']=(bool(rows) and all(
                row.get('process_group')==leader.get('pid') and row.get('session')==leader.get('pid')
                and row.get('cgroup')==leader.get('cgroup') for row in rows))
            self.report['passed']=(not self.report.get('failure') and not self.report['cleanup_errors']
                and identity.get('actual_go_compiler_observed') is True and identity.get('compile_cpu_ticks_observed',0)>0
                and not identity.get('identity_errors') and self.report.get('compiler_wrapper_overlapped_entire_startup') is True
                and self.report['compiler_observed_processes_contained']
                and self.report['safety_guard']['passed'] and not self.report['aborted']
                and not self.report['guard_stop_errors'])
            self.report['elapsed_seconds']=time.monotonic()-self.begin
            self.report['finished_at']=safety.utc_now()
            self.report['status']='aborted' if self.report['aborted'] else ('passed' if self.report['passed'] else 'failed')
            write_launch_receipt(self.output,self.report,new=True)
        return self.report


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ('binary','expected-binary-sha','candidate-source-commit','startup-script','state-parent','archive','ruling','output','live-token-file'):
        parser.add_argument('--'+name,required=True)
    args=parser.parse_args();gate=Gate(args)
    def interrupted(signum,frame):raise Failure('loaded gate interrupted; cleaning only owned processes')
    old={sig:signal.signal(sig,interrupted) for sig in (signal.SIGINT,signal.SIGTERM)}
    try:result=gate.run()
    finally:
        for sig,handler in old.items():signal.signal(sig,handler)
    print(json.dumps({'passed':result['passed'],'status':result['status'],'output':str(gate.output),'cleanup_errors':len(result['cleanup_errors']),
        'guard_aborted':result['aborted'],'load_before':result.get('startup_loadavg_before'),'load_after':result.get('startup_loadavg_after')}))
    return 0 if result['passed'] else 1


if __name__=='__main__':raise SystemExit(main())
