#!/usr/bin/env python3
"""Read-only identity supplement for the explicitly owned compiler load."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import threading
import time


def process_snapshot(pid):
    root = Path('/proc') / str(pid)
    try:
        fields = (root/'stat').read_text().rsplit(')', 1)[1].split()
        executable = root/'exe'
        info = executable.stat()
        groups = (root/'cgroup').read_text().splitlines()
        group = next((line[3:] for line in groups if line.startswith('0::/')), None)
        return {'pid': pid, 'process_start_ticks': int(fields[19]), 'state': fields[0],
                'ppid': int(fields[1]), 'process_group': int(fields[2]), 'session': int(fields[3]),
                'cpu_ticks': int(fields[11])+int(fields[12]), 'nice': int(fields[16]),
                'executable': os.readlink(executable), 'executable_device': info.st_dev,
                'executable_inode': info.st_ino, 'executable_bytes': info.st_size, 'cgroup': group}
    except (FileNotFoundError, ProcessLookupError):
        return None


def children_of(pid):
    """Go may fork from any runtime thread, so inspect every bounded task."""
    children = set()
    tasks = sorted((Path('/proc')/str(pid)/'task').iterdir(), key=lambda item: int(item.name))
    if len(tasks) > 256: raise RuntimeError('compiler task census exceeded its bound')
    for task in tasks:
        try: raw = (task/'children').read_text()
        except (FileNotFoundError, ProcessLookupError): continue
        if len(raw) > 65536: raise RuntimeError('compiler child census exceeded its bound')
        children.update(int(value) for value in raw.split())
    return children


def group_limits(group):
    if not group or not group.startswith('/') or '..' in Path(group).parts: return None
    root = Path('/sys/fs/cgroup')/group.lstrip('/')
    result = {}
    for name in ('cpu.weight', 'cpu.max', 'memory.max', 'pids.max'):
        try: result[name] = (root/name).read_text().strip()[:256]
        except FileNotFoundError: result[name] = None
    return result


def hash_executable(record):
    path = Path(record['executable'])
    if not path.is_absolute(): return {'sha256': None, 'reason': 'nonabsolute observed executable'}
    try:
        with path.open('rb') as source:
            before = os.fstat(source.fileno())
            expected = (record['executable_device'], record['executable_inode'], record['executable_bytes'])
            if (before.st_dev, before.st_ino, before.st_size) != expected:
                return {'sha256': None, 'reason': 'observed executable identity changed'}
            if before.st_size > 256*1024*1024:
                return {'sha256': None, 'reason': 'executable hash bound exceeded'}
            digest = hashlib.file_digest(source, 'sha256').hexdigest()
            after = os.fstat(source.fileno())
        if (before.st_size, before.st_mtime_ns, before.st_ctime_ns) != (after.st_size, after.st_mtime_ns, after.st_ctime_ns):
            return {'sha256': None, 'reason': 'executable changed during hashing'}
        return {'sha256': digest, 'hashed_after_profile': True}
    except OSError as error:
        return {'sha256': None, 'reason': type(error).__name__}


class BuildIdentity:
    def __init__(self, pid):
        self.pid, self.lock = pid, threading.Lock()
        self.rows, self.groups, self.errors, self.last_sample = {}, {}, [], 0
        try: self.leader = process_snapshot(pid)
        except OSError as error:
            self.leader = None; self.errors.append(type(error).__name__)
        if self.leader is None: self.errors.append('compiler load exited before its identity witness')
        elif self.leader['process_group'] != pid or self.leader['session'] != pid:
            self.errors.append('compiler load lacks the explicitly created group/session')
        self.total_samples = 0
        self.report = None
        self.observe(force=True)

    def observe(self, force=False):
        with self.lock:
            instant = time.monotonic()
            if not force and instant-self.last_sample < .5: return
            self.last_sample = instant
            if self.leader is None: return
            try: leader = process_snapshot(self.pid)
            except OSError as error:
                self.errors.append(type(error).__name__); return
            if leader is None: return
            if leader['process_start_ticks'] != self.leader['process_start_ticks']:
                self.errors.append('load leader incarnation changed'); return
            self.total_samples += 1
            pending, seen = [(self.pid, None)], set()
            while pending:
                pid, parent = pending.pop()
                if pid in seen: continue
                if len(seen) >= 64:
                    self.errors.append('descendant census exceeded64processes'); break
                seen.add(pid)
                try: record = process_snapshot(pid)
                except OSError as error:
                    self.errors.append(type(error).__name__); continue
                if record is None or (parent is not None and record['ppid'] != parent): continue
                key = (pid, record['process_start_ticks'], record['executable_device'], record['executable_inode'])
                row = self.rows.get(key)
                if row is None:
                    row = {**record, 'first_observed_monotonic': instant, 'first_cpu_ticks': record['cpu_ticks'], 'observations': 0}
                    self.rows[key] = row
                row.update(last_observed_monotonic=instant, last_cpu_ticks=record['cpu_ticks'], observations=row['observations']+1)
                group = record['cgroup']
                if group not in self.groups:
                    try: self.groups[group] = group_limits(group)
                    except OSError as error: self.groups[group] = {'error': type(error).__name__}
                try: pending.extend((child, pid) for child in children_of(pid))
                except (FileNotFoundError, ProcessLookupError): pass
                except (OSError, RuntimeError) as error: self.errors.append(str(error)[:160])

    def finish(self):
        """Called only after the timed profiler exits; no binary hashing in it."""
        with self.lock:
            if self.report is not None: return self.report
            rows = sorted(self.rows.values(), key=lambda row: (row['pid'], row['process_start_ticks'], row['executable']))
            hashes = {}
            for row in rows:
                key = (row['executable_device'], row['executable_inode'])
                if key not in hashes: hashes[key] = hash_executable(row)
                row['executable_identity'] = hashes[key]
                row['observed_cpu_tick_increase'] = row['last_cpu_ticks']-row['first_cpu_ticks']
            compilers = [row for row in rows if Path(row['executable']).name == 'compile']
            self.report = {'version': 1, 'leader': self.leader, 'sample_interval_seconds': .5,
                'samples': self.total_samples, 'processes': rows, 'cgroup_limits': self.groups,
                'identity_errors': self.errors, 'observed_compile_processes': len(compilers),
                'compile_cpu_ticks_observed': sum(row['last_cpu_ticks'] for row in compilers),
                'actual_go_compiler_observed': bool(compilers),
                'scope': 'Bounded descendant identity observations; wrapper overlap timing remains separate. No claim that a specific compiler subprocess occupied the entire profile interval.'}
            return self.report


def toolchain_preflight(env):
    """Select the same inherited module/toolchain context before starting load."""
    executable = shutil.which('go', path=env.get('PATH'))
    if not executable: raise RuntimeError('go executable is unavailable')
    results = {}
    for key, args in (('version', ['version']), ('tool_directory', ['env', 'GOTOOLDIR'])):
        completed = subprocess.run(['nice', '-n', '10', executable, *args], env=env,
            stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10, check=True)
        if len(completed.stdout) > 16384 or len(completed.stderr) > 16384:
            raise RuntimeError('toolchain preflight output exceeded its bound')
        results[key] = completed.stdout.decode().strip()
    return {'invoked_go': str(Path(executable).resolve()), 'cwd': str(Path.cwd()),
            'GOTOOLCHAIN': env.get('GOTOOLCHAIN'), 'GOMAXPROCS': env.get('GOMAXPROCS'),
            'GOFLAGS_present': bool(env.get('GOFLAGS')),
            'GOFLAGS_sha256': hashlib.sha256(env.get('GOFLAGS', '').encode()).hexdigest(),
            'GOOS': env.get('GOOS'), 'GOARCH': env.get('GOARCH'), **results,
            'scope': 'Preflight selection metadata; observed descendant executable identities are recorded separately.'}
