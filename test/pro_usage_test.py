import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('usage',Path(__file__).parents[1]/'scripts/codexpro-pro-usage.py')
u=importlib.util.module_from_spec(spec);spec.loader.exec_module(u)
URL='https://chatgpt.com/g/g-p-'+'a'*32+'-mud/project'
NOW=1790000000

def query(turns):return {'id':'q_test','adapterId':'chatgpt','projectUrl':URL,'turns':turns}
def turn(key,**extra):return {'id':key,'createdAt':u.iso(NOW-100),'effort':'Pro',**extra}
def event(key,mode='Pro'):
 return {'kind':'query.event','payload':{'queryId':'q_test','adapterId':'chatgpt','event':{'name':'turn-submitted','at':u.iso(NOW),'turnId':key,'snapshot':{'href':URL,'effort':mode}}}}

class UsageTests(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name);self.pilot=self.root/'pilot';self.pilot.mkdir();(self.pilot/'archive').mkdir();self.ledger=u.Ledger(self.root/'ledger')
 def tearDown(self):self.ledger.db.close();self.temp.cleanup()
 def state(self,turns): (self.pilot/'state.json').write_text(json.dumps({'queries':{'q_test':query(turns)}}))
 def test_accepted_failed_cancelled_inflight_retry_and_presend(self):
  self.state([turn('running',state='streaming',submittedAt=u.iso(NOW)),turn('failed',state='failed',submittedAt=u.iso(NOW)),turn('canceled',state='canceled',submittedAt=u.iso(NOW)),turn('retry',state='completed',retriedFromTurnId='failed',submittedAt=u.iso(NOW)),turn('pre',state='failed',error='ChatGPT submission preparation failed before Send: button unavailable'),turn('uncertain',state='completed'),turn('nonpro',submittedAt=u.iso(NOW),acceptanceEvidence={'selectedEffort':'Thinking'})])
  self.ledger.scan(self.pilot);pending,coverage=self.ledger.pending({u.project_key(URL):'mud'})
  self.assertEqual({x[0]['turn_id'] for x in pending},{'running','failed','canceled','retry'})
  self.assertEqual(coverage['failed_before_send'],1);self.assertEqual(coverage['unconfirmed'],1)
  self.assertTrue(all(x[0]['project_id']=='mud' for x in pending))
  self.ledger.acknowledge(pending);self.ledger.scan(self.pilot);self.assertEqual(self.ledger.pending({u.project_key(URL):'mud'})[0],[])
 def test_archives_log_rotation_partial_lines_and_crash_recovery(self):
  self.state([turn('current',submittedAt=u.iso(NOW))]);archived=query([turn('current',submittedAt=u.iso(NOW)),turn('older',submittedAt=u.iso(NOW-1000))]);(self.pilot/'archive/queries-2026-09.jsonl').write_text(json.dumps(archived)+'\n')
  line=json.dumps(event('event-only')).encode();log=self.pilot/'query-events.jsonl';log.write_bytes(line[:80]);self.ledger.scan(self.pilot);self.assertEqual(len(self.ledger.pending({})[0]),2)
  with log.open('ab') as stream:stream.write(line[80:]+b'\n')
  self.ledger.scan(self.pilot);self.assertEqual(len(self.ledger.pending({})[0]),3)
  log.rename(self.pilot/'query-events-rotated.jsonl');log.write_bytes(json.dumps(event('event-new')).encode()+b'\n')
  self.ledger.scan(self.pilot);pending,_=self.ledger.pending({});self.assertEqual(len(pending),4)
  self.ledger.db.close();self.ledger=u.Ledger(self.root/'ledger');self.ledger.scan(self.pilot);self.assertEqual(len(self.ledger.pending({})[0]),4,'unacknowledged outbox survives restart')
  self.ledger.acknowledge(pending);self.ledger.scan(self.pilot);self.assertEqual(len(self.ledger.pending({})[0]),0)
 def test_primary_secondary_weekly_validation(self):
  weekly={'windowDurationMins':10080,'resetsAt':NOW+100,'usedPercent':20}
  for slot in ['primary','secondary']:
   self.assertEqual(u.weekly_window({'rateLimits':{'limitId':'codex',slot:weekly}},NOW)['resets_at'],u.iso(NOW+100))
  for bad in [{}, {'rateLimits':{'primary':{**weekly,'resetsAt':NOW-1}}}, {'rateLimits':{'primary':{**weekly,'resetsAt':NOW+700000}}}, {'rateLimits':{'primary':{**weekly,'windowDurationMins':300}}}]:
   with self.assertRaises(ValueError):u.weekly_window(bad,NOW)
 def test_failed_upload_retains_outbox_and_manual_reset_forces_fresh_api(self):
  self.state([turn('current',submittedAt=u.iso(NOW))]);config=self.root/'monitor.json';config.write_text(json.dumps({'targets':[{'chatgpt_project_url':URL,'project_id':'mud'}]}))
  args=SimpleNamespace(monitor_config=config,sessionpilot_home=self.pilot,config=self.root/'client.json',collector_id='test',codex='codex')
  def failed(*args):raise u.m.MonitorError('transport unavailable')
  with self.assertRaises(u.m.MonitorError):u.cycle(args,self.ledger,api=failed)
  self.assertEqual(len(self.ledger.pending({u.project_key(URL):'mud'})[0]),1)
  calls=[]
  def api(_config,route,body=None):
   calls.append((route,body))
   if route=='':return {'settings':{'limits_checked_at':u.iso(),'pending_reset':{'request_id':'click'}},'reset_date_stale':False}
   return {}
  read=[]
  def fresh(command):read.append(command);return {'resets_at':u.iso(),'window_minutes':10080,'used_percent':20}
  u.cycle(args,self.ledger,api=api,read_limits=fresh);self.assertEqual(read,['codex']);self.assertEqual(calls[-1][1]['reset_request_id'],'click');self.assertEqual(self.ledger.pending({u.project_key(URL):'mud'})[0],[])

if __name__=='__main__':unittest.main()
