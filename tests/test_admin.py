import json,sys,tempfile,unittest,threading
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import Mock
ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'alice/manager'))
from admin import access,station,validate_access,revision,remote_path
from service import Error
class AdminTests(unittest.TestCase):
 def test_access_persists_and_rejects_stale_write(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=Path(tmp);active=root/'functions.json';persistent=root/'config/panel-access.json'
   initial={'labels':['alice'],'voice_command_label':'voice','entities':{}}
   active.write_text(json.dumps(initial));m=SimpleNamespace(config={'access_config':str(active),'access_persistent':str(persistent)})
   before=access(m);changed={**initial,'labels':['alice','osveshchenie'],'entities':{'light.room':{'enabled':True,'area':'Комната'}}}
   after=access(m,{'config':changed,'revision':before['revision']})
   self.assertEqual(json.loads(persistent.read_text(encoding="utf-8")),changed)
   self.assertEqual(json.loads(Path(after['backup']).read_text(encoding="utf-8")),initial)
   with self.assertRaises(Error):access(m,{'config':initial,'revision':before['revision']})
   self.assertEqual(json.loads(active.read_text(encoding="utf-8")),changed)
 def test_invalid_configuration_rejected(self):
  for candidate in ({'labels':['bad; command']},{'labels':[],'entities':{'light.x':{'enabled':'false'}}},{'labels':[],'expose_all':'yes'},{'labels':[],'entities':{'light.x':{'modes':{'a':42}}}}):
   with self.subTest(candidate=candidate),self.assertRaises(ValueError):validate_access(candidate)
 def test_paths_and_remote_file_identity(self):
  for path in ('relative','/data/../etc/x','/data/x\ncommand'):
   with self.assertRaises(ValueError):remote_path(path)
  m=SimpleNamespace(store=SimpleNamespace(speaker=lambda _: {'trusted_fingerprint':'test'}),operation_lock=threading.Lock(),jobs_lock=threading.Lock(),jobs={},ssh=Mock(return_value=b''))
  raw='{}\n';result=station(m,'test','file-write',{'path':"/data/file'quote.json",'revision':'a'*64,'content':raw})
  self.assertTrue(result['saved']);self.assertFalse(result['restarted']);self.assertEqual(m.ssh.call_args.kwargs['data'],raw.encode())
  script=m.ssh.call_args.args[1];self.assertIn('sha256sum',script);self.assertIn('cp -p',script);self.assertNotIn('restart',script)
  with self.assertRaises(Error):station(m,'test','file-write',{'path':'/data/config.json','revision':'a'*64,'content':'{broken'})
 def test_console_explicit_and_job_collision(self):
  m=SimpleNamespace(store=SimpleNamespace(speaker=lambda _: {'trusted_fingerprint':'test'}),operation_lock=threading.Lock(),jobs_lock=threading.Lock(),jobs={},ssh=Mock(return_value=b'hello\nALICE_EXIT_CODE=3\n'))
  result=station(m,'test','console',{'command':"printf 'hello'; exit 3"});self.assertEqual(result['exit_code'],3);self.assertEqual(result['output'],'hello')
  m.jobs={'job':{'speaker_id':'test','status':'running'}}
  with self.assertRaises(Error):station(m,'test','console',{'command':'uname'})
 def test_volume_validates_level_and_calls_loopback_bridge(self):
  m=SimpleNamespace(store=SimpleNamespace(speaker=lambda _: {'trusted_fingerprint':'test'}),operation_lock=threading.Lock(),jobs_lock=threading.Lock(),jobs={},bridge_get=Mock(return_value=b'{}'))
  self.assertEqual(station(m,'test','volume',{'level':7}),{'level':7})
  self.assertEqual(m.bridge_get.call_args.args[1],'/volume?level=7')
  for level in (-1,11,True,'5;reboot',None,5.0):
   with self.subTest(level=level),self.assertRaises(Error):station(m,'test','volume',{'level':level})
 def test_airplay_switch_keeps_old_service_off_and_rejects_other_actions(self):
  m=SimpleNamespace(store=SimpleNamespace(speaker=lambda _: {'trusted_fingerprint':'test'},metadata=Mock()),operation_lock=threading.Lock(),jobs_lock=threading.Lock(),jobs={},ssh=Mock(return_value=b'{"enabled":true,"connected":false,"playing":false,"error":""}\n'))
  self.assertTrue(station(m,'test','airplay',{'action':'on'})['enabled'])
  script=m.ssh.call_args.args[1];self.assertLess(script.index('ctl.sh stop'),script.index('action=start'));self.assertIn('touch /data/airplay/disabled',script)
  station(m,'test','airplay',{'action':'off'});script=m.ssh.call_args.args[1];self.assertLess(script.index('action=stop'),script.index('ctl.sh start'));self.assertIn('[ -x /data/airplay/bin/shairport-sync ]',script)
  m.store.metadata.assert_called_with('test',airplay_off=True)
  with self.assertRaises(Error):station(m,'test','airplay',{'action':'reboot'})
 def test_indicator_validates_and_uses_atomic_persistent_write(self):
  raw=b'{"enabled":false,"color":"#00bfff"}\n'
  m=SimpleNamespace(store=SimpleNamespace(speaker=lambda _: {'trusted_fingerprint':'test'}),operation_lock=threading.Lock(),jobs_lock=threading.Lock(),jobs={},ssh=Mock(side_effect=[b'ready\n',raw,b'']))
  result=station(m,'test','indicator',{'action':'save','enabled':True,'color':'#12ABef','revision':revision(raw)})
  self.assertEqual(result['config'],{'enabled':True,'color':'#12abef','brightness':100,'mode':'steady','period_ms':2400})
  self.assertFalse(result['restarted'])
  self.assertIn('mv ',m.ssh.call_args.args[1]);self.assertIn('cp -p',m.ssh.call_args.args[1])
  for change in ({'color':'#fff;reboot'},{'enabled':'true'},{'revision':'stale'},{'brightness':101},{'brightness':True},{'period_ms':0},{'mode':'strobe'}):
   m.ssh=Mock(side_effect=[b'ready\n',raw])
   with self.assertRaises(Error):station(m,'test','indicator',{'action':'save','enabled':True,'color':'#00bfff','revision':revision(raw),**change})
 def test_rainbow_requires_updated_plum_bridge(self):
  raw=b'{"enabled":true,"color":"#00bfff"}\n'
  m=SimpleNamespace(store=SimpleNamespace(speaker=lambda _: {'trusted_fingerprint':'test'}),operation_lock=threading.Lock(),jobs_lock=threading.Lock(),jobs={},ssh=Mock(side_effect=[b'ready\n',raw,b'']))
  payload={'action':'save','enabled':True,'color':'#00bfff','mode':'rainbow','revision':revision(raw)}
  with self.assertRaises(Error):station(m,'test','indicator',payload)
  self.assertEqual(m.ssh.call_count,3)
  m.ssh=Mock(side_effect=[b'ready\n',raw,b'rainbow\n',b''])
  result=station(m,'test','indicator',payload)
  self.assertEqual(result['config']['mode'],'rainbow')
  self.assertFalse(result['restarted'])
if __name__=='__main__':unittest.main()

