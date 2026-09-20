import express,{type Express,type Request,type Response,type RequestHandler} from "express";
import path from "node:path";
import {ZodError} from "zod";
import type {CodexProConfig} from "../config.js";
import {ProUsageStore,UsageError} from "./store.js";

export function registerProUsage(app:Express,config:CodexProConfig,sameOrigin:RequestHandler,principal:(req:Request)=>string){
  let store:ProUsageStore|undefined;
  const db=()=>store??=new ProUsageStore(path.join(path.dirname(config.auditLogPath),"usage"));
  const handle=(fn:(req:Request,res:Response)=>void)=>(req:Request,res:Response)=>{
    try{fn(req,res);}catch(e){res.status(e instanceof UsageError?e.status:e instanceof ZodError?400:500).json({schema_version:1,error:e instanceof UsageError?e.message:e instanceof ZodError?"Invalid usage request.":"Usage storage unavailable."});}
  };
  const project=(value:unknown)=>{
    if(value===undefined)return undefined;
    if(typeof value!=="string"||!config.projects.some(p=>p.id===value))throw new UsageError(404,"Unknown project.");return value;
  };
  app.get("/usage/v1/pro",handle((req,res)=>res.json(db().snapshot(project(req.query.project_id)))));
  app.get("/usage/v1/pro/history",handle((_req,res)=>res.json({schema_version:1,changes:db().history()})));
  const json=express.json({limit:"128kb"});
  app.post("/usage/v1/pro/submissions",sameOrigin,json,handle((req,res)=>{
    // Unknown historical projects remain identifiable; current dashboard labels
    // are presentation only, and removing a project must not remove usage.
    res.json(db().ingest(req.body));
  }));
  app.post("/usage/v1/pro/reset",sameOrigin,json,handle((req,res)=>res.json({schema_version:1,settings:db().reset(req.body,principal(req))})));
  app.post("/usage/v1/pro/limits",sameOrigin,json,handle((req,res)=>res.json({schema_version:1,settings:db().syncLimits(req.body)})));
}
