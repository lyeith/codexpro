import Database from "better-sqlite3";
import fs from "node:fs";
import path from "node:path";
import {z} from "zod";

const id = z.string().regex(/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,199}$/);
const date = z.string().datetime({offset:true});
const WEEK = 7*24*60*60*1000;
export const submissionSchema = z.object({
  query_id:id, turn_id:id, project_id:id.nullable(), submitted_at:date,
  mode_evidence:z.enum(["selected","requested"])
}).strict();
export const batchSchema = z.object({
  schema_version:z.literal(1), collector_id:id, submissions:z.array(submissionSchema).max(100),
  coverage:z.object({unconfirmed:z.number().int().min(0), failed_before_send:z.number().int().min(0),
    unknown_mode:z.number().int().min(0), oldest_record_at:date.nullable()}).strict()
}).strict();
export const resetSchema = z.object({schema_version:z.literal(1), request_id:id, expected_revision:z.number().int().positive()}).strict();
export const limitsSchema = z.object({
  schema_version:z.literal(1), checked_at:date, reset_request_id:id.optional(),
  weekly:z.object({resets_at:date, window_minutes:z.literal(10080), used_percent:z.number().min(0).max(100)}).strict().nullable()
}).strict();
interface PendingReset {request_id:string; requested_at:string; by:string;}
export interface UsageSettings {
  revision:number;
  weekly_anchor_at:string|null;
  manual_reset_at:string|null;
  pending_reset:PendingReset|null;
  limits_checked_at:string|null;
  limits_verified_at:string|null;
  limits_error:string|null;
}
export class UsageError extends Error {constructor(readonly status:number,message:string){super(message);}}

/** Durable submission receipts. Resets move a counting boundary without deleting history. */
export class ProUsageStore {
  readonly db:Database.Database;
  constructor(directory:string, readonly clock:()=>number=Date.now) {
    fs.mkdirSync(directory,{recursive:true,mode:0o700});
    const file=path.join(directory,"usage.sqlite");
    this.db=new Database(file,{timeout:5000}); fs.chmodSync(file,0o600);
    this.db.pragma("journal_mode = WAL"); this.db.pragma("synchronous = FULL");
    this.db.exec(`CREATE TABLE IF NOT EXISTS pro_submissions(query_id TEXT NOT NULL,turn_id TEXT NOT NULL,project TEXT,submitted_at TEXT NOT NULL,submitted_ms INTEGER NOT NULL,mode_evidence TEXT NOT NULL,PRIMARY KEY(query_id,turn_id));
      CREATE INDEX IF NOT EXISTS pro_submissions_time ON pro_submissions(submitted_ms,project);
      CREATE TABLE IF NOT EXISTS usage_settings(id INTEGER PRIMARY KEY CHECK(id=1),body TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS usage_collectors(id TEXT PRIMARY KEY,body TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS usage_changes(sequence INTEGER PRIMARY KEY AUTOINCREMENT,body TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS usage_requests(id TEXT PRIMARY KEY,payload TEXT NOT NULL,result TEXT NOT NULL);`);
    this.db.prepare("INSERT OR IGNORE INTO usage_settings VALUES (1,?)").run(JSON.stringify({
      revision:1,weekly_anchor_at:null,manual_reset_at:null,pending_reset:null,
      limits_checked_at:null,limits_verified_at:null,limits_error:null
    }));
  }
  settings():UsageSettings {
    return JSON.parse((this.db.prepare("SELECT body FROM usage_settings WHERE id=1").get() as {body:string}).body);
  }
  private save(settings:UsageSettings) {this.db.prepare("UPDATE usage_settings SET body=? WHERE id=1").run(JSON.stringify(settings));}
  private record(value:unknown) {this.db.prepare("INSERT INTO usage_changes(body) VALUES (?)").run(JSON.stringify(value));}
  ingest(input:unknown) {
    const batch=batchSchema.parse(input), now=this.clock();
    return this.db.transaction(()=>{
      let inserted=0;
      for(const value of batch.submissions) {
        const at=Date.parse(value.submitted_at);
        if(at<0||at>now+300000) throw new UsageError(400,"Submission timestamp is outside the supported range.");
        const existing=this.db.prepare("SELECT submitted_ms,project,mode_evidence FROM pro_submissions WHERE query_id=? AND turn_id=?").get(value.query_id,value.turn_id) as {submitted_ms:number,project:string|null,mode_evidence:string}|undefined;
        if(existing) {
          if(existing.submitted_ms!==at) throw new UsageError(409,"Conflicting timestamp for the same submitted turn.");
          if(existing.project&&value.project_id&&existing.project!==value.project_id) throw new UsageError(409,"Conflicting project for the same submitted turn.");
          this.db.prepare("UPDATE pro_submissions SET project=COALESCE(project,?),mode_evidence=CASE WHEN mode_evidence='selected' THEN mode_evidence ELSE ? END WHERE query_id=? AND turn_id=?").run(value.project_id,value.mode_evidence,value.query_id,value.turn_id);
        } else {
          this.db.prepare("INSERT INTO pro_submissions VALUES (?,?,?,?,?,?)").run(value.query_id,value.turn_id,value.project_id,new Date(at).toISOString(),at,value.mode_evidence); inserted++;
        }
      }
      const collector={collector_id:batch.collector_id,last_sync_at:new Date(now).toISOString(),coverage:batch.coverage};
      this.db.prepare("INSERT INTO usage_collectors VALUES (?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body").run(batch.collector_id,JSON.stringify(collector));
      return {schema_version:1,accepted:batch.submissions.length,inserted};
    }).immediate();
  }
  reset(input:unknown, principal:string) {
    const request=resetSchema.parse(input);
    return this.db.transaction(()=>{
      const payload=JSON.stringify(request);
      const prior=this.db.prepare("SELECT payload,result FROM usage_requests WHERE id=?").get(request.request_id) as {payload:string,result:string}|undefined;
      if(prior) {
        if(prior.payload!==payload) throw new UsageError(409,"Request ID was used for a different counter change.");
        return JSON.parse(prior.result) as UsageSettings;
      }
      const settings=this.settings();
      if(settings.revision!==request.expected_revision) throw new UsageError(409,"Counter settings changed; refresh before trying again.");
      if(settings.pending_reset) throw new UsageError(409,"A reset is already waiting for the Codex reset date.");
      settings.pending_reset={request_id:request.request_id,requested_at:new Date(this.clock()).toISOString(),by:principal};
      settings.revision++; this.save(settings);
      this.record({kind:"reset.requested",...settings.pending_reset});
      this.db.prepare("INSERT INTO usage_requests VALUES (?,?,?)").run(request.request_id,payload,JSON.stringify(settings));
      return settings;
    }).immediate();
  }
  /** Laptop collector reads Codex account/rateLimits/read. No account credentials reach this server. */
  syncLimits(input:unknown) {
    const value=limitsSchema.parse(input), now=this.clock(), checked=Date.parse(value.checked_at);
    if(checked>now+30000||now-checked>120000) throw new UsageError(400,"Rate-limit observation is stale or has an invalid clock.");
    if(value.weekly) {
      const reset=Date.parse(value.weekly.resets_at);
      if(reset<=checked||reset>checked+WEEK+300000) throw new UsageError(400,"Codex weekly reset date is outside the expected window.");
    }
    return this.db.transaction(()=>{
      const settings=this.settings();
      if(settings.limits_checked_at&&checked<Date.parse(settings.limits_checked_at)) throw new UsageError(409,"A newer rate-limit observation is already saved.");
      settings.limits_checked_at=new Date(checked).toISOString();
      settings.limits_error=value.weekly?null:"Could not verify the Codex weekly reset date. Check the laptop collector and Codex login; pending resets will retry automatically.";
      if(value.weekly) {
        const anchor=new Date(value.weekly.resets_at).toISOString();
        settings.limits_verified_at=settings.limits_checked_at;
        if(settings.weekly_anchor_at!==anchor) {
          settings.weekly_anchor_at=anchor; settings.revision++;
          this.record({kind:"weekly.synced",at:settings.limits_checked_at,next_reset_at:anchor});
        }
        const pending=settings.pending_reset;
        // The date must be fetched after the click; an old cached response cannot complete a reset.
        if(pending&&value.reset_request_id===pending.request_id&&checked>=Date.parse(pending.requested_at)) {
          settings.manual_reset_at=pending.requested_at; settings.pending_reset=null; settings.revision++;
          this.record({kind:"reset.completed",...pending,verified_at:settings.limits_checked_at,next_reset_at:anchor});
        }
      }
      this.save(settings); return settings;
    }).immediate();
  }
  snapshot(project?:string) {
    const now=this.clock(), settings=this.settings();
    const anchor=settings.weekly_anchor_at?Date.parse(settings.weekly_anchor_at):null;
    // Use only API-observed boundaries. After a deadline, refresh the API before predicting another week.
    const weeklyStart=anchor===null?null:(now<anchor?anchor-WEEK:anchor);
    const since=Math.max(0,weeklyStart??0,settings.manual_reset_at?Date.parse(settings.manual_reset_at):0);
    const counts=(where:string,args:unknown[])=>this.db.prepare(`SELECT count(*) AS count,COALESCE(sum(mode_evidence='requested'),0) AS requested_mode_count,min(submitted_at) AS first_submission_at,max(submitted_at) AS last_submission_at FROM pro_submissions WHERE ${where}`).get(...args) as {count:number,requested_mode_count:number,first_submission_at:string|null,last_submission_at:string|null};
    const filter="(? IS NULL OR project=?)", scope=[project??null,project??null];
    const current=counts(`submitted_ms>=? AND submitted_ms<=? AND ${filter}`,[since,now,...scope]);
    const all=counts(`submitted_ms<=? AND ${filter}`,[now,...scope]);
    const projects=this.db.prepare(`SELECT project AS project_id,count(*) AS count FROM pro_submissions WHERE submitted_ms>=? AND submitted_ms<=? AND ${filter} GROUP BY project ORDER BY count DESC,project`).all(since,now,...scope);
    const collectors=(this.db.prepare("SELECT body FROM usage_collectors ORDER BY id").all() as {body:string}[]).map(r=>{
      const collector=JSON.parse(r.body); return {...collector,stale:now-Date.parse(collector.last_sync_at)>120000};
    });
    return {schema_version:1,generated_at:new Date(now).toISOString(),settings,
      period_started_at:since?new Date(since).toISOString():null,
      next_weekly_reset_at:anchor!==null&&now<anchor?new Date(anchor).toISOString():null,
      reset_date_stale:anchor===null||now>=anchor||!settings.limits_verified_at||now-Date.parse(settings.limits_verified_at)>600000,
      ...current,all_time_count:all.count,projects,collectors,
      coverage_note:"SessionPilot Pro submissions, including in-flight requests and requests that later fail or are canceled. Pre-send failures and unconfirmed sends are excluded. This count is not OpenAI's remaining allowance."};
  }
  history() {return (this.db.prepare("SELECT sequence,body FROM usage_changes ORDER BY sequence DESC LIMIT 30").all() as {sequence:number,body:string}[]).map(r=>({sequence:r.sequence,...JSON.parse(r.body)}));}
  close() {this.db.close();}
}
