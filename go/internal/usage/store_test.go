package usage

import("context";"encoding/json";"errors";"path/filepath";"testing";"time")
func requireStatus(t *testing.T,err error,status int){t.Helper();var e *Error;if !errors.As(err,&e)||e.Status!=status{t.Fatalf("wanted status %d, got %v",status,err)}}
func TestSubmissionIdentityEnrichmentAndAtomicConflict(t *testing.T){
 ctx:=context.Background();now:=time.Date(2026,10,31,12,0,0,0,time.UTC)
 s,err:=Open(t.TempDir(),func()time.Time{return now});if err!=nil{t.Fatal(err)};defer s.Close()
 payload:=func(subs string)[]byte{return []byte("{\"schema_version\":1,\"collector_id\":\"c1\",\"submissions\":"+subs+",\"coverage\":{\"unconfirmed\":0,\"failed_before_send\":0,\"unknown_mode\":0,\"oldest_record_at\":null}}")}
 first:=payload("[{\"query_id\":\"q1\",\"turn_id\":\"t1\",\"project_id\":null,\"submitted_at\":\"2026-10-31T11:00:00Z\",\"mode_evidence\":\"requested\"}]")
 if r,e:=s.Ingest(ctx,first);e!=nil||r["inserted"]!=1{t.Fatalf("%v %v",r,e)}
 second:=payload("[{\"query_id\":\"q1\",\"turn_id\":\"t1\",\"project_id\":\"retired-project\",\"submitted_at\":\"2026-10-31T11:00:00+00:00\",\"mode_evidence\":\"selected\"}]")
 if r,e:=s.Ingest(ctx,second);e!=nil||r["inserted"]!=0{t.Fatalf("%v %v",r,e)}
 if _,e:=s.Ingest(ctx,first);e!=nil{t.Fatal(e)}
 snapshot,e:=s.Snapshot(ctx,nil);if e!=nil{t.Fatal(e)}
 if snapshot["count"]!=int64(1)||snapshot["requested_mode_count"]!=int64(0){t.Fatalf("%#v",snapshot)}
 conflictBatch:=payload("[{\"query_id\":\"q2\",\"turn_id\":\"t2\",\"project_id\":null,\"submitted_at\":\"2026-10-31T10:00:00Z\",\"mode_evidence\":\"selected\"},{\"query_id\":\"q1\",\"turn_id\":\"t1\",\"project_id\":\"different\",\"submitted_at\":\"2026-10-31T11:00:00Z\",\"mode_evidence\":\"selected\"}]")
 _,e=s.Ingest(ctx,conflictBatch);requireStatus(t,e,409)
 snapshot,e=s.Snapshot(ctx,nil);if e!=nil||snapshot["count"]!=int64(1){t.Fatalf("transaction leaked: %#v %v",snapshot,e)}
}
func TestResetVerifiedBoundaryAndReplaySurviveRestart(t *testing.T){
 ctx:=context.Background();dir:=filepath.Join(t.TempDir(),"usage");now:=time.Date(2026,10,31,12,0,0,0,time.UTC)
 clock:=func()time.Time{return now};s,e:=Open(dir,clock);if e!=nil{t.Fatal(e)}
 request:=[]byte("{\"schema_version\":1,\"request_id\":\"reset-1\",\"expected_revision\":1}")
 pending,e:=s.RequestReset(ctx,request,"operator");if e!=nil||pending.PendingReset==nil||pending.Revision!=2{t.Fatalf("%#v %v",pending,e)}
 oldObservation:=[]byte("{\"schema_version\":1,\"checked_at\":\"2026-10-31T11:59:59Z\",\"reset_request_id\":\"reset-1\",\"weekly\":{\"resets_at\":\"2026-11-06T12:00:00Z\",\"window_minutes\":10080,\"used_percent\":20}}")
 settings,e:=s.SyncLimits(ctx,oldObservation);if e!=nil||settings.PendingReset==nil{t.Fatalf("cached observation completed reset: %#v %v",settings,e)}
 now=now.Add(time.Second)
 observation:=[]byte("{\"schema_version\":1,\"checked_at\":\"2026-10-31T12:00:01Z\",\"reset_request_id\":\"reset-1\",\"weekly\":{\"resets_at\":\"2026-11-06T12:00:00Z\",\"window_minutes\":10080,\"used_percent\":20}}")
 settings,e=s.SyncLimits(ctx,observation);if e!=nil||settings.PendingReset!=nil||settings.ManualResetAt==nil||*settings.ManualResetAt!="2026-10-31T12:00:00.000Z"{t.Fatalf("%#v %v",settings,e)}
 if e=s.Close();e!=nil{t.Fatal(e)};s,e=Open(dir,clock);if e!=nil{t.Fatal(e)};defer s.Close()
 replay,e:=s.RequestReset(ctx,request,"other");if e!=nil||replay.PendingReset==nil||replay.Revision!=2{t.Fatalf("not original receipt: %#v %v",replay,e)}
 _,e=s.RequestReset(ctx,[]byte("{\"schema_version\":1,\"request_id\":\"reset-1\",\"expected_revision\":2}"),"operator");requireStatus(t,e,409)
 hist,e:=s.History(ctx);if e!=nil||len(hist)!=3{t.Fatalf("%#v %v",hist,e)}
}
func TestStrictDTOAndInvalidTime(t *testing.T){
 ctx:=context.Background();now:=time.Date(2026,10,31,12,0,0,0,time.UTC)
 s,e:=Open(t.TempDir(),func()time.Time{return now});if e!=nil{t.Fatal(e)};defer s.Close()
 for _,data:=range []string{
  "{\"schema_version\":1,\"request_id\":\"x\",\"expected_revision\":1,\"extra\":true}",
  "{\"schema_version\":1,\"request_id\":\"x\",\"expected_revision\":1.5}",
 }{_,e=s.RequestReset(ctx,[]byte(data),"p");requireStatus(t,e,400)}
 _,e=s.SyncLimits(ctx,[]byte("{\"schema_version\":1,\"checked_at\":\"2026-10-31T11:57:00Z\",\"weekly\":null}"));requireStatus(t,e,400)
 snapshot,e:=s.Snapshot(ctx,nil);if e!=nil{t.Fatal(e)}
 b,_:=json.Marshal(snapshot);var v map[string]any;_ = json.Unmarshal(b,&v)
 if v["period_started_at"]!=nil||v["next_weekly_reset_at"]!=nil||v["reset_date_stale"]!=true{t.Fatalf("%s",b)}
}
