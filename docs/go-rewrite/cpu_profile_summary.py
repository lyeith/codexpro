#!/usr/bin/env python3
"""Summarize V8 CPU samples from a private instance without copying raw profiles."""
import argparse
import collections
import json
from pathlib import Path

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("profile")
    p.add_argument("--out",required=True)
    a=p.parse_args()
    data=json.loads(Path(a.profile).read_text())
    nodes={n["id"]:n for n in data["nodes"]}
    parent={child:n["id"] for n in data["nodes"] for child in n.get("children",[])}
    weights=collections.Counter()
    categories=collections.Counter()
    paths=collections.Counter()
    samples=data.get("samples",[])
    deltas=data.get("timeDeltas",[1]*len(samples))
    for sample,delta in zip(samples,deltas):
        n=nodes[sample]
        call=n.get("callFrame",{})
        name=call.get("functionName") or "(anonymous)"
        url=call.get("url","")
        key=(name,url,call.get("lineNumber",0)+1)
        weights[key]+=delta
        lineage=[]
        current=sample
        while current in nodes:
            f=nodes[current].get("callFrame",{})
            lineage.append((f.get("url",""),f.get("functionName","")))
            if current not in parent:
                break
            current=parent[current]
        category="other"
        if name=="(idle)":
            category="idle"
        elif any("/audit." in f for f,nm in lineage):
            category="audit/activity"
        elif any("/analysis/" in f for f,nm in lineage):
            category="repository-analysis"
        elif any("/worktrees/" in f or "/work/" in f for f,nm in lineage):
            category="workspaces/work"
        elif any("/jobs." in f or "/job" in f for f,nm in lineage):
            category="jobs/process"
        elif any("/gitOps." in f or "/gitPaths." in f for f,nm in lineage):
            category="git"
        elif any("/fsOps." in f or "/searchOps." in f for f,nm in lineage):
            category="files/search"
        elif name=="(garbage collector)":
            category="garbage-collection"
        categories[category]+=delta
        if url:
            paths[url]+=delta
    total=sum(deltas)
    active=total-categories["idle"]
    report={"profile_file":Path(a.profile).name,"profile_duration_us":data.get("endTime",0)-data.get("startTime",0),
            "samples":len(samples),"sampled_us":total,"active_sampled_us":active,
            "categories_us":dict(categories),
            "top_self_frames":[{"function":name,"file":url,"line":line,"self_us":weight,
                                "active_percent":round(weight/active*100,3) if active else 0}
                               for (name,url,line),weight in [(k,v) for k,v in weights.most_common() if k[0] != "(idle)"][:40]],
            "top_self_files":[{"file":url,"self_us":weight} for url,weight in paths.most_common(20)],
            "limits":"Statistical V8 samples only; child-process CPU and filesystem wait are not complete CPU attribution."}
    Path(a.out).write_text(json.dumps(report,indent=2)+"\n")
    print(json.dumps(report,indent=2))

if __name__=="__main__":
    main()
