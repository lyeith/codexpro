#!/usr/bin/env python3
"""Reproduce the final profile context; prior receipts used the inline form."""
from __future__ import annotations
import argparse
import ast
import hashlib
import json
import os
from pathlib import Path
import re
import sys
from paired_contract_suite import private_path

REPOSITORY = Path("/home/spite/Projects/codexpro-go")
TEMPLATE_SHA256 = "bd65cbf7a8dbbfb8d6eefdef5870487f08c5bea28e1744d7a80c801f5811e378"
# Captured ordinary inline template; earlier runs bound its four placeholders.
# It is not run on import.
ORDINARY_SOURCE = r"""import pathlib,subprocess,json,hashlib,os,sys,time,datetime
root=pathlib.Path('/home/spite/Projects/codexpro-go');sys.path.insert(0,str(root/'docs/go-rewrite'))
from profile import Client
from paired_contract_suite import validate_launch
state=pathlib.Path('__STATE__');side='__SIDE__';port=__PORT__;label='__LABEL__';out=root/'docs/go-rewrite/evidence'/f'{label}.json';context=out.with_name(out.stem+'-context.json');assert not out.exists() and not context.exists()
launch=json.loads((state/'launch.json').read_text());pid=launch['pid'];proc=pathlib.Path('/proc')/str(pid)
def ticks():return int((proc/'stat').read_text().rsplit(')',1)[1].split()[19])
started=ticks()
if side=='go':
 assert launch['binary_sha256']=='c4402d15660cbad962b03a63a4ec8f1c4561f7a51264c2d2d1cdcaa4b7ebfda2'
 validate_launch(launch,f'http://127.0.0.1:{port}/mcp',launch['token_file'],launch['fixture_root'],port)
else:
 env=dict(x.split(b'=',1) for x in (proc/'environ').read_bytes().split(b'\0') if b'=' in x)
 assert env[b'CODEXPRO_HOME']==str(state/'state').encode() and env[b'CODEXPRO_PORT']==str(port).encode()
 assert b'/home/spite/apps/codexpro/releases/job45-dashboard-20261030-44bb76b/dist/http.js' in (proc/'cmdline').read_bytes()
def sha(b):return hashlib.sha256(b).hexdigest()
project=pathlib.Path('/home/spite/Projects/codexpro')
def fingerprint():
 def git(*args):return subprocess.check_output(['git','-C',str(project),*args],timeout=15)
 names=git('ls-files','--cached','--others','--exclude-standard','-z').split(b'\0');lines=[];count=0;total=0
 for raw in sorted(set(x for x in names if x)):
  p=project/os.fsdecode(raw)
  if p.is_symlink():data=os.readlink(p).encode();kind='symlink'
  elif p.is_file():data=p.read_bytes();kind='file'
  elif not p.exists():data=b'';kind='missing'
  else:data=b'';kind='directory'
  lines.append(raw+b'\0'+kind.encode()+b'\0'+str(len(data)).encode()+b'\0'+sha(data).encode()+b'\n');count+=1;total+=len(data)
 status=git('status','--porcelain=v1','-z','--untracked-files=all')
 return {'root':str(project),'head':git('rev-parse','HEAD').decode().strip(),'head_tree':git('rev-parse','HEAD^{tree}').decode().strip(),'status_sha256':sha(status),'status_bytes':len(status),'working_files_sha256':sha(b''.join(lines)),'working_file_entries':count,'working_file_bytes':total}
catalog=json.loads((state/'projects.json').read_text());assert next(p for p in catalog['projects'] if p['id']=='codexpro')['root']==str(project)
normalized=json.loads(json.dumps(catalog).replace(str(state),'[private-state]'))
before=fingerprint()
deadline=time.monotonic()+30
while True:
 client=None
 try:
  client=Client(f'http://127.0.0.1:{port}/mcp',(state/'http-token').read_text().strip(),timeout=3);reply=client.rpc('tools/list',{});assert len(reply.get('result',{}).get('tools',[]))>=28;break
 except Exception:
  if time.monotonic()>deadline:raise
  time.sleep(.2)
 finally:
  if client:client.close()
receipt={'schema_version':1,'started_at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'side':side,'label':label,'pid':pid,'process_start_ticks':started,'binary_sha256':sha((proc/'exe').read_bytes()),'executable':str((proc/'exe').resolve()),'launch_manifest_sha256':sha((state/'launch.json').read_bytes()),'limits_sha256':sha((state/'limits.env').read_bytes()),'limits':(state/'limits.env').read_text().splitlines(),'catalog_sha256':sha((state/'projects.json').read_bytes()),'catalog_private_roots_normalized':normalized,'before':before,'cache_condition':'Fresh server/application analysis caches; source files hashed before run so shared OS page cache is uncontrolled and may be warm. Metadata-only MCP initialize/tools/list readiness; no tool calls before timed corpus.','expected_audit_actions':2783,'audit_seed_evidence':'docs/go-rewrite/evidence/performance-seeds-final.json','nice':os.getpriority(os.PRIO_PROCESS,pid),'cgroup':(proc/'cgroup').read_text().strip(),'go_build_environment':{'GOTOOLCHAIN':os.environ.get('GOTOOLCHAIN'),'GOMAXPROCS':os.environ.get('GOMAXPROCS')}}
command=[sys.executable,str(root/'docs/go-rewrite/profile.py'),'--url',f'http://127.0.0.1:{port}/mcp','--token-file',str(state/'http-token'),'--pid',str(pid),'--workspace','ws_4d43e03b8d338283c2776d95','--count','160','--concurrency','2','--duration','180','--delay-ms','50','--label',label,'--out',str(out)]
result=subprocess.run(command,timeout=200);receipt['profile_exit_code']=result.returncode;receipt['after']=fingerprint();receipt['inputs_unchanged']=receipt['before']==receipt['after'];receipt['process_identity_unchanged']=ticks()==started;receipt['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat();receipt['profile_sha256']=sha(out.read_bytes()) if out.exists() else None
context.write_text(json.dumps(receipt,indent=2)+'\n');print('CONTEXT',json.dumps({'path':str(context),'pid':pid,'process_start_ticks':started,'inputs_unchanged':receipt['inputs_unchanged'],'profile_sha256':receipt['profile_sha256'],'project':receipt['before']}))
profile=json.loads(out.read_text());assert result.returncode==0 and receipt['inputs_unchanged'] and receipt['process_identity_unchanged'] and len(profile['measurements'])==160 and profile['failures']==0 and not profile['initialization_errors']
"""


def variant_source(under_build):
    if hashlib.sha256(ORDINARY_SOURCE.encode()).hexdigest() != TEMPLATE_SHA256:
        raise ValueError("ordinary template identity changed")
    source = ORDINARY_SOURCE
    if under_build:
        for before, after in (
            ("'docs/go-rewrite/profile.py'", "'docs/go-rewrite/profile_under_build_observed.py'"),
            ("'--count','160','--concurrency','2','--duration','180','--delay-ms','50',", ""),
            ("timeout=200", "timeout=240"),
        ):
            if source.count(before) != 1:
                raise ValueError("build transformation no longer matches the exact inline form")
            source = source.replace(before, after, 1)
    return source


def private_state(value, proof_root=None):
    anchor = private_path(proof_root or (Path.home() / ".codexpro-go-proof"))
    supplied = Path(value).expanduser().absolute()
    state = private_path(supplied)
    if state != supplied or state == anchor or not state.is_relative_to(anchor) or not state.is_dir():
        raise ValueError("state must be a canonical private child of ~/.codexpro-go-proof")
    if state.stat().st_uid != os.getuid():
        raise ValueError("private state is not owned by the current user")
    return state


def check_outputs(label, under_build, repository=REPOSITORY):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,100}", label):
        raise ValueError("label must be one bounded lexical filename component")
    directory = repository / "docs/go-rewrite/evidence"
    if not directory.is_dir() or directory.resolve() != directory:
        raise ValueError("evidence directory must exist without symlink aliases")
    names = [label + ".json", label + "-context.json"]
    if under_build:
        names.append(label + "-load.json")
        if os.path.lexists(Path("/tmp") / ("sp6-load-" + label)):
            raise ValueError("build cache/run directory already exists")
    if any(os.path.lexists(directory / name) for name in names):
        raise ValueError("profile/context/load output must be new, including no dangling symlinks")


def bound_program(state, side, label, under_build):
    if side not in {"go", "typescript"}:
        raise ValueError("side must be go or typescript")
    values = {"__STATE__": str(state), "__SIDE__": side, "__LABEL__": label}
    counts = dict.fromkeys([*values, "__PORT__"], 0)
    class Bind(ast.NodeTransformer):
        def visit_Constant(self, node):
            if isinstance(node.value, str) and node.value in values:
                counts[node.value] += 1
                return ast.copy_location(ast.Constant(values[node.value]), node)
            return node
        def visit_Name(self, node):
            if node.id == "__PORT__":
                counts[node.id] += 1
                return ast.copy_location(ast.Constant(7210 if side == "go" else 7211), node)
            return node
    tree = Bind().visit(ast.parse(variant_source(under_build)))
    if set(counts.values()) != {1}:
        raise ValueError("inline binding shape changed")
    return ast.fix_missing_locations(tree)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", required=True)
    parser.add_argument("--side", required=True, choices=("go", "typescript"))
    parser.add_argument("--label", required=True)
    parser.add_argument("--under-build", action="store_true")
    args = parser.parse_args()
    if sys.flags.optimize:
        parser.error("profile assertions require Python without -O")
    state = private_state(args.state)
    check_outputs(args.label, args.under_build)
    launch = json.loads((state / "launch.json").read_text())
    pid, port = launch.get("pid"), 7210 if args.side == "go" else 7211
    if type(pid) is not int or pid <= 1 or type(launch.get("port")) is not int or launch["port"] != port:
        parser.error("manifest must name the fixed private port and a valid server PID")
    if Path(f"/proc/{pid}").stat().st_uid != os.getuid():
        parser.error("server PID is not owned by the current user")
    # AST constants preserve even unusual path bytes as data, never Python code.
    program = compile(bound_program(state, args.side, args.label, args.under_build), __file__ + ":inline", "exec")
    exec(program, {"__name__": "__main__", "__file__": __file__})


if __name__ == "__main__":
    main()
