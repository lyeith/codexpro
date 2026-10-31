#!/usr/bin/env python3
"""Run an immutable TypeScript release with private state and credentials on 7211."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess

RELEASE=Path("/home/spite/apps/codexpro/releases/job45-dashboard-20261030-44bb76b")

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state",default="/home/spite/.codexpro-go-proof/ts")
    parser.add_argument("--port",type=int,default=7211)
    parser.add_argument("--cpu-profile",action="store_true")
    args=parser.parse_args()
    if args.port in (7200,7210):
        parser.error("private TypeScript instance may not bind live or Go port")
    state=Path(args.state).resolve()
    allowed_parent=Path("/home/spite/.codexpro-go-proof").resolve()
    if not (str(state).startswith("/tmp/sp6-") or allowed_parent in state.parents):
        parser.error("private TypeScript state must be in /tmp/sp6-* or ~/.codexpro-go-proof/")
    state.mkdir(mode=0o700,parents=True,exist_ok=True)
    token_file=state/"http-token"
    if not token_file.exists():
        fd=os.open(token_file,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
        with os.fdopen(fd,"w") as handle:
            handle.write(secrets.token_urlsafe(48)+"\n")
    (state/"limits.env").touch()
    (state/"cpu").mkdir(exist_ok=True)
    fixture=state/"fixture"
    fixture.mkdir(exist_ok=True)
    if not (fixture/".git").exists():
        subprocess.run(["git","init","-q","-b","main",str(fixture)],check=True)
        subprocess.run(["git","-C",str(fixture),"config","user.name","Contract Fixture"],check=True)
        subprocess.run(["git","-C",str(fixture),"config","user.email","fixture@example.invalid"],check=True)
        (fixture/"README.md").write_text("# Contract fixture\n\nalpha beta gamma\n")
        (fixture/"sample.ts").write_text("export function add(a: number, b: number) { return a + b; }\n")
        (fixture/"sample.json").write_text('{"server":{"port":7211},"enabled":true}\n')
        subprocess.run(["git","-C",str(fixture),"add","."],check=True)
        subprocess.run(["git","-C",str(fixture),"commit","-qm","test: create contract fixture"],check=True)
    catalog={"version":1,"defaultProject":"codexpro","projects":[
        {"id":"codexpro","label":"CodexPro readonly baseline","root":"/home/spite/Projects/codexpro","baseRef":"HEAD","maxWorktrees":64},
        {"id":"contract","label":"Private contract fixture","root":str(fixture),"baseRef":"main","maxWorktrees":16}],
        "creationRoots":[{"id":"fixtures","label":"Private created projects","root":str(state/"created-projects")}]}
    (state/"created-projects").mkdir(exist_ok=True)
    (state/"projects.json").write_text(json.dumps(catalog,indent=2)+"\n")
    env={key:value for key,value in os.environ.items()
         if not key.startswith(("CODEXPRO_","CODEBASE_BRIDGE_","LISTEN_"))}
    env.update({
        "CODEXPRO_HOME":str(state/"state"),
        "CODEXPRO_ENV_FILE":str(state/"limits.env"),
        "CODEXPRO_PROJECTS_FILE":str(state/"projects.json"),
        "CODEXPRO_HTTP_TOKEN":token_file.read_text().strip(),
        "CODEXPRO_HOST":"127.0.0.1","CODEXPRO_PORT":str(args.port),
        "CODEXPRO_BASH_MODE":"full","CODEXPRO_WRITE_MODE":"workspace",
        "CODEXPRO_TOOL_MODE":"standard","CODEXPRO_WORK_MODE":"on",
        "CODEXPRO_WORK_MANAGEMENT":"true","CODEXPRO_WORKTREE_MODE":"off",
        "CODEXPRO_CODEX_SESSIONS":"off","CODEXPRO_CODEX_DIR":str(state/"empty-codex"),
        "CODEXPRO_AUDIT_MODE":"metadata","CODEXPRO_DEBUG_ACTIVITY":"metadata",
        "CODEXPRO_INHERIT_ENV":"0","CODEXPRO_HANDOFF_MODE":"off",
        "CODEXPRO_JOB_TIMEOUT_MS":"5400000","CODEXPRO_MAX_JOBS":"12",
        "CODEXPRO_HOST_RESOURCES_HELPER":"/home/spite/.local/bin/ssd-dev",
        "CODEXPRO_JOB_SCOPES":"1","CODEXPRO_MAX_JOBS_PER_WORKSPACE":"6"})
    command=["node"]
    if args.cpu_profile:
        command.extend([str(Path(__file__).resolve().with_name("profile_launcher.mjs")),
                        str(RELEASE/"dist/http.js"),str(state/"cpu"/f"private-{os.getpid()}.cpuprofile")])
    else:
        command.append(str(RELEASE/"dist/http.js"))
    receipt={"release":str(RELEASE),"state":str(state),"port":args.port,"pid":os.getpid(),
             "started_by":"SP-6 separate process, never systemd service","cpu_profile":args.cpu_profile}
    (state/"launch.json").write_text(json.dumps(receipt,indent=2)+"\n")
    print(json.dumps(receipt),flush=True)
    os.chdir(fixture)
    os.execvpe(command[0],command,env)

if __name__=="__main__":
    main()
