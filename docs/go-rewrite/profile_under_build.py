#!/usr/bin/env python3
"""Run the read-only profile while a bounded two-worker compiler build is active."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--url",default="http://127.0.0.1:7200/mcp")
    p.add_argument("--token-file",required=True)
    p.add_argument("--label",required=True)
    p.add_argument("--out",required=True)
    p.add_argument("--pid",type=int)
    p.add_argument("--workspace",default="ws_4d43e03b8d338283c2776d95")
    a=p.parse_args()
    evidence=Path(a.out)
    evidence.parent.mkdir(parents=True,exist_ok=True)
    run_dir=Path("/tmp")/("sp6-load-"+a.label)
    run_dir.mkdir(mode=0o700,exist_ok=True)
    env=dict(os.environ,GOMAXPROCS="2",GOCACHE=str(run_dir/"gocache"))
    load_command=["nice","-n","10","timeout","90s","go","build","-p","2","-a",
                  "-o",str(run_dir/"compiler"),"cmd/compile"]
    started=time.time()
    with (run_dir/"build.log").open("w") as log:
        load=subprocess.Popen(load_command,env=env,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
        profile=None
        try:
            time.sleep(2)
            command=[sys.executable,str(Path(__file__).with_name("profile.py")),
                     "--url",a.url,"--token-file",a.token_file,"--workspace",a.workspace,
                     "--count","160","--concurrency","2","--duration","180",
                     "--label",a.label,"--out",str(evidence)]
            if a.pid:
                command.extend(["--pid",str(a.pid)])
            profile=subprocess.run(command,timeout=205)
            load_code=load.wait(timeout=95)
        finally:
            if load.poll() is None:
                os.killpg(load.pid,signal.SIGTERM)
                try:
                    load.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(load.pid,signal.SIGKILL)
                    load.wait(timeout=3)
        record={"load_command":load_command,"load_started_epoch":started,
                "elapsed_seconds":time.time()-started,"load_pid":load.pid,
                "load_exit_code":load.returncode,
                "bounded_timeout_expected":load.returncode==124,
                "profile_exit_code":profile.returncode if profile else None,
                "build_log":str(run_dir/"build.log")}
        evidence.with_name(evidence.stem+"-load.json").write_text(json.dumps(record,indent=2)+"\n")
        print(json.dumps(record,indent=2))
        return 0 if profile and profile.returncode==0 else 1

if __name__=="__main__":
    raise SystemExit(main())
