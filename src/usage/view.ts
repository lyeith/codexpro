/** Compact Activity dashboard component; details stay collapsed during normal monitoring. */
export const proUsageStyle = `
  .pro-usage { margin:0 0 18px; border:1px solid var(--line, #dbe3ed); border-radius:16px; background:var(--card, #fff); padding:0 18px; }
  .pro-usage > summary { list-style:none; cursor:pointer; display:flex; flex-wrap:wrap; align-items:center; gap:10px 24px; padding:15px 0; }
  .pro-usage > summary::-webkit-details-marker { display:none; }
  .pro-usage-title { display:flex; align-items:baseline; gap:10px; }
  .pro-usage-title strong { font-size:26px; font-variant-numeric:tabular-nums; }
  .pro-usage-title span { font-weight:650; }
  .pro-usage-reset { margin-left:auto; font-size:13px; color:var(--muted, #64748b); }
  .pro-usage-hint { font-size:12px; color:var(--muted, #64748b); }
  .pro-usage-body { border-top:1px solid var(--line, #dbe3ed); padding:14px 0 18px; font-size:13px; }
  .pro-usage-body p { margin:0 0 10px; line-height:1.6; }
  .pro-usage-projects { display:flex; flex-wrap:wrap; gap:8px; padding:0; list-style:none; }
  .pro-usage-projects li { border:1px solid var(--line, #dbe3ed); border-radius:8px; padding:6px 10px; }
  .pro-usage-controls { display:flex; flex-wrap:wrap; align-items:center; gap:12px; margin-top:14px; }
  .pro-usage [role=status] { color:var(--muted, #64748b); }
  .pro-usage-warning { color:#92400e; }
  @media(max-width:640px) { .pro-usage-reset { margin-left:0; width:100%; } }
`;
export const proUsageHtml = `
  <details class="pro-usage" data-pro-usage>
    <summary><span class="pro-usage-title"><strong data-pro-count>…</strong><span>Pro requests</span></span><span class="pro-usage-reset" data-pro-reset-date>Loading reset date…</span><span class="pro-usage-hint">Details & reset ▾</span></summary>
    <div class="pro-usage-body">
      <p data-pro-period></p><p data-pro-coverage></p>
      <ul class="pro-usage-projects" data-pro-projects aria-label="Requests by project"></ul>
      <p data-pro-sync></p><p data-pro-warning class="pro-usage-warning"></p>
      <div class="pro-usage-controls"><button type="button" class="button" data-pro-reset>Reset counter</button><a class="button" href="/usage/v1/pro" data-pro-json>JSON</a><span role="status" data-pro-status></span></div>
      <p style="margin-top:10px" class="pro-usage-hint">Reset starts a new count from the time you click, after checking the latest Codex weekly reset date. Request history is retained. This does not reset your OpenAI allowance.</p>
    </div>
  </details>`;
export const proUsageScript = `
  (() => {
    const root=document.querySelector('[data-pro-usage]'); if(!root)return;
    const el=(name)=>root.querySelector('[data-pro-'+name+']');
    const setText=(name,value)=>{const node=el(name);if(node.textContent!==value)node.textContent=value;};
    const local=(value)=>value?new Date(value).toLocaleString():"not available";
    const currentProject=()=>new URL(location.href).searchParams.get('project_id')||'';
    const savedKey='codexpro.pro-reset.request';
    let snapshot, busy=false, loading=false;
    async function api(path,body){
      const response=await fetch(authenticatedLocalUrl(path),{credentials:'same-origin',...(body?{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}:{})});
      const value=await response.json();if(!response.ok)throw Object.assign(new Error(value.error||'HTTP '+response.status),{status:response.status});return value;
    }
    async function refresh(){
      if(loading)return;loading=true;
      const project=currentProject();
      const route='/usage/v1/pro'+(project?'?project_id='+encodeURIComponent(project):'');
      el('json').href=authenticatedLocalUrl(route);
      try{
        const received=await api(route);
        if(project!==currentProject())return;
        snapshot=received; const settings=snapshot.settings;
        setText('count',snapshot.count.toLocaleString());
        setText('reset-date',snapshot.next_weekly_reset_at?'Weekly reset · '+local(snapshot.next_weekly_reset_at):'Weekly reset · awaiting Codex API');
        setText('period',(project?'This project':'All projects')+' · '+(snapshot.period_started_at?'Since '+local(snapshot.period_started_at):'All retained history; waiting for weekly reset date')+' · '+snapshot.all_time_count.toLocaleString()+' lifetime tracked');
        setText('coverage',snapshot.coverage_note+(snapshot.requested_mode_count?' '+snapshot.requested_mode_count+' historical submissions use the requested Pro mode because selected-mode evidence was unavailable.':''));
        const list=el('projects');
        const previous=new Map(Array.from(list.children).map(li=>[li.dataset.proProject,li]));
        let cursor=list.firstChild;
        for(const item of snapshot.projects){
          const key=item.project_id||'';
          const li=previous.get(key)||document.createElement('li');previous.delete(key);
          li.dataset.proProject=key;
          const label=(item.project_id||'Other / unmapped')+' · '+item.count;
          if(li.textContent!==label)li.textContent=label;
          if(li!==cursor)list.insertBefore(li,cursor);
          cursor=li.nextSibling;
        }
        for(const li of previous.values())li.remove();
        const collectors=snapshot.collectors;
        const syncText=collectors.length?collectors.map(x=>x.collector_id+' · '+(x.stale?'sync delayed · ':'synced ')+local(x.last_sync_at)).join(' | '):'Waiting for the laptop collector; displayed count may be incomplete.';
        const excluded=collectors.reduce((a,x)=>a+x.coverage.failed_before_send,0);
        const unconfirmed=collectors.reduce((a,x)=>a+x.coverage.unconfirmed+x.coverage.unknown_mode,0);
        setText('sync',syncText+' · '+excluded+' failures before Send excluded · '+unconfirmed+' unconfirmed or unknown-mode turns excluded');
        setText('warning',settings.limits_error||(snapshot.reset_date_stale?'Codex reset date needs a fresh check; the collector will retry.':''));
        el('reset').disabled=busy||!!settings.pending_reset;
        setText('reset',sessionStorage.getItem(savedKey)?'Retry reset request':(project?'Reset all projects’ counter':'Reset counter'));
        if(settings.pending_reset)setText('status','Reset pending · checking the Codex reset date on the laptop…');
        else if(settings.manual_reset_at)setText('status','Last manual reset · '+local(settings.manual_reset_at));
        else setText('status','Automatic reset follows the Codex API.');
      }catch(error){if(project===currentProject()){el('status').textContent='Usage unavailable: '+error.message;el('reset').disabled=true;}}
      finally{loading=false;if(project!==currentProject())refresh();}
    }
    el('reset').addEventListener('click',async()=>{
      if(busy||!snapshot)return;busy=true;el('reset').disabled=true;
      try{
        const saved=sessionStorage.getItem(savedKey);
        const body=saved?JSON.parse(saved):{schema_version:1,request_id:crypto.randomUUID(),expected_revision:snapshot.settings.revision};
        sessionStorage.setItem(savedKey,JSON.stringify(body));
        await api('/usage/v1/pro/reset',body);sessionStorage.removeItem(savedKey);
        busy=false;await refresh();
      }catch(error){
        if(error.status===409)sessionStorage.removeItem(savedKey);
        busy=false;await refresh();setText('status','Reset request: '+error.message);
      }
    });
    window.addEventListener('activityfilterchange',refresh);
    refresh();setInterval(refresh,5000);
  })();`;
