#!/usr/bin/env python3
"""Launch the Go candidate on 7210 with explicit private state and a proof manifest."""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import time
import urllib.request

def private_path(path):
    path=path.expanduser().resolve()
    home=Path.home().resolve()
    allowed=[home/".codexpro-go",home/".codexpro-go-proof"]
    if not any(path==root or root in path.parents for root in allowed):
        raise ValueError("candidate state must be beneath its explicit private Go directories")
    for forbidden in [home/".codexpro",home/".config"/"codexpro"]:
        if path==forbidden or forbidden in path.parents:
            raise ValueError("live state is forbidden")
    return path

def atomic_json(path,value):
    temporary=path.with_name(path.name+".new")
    fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    try:
        with os.fdopen(fd,"w") as handle:
            json.dump(value,handle,indent=2)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary,path)
        directory=os.open(path.parent,os.O_RDONLY|os.O_DIRECTORY)
        try: os.fsync(directory)
        finally: os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)

def start_ticks(pid):
    text=(Path("/proc")/str(pid)/"stat").read_text()
    return int(text[text.rfind(")")+2:].split()[19])

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary",required=True,type=Path)
    parser.add_argument("--state",type=Path,default=Path.home()/".codexpro-go")
    parser.add_argument("--seed-fixture",required=True,type=Path)
    parser.add_argument("--readonly-root",type=Path,default=Path("/home/spite/Projects/codexpro"))
    parser.add_argument("--ast-grep-path",type=Path)
    parser.add_argument("--work-history",type=Path)
    args=parser.parse_args()
    state=private_path(args.state)
    binary=args.binary.expanduser().resolve()
    if not binary.is_file() or not os.access(binary,os.X_OK):
        parser.error("Go binary is not executable")
    with socket.socket() as check:
        check.bind(("127.0.0.1",7210))
    state.mkdir(mode=0o700,parents=True,exist_ok=True)
    seed=private_path(args.seed_fixture)
    if not seed.is_dir() or seed==state or seed in state.parents:
        parser.error("seed must be a separate private fixture directory")
    fixture=state/"fixture"
    if not fixture.exists():
        shutil.copytree(seed,fixture,symlinks=True)
    for directory in ["created-projects","empty-codex","usage","inbox"]:
        (state/directory).mkdir(mode=0o700,exist_ok=True)
    catalog=state/"projects.json"
    if not catalog.exists():
        atomic_json(catalog,{"version":1,"defaultProject":"codexpro","projects":[
            {"id":"codexpro","label":"CodexPro readonly baseline","root":str(args.readonly_root.resolve()),"baseRef":"HEAD","maxWorktrees":64},
            {"id":"contract","label":"Private contract fixture","root":str(fixture),"baseRef":"main","maxWorktrees":16}],
            "creationRoots":[{"id":"fixtures","label":"Private created projects","root":str(state/"created-projects")}]})
    else:
        saved=json.loads(catalog.read_text())
        roots={p["id"]:Path(p["root"]).resolve() for p in saved["projects"]}
        if roots.get("contract")!=fixture or roots.get("codexpro")!=args.readonly_root.resolve():
            parser.error("existing private catalog is bound to other roots")
    limits=state/"limits.env"
    if not limits.exists():
        fd=os.open(limits,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
        with os.fdopen(fd,"w") as handle:
            handle.write("CODEXPRO_JOB_TIMEOUT_MS=5400000\n")
    env={key:value for key,value in os.environ.items() if key in {
        "PATH","HOME","USER","LOGNAME","LANG","LC_ALL","LC_CTYPE","XDG_RUNTIME_DIR",
        "DBUS_SESSION_BUS_ADDRESS","TMPDIR","GOTMPDIR"}}
    env.update({"GOMAXPROCS":"2","NO_COLOR":"1","CODEXPRO_GO_HOME":str(state)})
    command=[str(binary),"--state-dir",str(state),"--listen","127.0.0.1:7210",
             "--token-file",str(state/"http-token"),"--projects-file",str(catalog),
             "--limits-file",str(limits),"--codex-dir",str(state/"empty-codex"),
             "--host-resources-helper",str(Path.home()/".local/bin/ssd-dev")]
    if args.ast_grep_path: command+=["--ast-grep-path",str(args.ast_grep_path.resolve())]
    if args.work_history: command+=["--work-history",str(private_path(args.work_history))]
    child=subprocess.Popen(command,env=env)
    def stop(signum,frame):
        if child.poll() is None: child.send_signal(signal.SIGTERM)
    signal.signal(signal.SIGTERM,stop)
    signal.signal(signal.SIGINT,stop)
    try:
        deadline=time.monotonic()+20
        ready=False
        while time.monotonic()<deadline and child.poll() is None:
            token_path=state/"http-token"
            if token_path.exists():
                credential=token_path.read_text().strip()
                request=urllib.request.Request("http://127.0.0.1:7210/healthz",headers={"Authorization":"Bearer "+credential})
                try:
                    with urllib.request.urlopen(request,timeout=1) as response:
                        ready=response.status==200 and json.load(response).get("ok") is True
                except (OSError,ValueError): pass
                if ready: break
            time.sleep(0.1)
        if not ready: raise RuntimeError("private Go server did not become ready")
        manifest={"port":7210,"pid":child.pid,"process_start_ticks":start_ticks(child.pid),
                  "state_root":str(state),"token_file":str(state/"http-token"),
                  "config_home":str(state),"projects_file":str(catalog),
                  "creation_root":str(state/"created-projects"),"creation_root_id":"fixtures",
                  "binary_sha256":hashlib.sha256(binary.read_bytes()).hexdigest(),
                  "fixture_root":str(fixture),"project_id":"contract","binary":str(binary),
                  "audit":{"format":"segments","path":str(state/"audit")},
                  "usage_database":str(state/"usage/usage.sqlite"),
                  "inbox_database":str(state/"inbox/inbox.sqlite"),
                  "nice":os.getpriority(os.PRIO_PROCESS,child.pid),
                  "cgroup":(Path("/proc")/str(child.pid)/"cgroup").read_text().strip(),
                  "gomaxprocs":2}
        atomic_json(state/"launch.json",manifest)
        print(json.dumps({"ready":True,"port":7210,"pid":child.pid,"manifest":str(state/"launch.json")}),flush=True)
        raise SystemExit(child.wait())
    finally:
        if child.poll() is None:
            child.terminate()
            try: child.wait(timeout=25)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)

if __name__=="__main__":
    main()
