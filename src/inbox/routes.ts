import express, {type Express,type Request,type Response,type RequestHandler} from "express";
import path from "node:path";
import { ZodError } from "zod";
import type { CodexProConfig } from "../config.js";
import {InboxStore,InboxError} from "./store.js";
import {renderInboxPage} from "./view.js";

export function registerInbox(app:Express,config:CodexProConfig,sameOrigin:RequestHandler,principal:(req:Request)=>string) {
  // Lazy: read-only processes/tests do not create a DB until this surface is used.
  let store:InboxStore|undefined;
  const getStore=()=>store??=new InboxStore(path.join(path.dirname(config.auditLogPath),"inbox"));
  const project=(value:unknown,optional=false)=>{
    if(value===undefined&&optional)return undefined;
    if(typeof value!=="string"||!config.projects.some(p=>p.id===value))throw new InboxError(404,"Unknown project.");return value;
  };
  const number=(value:unknown,fallback:number,min:number,max:number)=>{
    if(value===undefined)return fallback;
    if(typeof value!=="string"||!/^\d+$/.test(value)||Number(value)<min||Number(value)>max)throw new InboxError(400,"Invalid pagination.");return Number(value);
  };
  const handle=(fn:(req:Request,res:Response)=>void)=>(req:Request,res:Response)=>{
    try{fn(req,res);}catch(e){res.status(e instanceof InboxError?e.status:e instanceof ZodError?400:500).json({schema_version:1,error:e instanceof InboxError?e.message:e instanceof ZodError?"Invalid inbox payload.":"Inbox unavailable."});}
  };
  app.get("/inbox/v1/items",handle((req,res)=>{
    const state=req.query.status;
    if(state!==undefined&&state!=="pending"&&state!=="answered")throw new InboxError(400,"Invalid status.");
    const consumer=req.query.consumer;
    if(consumer!==undefined&&(typeof consumer!=="string"||!/^[A-Za-z0-9][A-Za-z0-9_.:-]{0,159}$/.test(consumer)))throw new InboxError(400,"Invalid consumer.");
    res.json({schema_version:1,...getStore().list(project(req.query.project_id,true),state,number(req.query.limit,100,1,100),number(req.query.offset,0,0,1000000),consumer)});
  }));
  app.get("/inbox/v1/events",handle((req,res)=>{
    const events=getStore().events(project(req.query.project_id,true),number(req.query.after,0,0,Number.MAX_SAFE_INTEGER),number(req.query.limit,100,1,100));
    res.json({schema_version:1,events,next_after:events.at(-1)?.sequence??number(req.query.after,0,0,Number.MAX_SAFE_INTEGER)});
  }));
  app.get("/inbox/v1/items/:project/:id",handle((req,res)=>{
    const item=getStore().get(project(req.params.project)!,String(req.params.id));if(!item)throw new InboxError(404,"Question not found.");res.json(item);
  }));
  const json=express.json({limit:"32kb"});
  app.post("/inbox/v1/items",sameOrigin,json,handle((req,res)=>{project(req.body?.project_id);res.json(getStore().publish(req.body));}));
  app.post("/inbox/v1/items/:project/:id/answers",sameOrigin,json,handle((req,res)=>{
    res.json(getStore().answer(project(req.params.project)!,String(req.params.id),req.body,principal(req)));
  }));
  app.post("/inbox/v1/items/:project/:id/deliveries",sameOrigin,json,handle((req,res)=>{
    res.json(getStore().delivered(project(req.params.project)!,String(req.params.id),req.body));
  }));
  app.get("/activity/inbox",handle((_req,res)=>{
    res.setHeader("Content-Security-Policy","default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'");
    res.type("html").send(renderInboxPage(config.projects.map(p=>({id:p.id,label:p.label}))));
  }));
  return ()=>store?.close();
}
