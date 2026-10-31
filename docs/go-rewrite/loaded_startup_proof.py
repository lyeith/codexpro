#!/usr/bin/env python3
"""Unchanged startup gate under observed compiler load and bounded own workers."""
from __future__ import annotations
import argparse
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

from startup_recovery_proof import Owned, archive_identity, exclusive, thresholds_pass, valid_cpu_witness, valid_helper_witness
from lifecycle_proof import alive, process_identity, write_launch_receipt
from managed_contract_suite import assert_free_ports, sanitized_environment, process_group_members
from paired_contract_suite import private_path, artifact_path
from build_identity import BuildIdentity, toolchain_preflight
from profile_under_build_observed import LoadObserver, overlap_observation

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


def stop_owned(item):
    # The nested proof owns separately sessioned server/helper processes. Give
    # its own cleanup enough time; a generic five-second parent kill is unsafe.
    if item.role == 'startup-proof' and item.child is not None and item.child.poll() is None:
        witness={'pid':item.child.pid,'identity':str(item.witness)}
        check(item.witness is not None and alive(witness), 'startup child identity changed before graceful cleanup')
        check(os.getpgid(item.child.pid)==item.child.pid and os.getsid(item.child.pid)==item.child.pid,
              'startup child no longer owns its witnessed group/session')
        os.killpg(item.child.pid,signal.SIGTERM)
        try:item.child.wait(timeout=INNER_CLEANUP_GRACE)
        except subprocess.TimeoutExpired:pass
    return item.stop()


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
        self.cache=Path(tempfile.mkdtemp(prefix='sp6-loaded-startup-',dir='/tmp'))
        self.environment['GOCACHE']=str(self.cache/'gocache')
        self.report={'schema_version':1,'kind':'observed-compiler-load-startup-gate',
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

    def start(self,role,argv):
        item=Owned(self,role,argv)
        item.receipt.update(kind='sp6-loaded-startup-owned-process',role=role)
        if role=='compiler':
            go=Path(self.native_toolchain['binary'])
            item.receipt.update(binary=str(go),binary_sha256=hashlib.sha256(go.read_bytes()).hexdigest(),
                                binary_identity_scope='pinned native Go ELF; actual descendant images in compiler_identity')
        self.owned.append(item);item.start();return item

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
            if condition(): return
            if self.workers and time.monotonic()>=next_sample:
                self.observe(phase);next_sample=time.monotonic()+1
            time.sleep(.1)
        raise Failure('bounded wait expired: '+phase)

    def exercise(self):
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
        check(self.report['cleanup_seconds']<=90,'cleanup exceeded its declared budget')

    def run(self):
        try:self.exercise()
        except Exception as error:self.report['failure']=type(error).__name__+': '+str(error)[:1800]
        finally:
            try:self.cleanup()
            except Exception as error:self.report['cleanup_errors'].append(type(error).__name__+': cleanup incomplete')
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
                and self.report['compiler_observed_processes_contained'])
            self.report['elapsed_seconds']=time.monotonic()-self.begin
            write_launch_receipt(self.output,self.report,new=True)
        return self.report


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ('binary','expected-binary-sha','candidate-source-commit','startup-script','state-parent','archive','ruling','output'):
        parser.add_argument('--'+name,required=True)
    args=parser.parse_args();gate=Gate(args)
    def interrupted(signum,frame):raise Failure('loaded gate interrupted; cleaning only owned processes')
    old={sig:signal.signal(sig,interrupted) for sig in (signal.SIGINT,signal.SIGTERM)}
    try:result=gate.run()
    finally:
        for sig,handler in old.items():signal.signal(sig,handler)
    print(json.dumps({'passed':result['passed'],'output':str(gate.output),'cleanup_errors':len(result['cleanup_errors']),
        'load_before':result.get('startup_loadavg_before'),'load_after':result.get('startup_loadavg_after')}))
    return 0 if result['passed'] else 1


if __name__=='__main__':raise SystemExit(main())
