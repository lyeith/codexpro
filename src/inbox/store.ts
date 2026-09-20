import Database from "better-sqlite3";
import fs from "node:fs";
import path from "node:path";
import { z } from "zod";

const identity = z.string().regex(/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$/);
export const questionSchema = z.object({
  schema_version: z.literal(1), id: identity, project_id: identity,
  source: z.string().trim().min(1).max(160), title: z.string().trim().min(1).max(200),
  question: z.string().trim().min(1).max(2000), context: z.string().max(4000).default(""),
  options: z.array(z.string().trim().min(1).max(500)).max(6).default([]),
  recommendation: z.string().max(2000).default(""),
  blocking_scope: z.enum(["none", "ticket", "project"]),
  blocked_work: z.array(z.string().max(200)).max(12).default([]),
  source_url: z.string().url().refine(s => /^https?:\/\//.test(s)).optional()
}).strict();
export type InboxQuestion = z.infer<typeof questionSchema>;
export interface InboxItem extends InboxQuestion {
  status: "pending" | "answered";
  revision: number;
  created_at: string;
  updated_at: string;
  answer?: { text: string; at: string; by: string; request_id: string };
  deliveries: Array<{ consumer: string; revision: number; at: string }>;
}
export class InboxError extends Error {
  constructor(readonly status: number, message: string) { super(message); }
}
export const answerSchema = z.object({schema_version:z.literal(1), request_id:identity,
  expected_revision:z.number().int().positive(), answer:z.string().trim().min(1).max(8000)}).strict();
export const deliverySchema = z.object({schema_version:z.literal(1), consumer:identity, revision:z.number().int().positive()}).strict();

/** The wire format and durable store do not depend on Ralph, ChatGPT or a view. */
export class InboxStore {
  readonly db: Database.Database;
  constructor(directory: string) {
    fs.mkdirSync(directory, {recursive:true,mode:0o700});
    const file=path.join(directory,"inbox.sqlite");
    this.db=new Database(file,{timeout:5000});
    fs.chmodSync(file,0o600);
    this.db.pragma("journal_mode = WAL"); this.db.pragma("synchronous = FULL");
    this.db.exec(`CREATE TABLE IF NOT EXISTS inbox_items (project TEXT NOT NULL,id TEXT NOT NULL,status TEXT NOT NULL,updated TEXT NOT NULL,body TEXT NOT NULL,PRIMARY KEY(project,id));
      CREATE TABLE IF NOT EXISTS inbox_events (sequence INTEGER PRIMARY KEY AUTOINCREMENT,project TEXT NOT NULL,id TEXT NOT NULL,body TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS inbox_answers (project TEXT NOT NULL,id TEXT NOT NULL,request TEXT NOT NULL,payload TEXT NOT NULL,result TEXT NOT NULL,PRIMARY KEY(project,id,request));`);
  }
  get(project:string,id:string): InboxItem | undefined {
    const row=this.db.prepare("SELECT body FROM inbox_items WHERE project=? AND id=?").get(project,id) as {body:string}|undefined;
    return row ? JSON.parse(row.body) : undefined;
  }
  list(project?:string,status?:string,limit=100,offset=0,consumer?:string): {items:InboxItem[];total:number;next_offset:number|null} {
    const args=[project??null,project??null,status??null,status??null,consumer??null,consumer??null];
    const where="WHERE (? IS NULL OR project=?) AND (? IS NULL OR status=?) AND (? IS NULL OR status='pending' OR NOT EXISTS (SELECT 1 FROM json_each(json_extract(inbox_items.body,'$.deliveries')) d WHERE json_extract(d.value,'$.consumer')=? AND json_extract(d.value,'$.revision')=json_extract(inbox_items.body,'$.revision')))";
    const total=(this.db.prepare(`SELECT count(*) AS n FROM inbox_items ${where}`).get(...args) as {n:number}).n;
    const rows=this.db.prepare(`SELECT body FROM inbox_items ${where} ORDER BY status DESC,updated DESC,project,id LIMIT ? OFFSET ?`).all(...args,limit,offset) as {body:string}[];
    return {items:rows.map(r=>JSON.parse(r.body)),total,next_offset:offset+rows.length<total?offset+rows.length:null};
  }
  private save(item:InboxItem,kind:string,detail:unknown) {
    this.db.prepare("INSERT INTO inbox_items VALUES (?,?,?,?,?) ON CONFLICT(project,id) DO UPDATE SET status=excluded.status,updated=excluded.updated,body=excluded.body")
      .run(item.project_id,item.id,item.status,item.updated_at,JSON.stringify(item));
    this.db.prepare("INSERT INTO inbox_events(project,id,body) VALUES (?,?,?)").run(item.project_id,item.id,JSON.stringify({schema_version:1,kind,at:new Date().toISOString(),project_id:item.project_id,id:item.id,revision:item.revision,detail}));
  }
  publish(input:unknown): InboxItem {
    const q=questionSchema.parse(input);
    return this.db.transaction(()=>{
      const old=this.get(q.project_id,q.id);
      if(old){
        const previous=questionSchema.parse(Object.fromEntries(Object.keys(questionSchema.shape).map(k=>[k,(old as unknown as Record<string,unknown>)[k]])));
        if(JSON.stringify(previous)!==JSON.stringify(q))throw new InboxError(409,"Question ID already exists with different content; read it before posting a new question.");
        return old;
      }
      const now=new Date().toISOString();
      const item:InboxItem={...q,status:"pending",revision:1,created_at:now,updated_at:now,deliveries:[]};
      this.save(item,"question.created",q);return item;
    }).immediate();
  }
  answer(project:string,id:string,input:unknown,principal:string):InboxItem {
    const a=answerSchema.parse(input);
    return this.db.transaction(()=>{
      const prior=this.db.prepare("SELECT payload,result FROM inbox_answers WHERE project=? AND id=? AND request=?").get(project,id,a.request_id) as {payload:string,result:string}|undefined;
      if(prior){if(prior.payload!==JSON.stringify(a))throw new InboxError(409,"Answer request ID was already used for different content.");return JSON.parse(prior.result);}
      const item=this.get(project,id);if(!item)throw new InboxError(404,"Question not found.");
      if(item.revision!==a.expected_revision)throw new InboxError(409,"Question changed; refresh before answering.");
      item.revision++;item.status="answered";item.updated_at=new Date().toISOString();
      item.answer={text:a.answer,at:item.updated_at,by:principal,request_id:a.request_id};
      this.save(item,"question.answered",item.answer);
      this.db.prepare("INSERT INTO inbox_answers VALUES (?,?,?,?,?)").run(project,id,a.request_id,JSON.stringify(a),JSON.stringify(item));
      return item;
    }).immediate();
  }
  delivered(project:string,id:string,input:unknown):InboxItem {
    const d=deliverySchema.parse(input);
    return this.db.transaction(()=>{
      const item=this.get(project,id);if(!item)throw new InboxError(404,"Question not found.");
      if(!item.answer || item.revision!==d.revision)throw new InboxError(409,"Answer revision changed; read the current answer.");
      if(!item.deliveries.some(x=>x.consumer===d.consumer&&x.revision===d.revision)){
        item.deliveries=item.deliveries.filter(x=>x.consumer!==d.consumer);
        if(item.deliveries.length>=50)throw new InboxError(409,"Too many consumers.");
        const receipt={...d,at:new Date().toISOString()};item.deliveries.push(receipt);this.save(item,"answer.delivered",receipt);
      }
      return item;
    }).immediate();
  }
  events(project?:string,after=0,limit=100) {
    return (this.db.prepare("SELECT sequence,body FROM inbox_events WHERE sequence>? AND (? IS NULL OR project=?) ORDER BY sequence LIMIT ?").all(after,project??null,project??null,limit) as {sequence:number,body:string}[]).map(r=>({sequence:r.sequence,...JSON.parse(r.body)}));
  }
  close(){this.db.close();}
}
