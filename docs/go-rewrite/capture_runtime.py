#!/usr/bin/env python3
"""Read-only, metadata-only census of the live TypeScript runtime. No source-store writes."""
import argparse
import collections
import datetime
import json
import math
from pathlib import Path
import sqlite3
import subprocess

def percentiles(values):
    v=sorted(values)
    return {name:round(v[max(0,math.ceil(p*len(v))-1)],3) for name,p in
            [("p50_ms",.5),("p95_ms",.95),("p99_ms",.99)]} if v else {}

def table_metadata(path):
    if not path.exists():
        return {"present":False}
    result={"present":True,"bytes":path.stat().st_size}
    con=sqlite3.connect("file:"+str(path)+"?mode=ro",uri=True,timeout=2)
    try:
        con.execute("PRAGMA query_only=ON")
        tables=list(con.execute("SELECT name,sql FROM sqlite_master WHERE type='table' ORDER BY name"))
        result["tables"]=[]
        for name,sql in tables:
            if not name.replace("_","").isalnum():
                continue
            count=con.execute('SELECT count(*) FROM "'+name+'"').fetchone()[0]
            result["tables"].append({"name":name,"schema":sql,"rows":count})
        names={name for name,sql in tables}
        if "runs" in names:
            result["run_state_counts"]=[{"project":p,"state":s,"count":n} for p,s,n in
                 con.execute("SELECT project,state,count(*) FROM runs GROUP BY project,state")]
            rows=[]
            for rid,project,state,body in con.execute("SELECT id,project,state,body FROM runs"):
                raw=json.loads(body)
                rows.append({"id":rid,"project":project,"state":state,"body_keys":sorted(raw),
                             **{k:raw[k] for k in ("mode","createdAt","updatedAt","created_at","updated_at","last_activity_at","revision") if k in raw}})
            result["runs"]=rows
        if "events" in names:
            cols={r[1] for r in con.execute('PRAGMA table_info("events")')}
            if "at" in cols:
                row=con.execute("SELECT min(at),max(at),count(*) FROM events").fetchone()
                result["event_window"]={"oldest":row[0],"newest":row[1],"count":row[2]}
        if "operations" in names:
            row=con.execute("SELECT body FROM operations LIMIT 1").fetchone()
            result["operation_body_keys"]=sorted(json.loads(row[0])) if row else []
    finally:
        con.close()
    return result

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--out",required=True)
    a=p.parse_args()
    started=datetime.datetime.now(datetime.timezone.utc).isoformat()
    base=Path("/home/spite/.codexpro")
    journal=base/"audit/tool-calls.jsonl"
    first_stat=journal.stat()
    if first_stat.st_size>16*1024*1024:
        raise SystemExit("Refusing unbounded journal scan")
    by_tool=collections.defaultdict(list)
    by_status=collections.Counter()
    by_project=collections.Counter()
    malformed=0
    with journal.open() as handle:
        for line in handle:
            if not line.strip():
                continue
            try:
                row=json.loads(line)
            except json.JSONDecodeError:
                malformed+=1
                continue
            safe={k:row.get(k) for k in ("sequence","occurred_at","finished_at","tool_name",
                                        "project_id","status","duration_ms","operation_class")}
            by_tool[str(row.get("tool_name","unknown"))].append(safe)
            by_status[str(row.get("status","unknown"))]+=1
            by_project[str(row.get("project_id","unknown"))]+=1
    all_rows=[r for rows in by_tool.values() for r in rows]
    tools={}
    for name,rows in sorted(by_tool.items()):
        dates=sorted(str(r.get("occurred_at") or r.get("finished_at")) for r in rows)
        durations=[r["duration_ms"] for r in rows if isinstance(r["duration_ms"],(int,float))]
        tools[name]={"retained_count":len(rows),"oldest":dates[0],"newest":dates[-1],
                     "since_2026_10_30":sum(d>="2026-10-30" for d in dates),
                     "since_2026_10_01":sum(d>="2026-10-01" for d in dates),
                     "statuses":dict(collections.Counter(r["status"] for r in rows)),
                     **percentiles(durations)}
    index_path=journal.with_name(journal.name+".index.json")
    index=json.loads(index_path.read_text()) if index_path.exists() else {}
    main_pid=int(subprocess.check_output(["systemctl","--user","show","codexpro.service","--property=MainPID","--value"],text=True))
    children=Path(f"/proc/{main_pid}/task/{main_pid}/children").read_text().split()
    server_pid=int(children[0]) if children else main_pid
    env=dict(item.split("=",1) for item in Path(f"/proc/{server_pid}/environ").read_text().split("\0") if "=" in item)
    allowed={"CODEXPRO_HOME","CODEXPRO_AUDIT_LOG","CODEXPRO_WORK_DIR","CODEXPRO_WORKTREE_ROOT",
             "CODEXPRO_JOBS_DIR","CODEXPRO_ENV_FILE","CODEXPRO_PROJECTS_FILE","CODEXPRO_HOST_RESOURCES_HELPER",
             "CODEXPRO_WORK_MODE","CODEXPRO_WORKTREE_MODE","CODEXPRO_HANDOFF_MODE","CODEXPRO_CODEX_SESSIONS",
             "CODEXPRO_TOOL_MODE","CODEXPRO_BASH_MODE","CODEXPRO_AUDIT_MODE","CODEXPRO_LOG_REQUESTS"}
    report={"captured_at":started,"source_journal":str(journal),"source_bytes":first_stat.st_size,
            "source_inode":first_stat.st_ino,"malformed_lines":malformed,"retained_records":len(all_rows),
            "sequence_min":min((r["sequence"] for r in all_rows if isinstance(r["sequence"],int)),default=None),
            "sequence_max":max((r["sequence"] for r in all_rows if isinstance(r["sequence"],int)),default=None),
            "tools":tools,"statuses":dict(by_status),"projects":dict(by_project),
            "retention_index":index,
            "server_pid":server_pid,"selected_environment":{k:v for k,v in env.items() if k in allowed},
            "work":table_metadata(base/"work/work.sqlite"),
            "usage":table_metadata(base/"audit/usage/usage.sqlite"),
            "inbox":table_metadata(base/"audit/inbox/inbox.sqlite"),
            "limits":["Retained per-project history is not a continuous all-traffic archive.",
                      "Zero observations do not prove a tool was never used.",
                      "HTTP requests are not assumed to appear in MCP action history.",
                      "No raw request/response text, commands, document contents or credentials are exported."]}
    report["finished_at"]=datetime.datetime.now(datetime.timezone.utc).isoformat()
    out=Path(a.out)
    out.parent.mkdir(parents=True,exist_ok=True)
    out.write_text(json.dumps(report,indent=2)+"\n")
    print(json.dumps({"out":str(out),"retained_records":len(all_rows),"tools":tools,
                     "run_state_counts":report["work"].get("run_state_counts"),
                     "event_window":report["work"].get("event_window")},indent=2))

if __name__=="__main__":
    main()
