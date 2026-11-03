import json
from pathlib import Path
import re
import shutil
import sys
import tempfile
import unittest
import threading
from http.server import ThreadingHTTPServer
from urllib.request import Request,urlopen
from urllib.error import HTTPError
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(ROOT/'alice/app'))
sys.path.insert(0,str(ROOT/'alice/manager'))
from bootstrap import configure,options
from gateway import markdown
from gateway import Handler
from import_state import import_state
import io
import tarfile
import types


class AppTests(unittest.TestCase):
    def test_private_import_and_traversal_rejection(self):
        with tempfile.TemporaryDirectory() as directory:
            base=Path(directory);source=base/'import.tar';data=base/'data';data.mkdir()
            entries={'state/speakers.json':b'[{"id":"midi"}]','state/assignments.json':b'{}','state/known_hosts':b'host key',
                     'ssh/id_ed25519':b'private-test-key','ssh/id_ed25519.pub':b'ssh-ed25519 PUBLIC test','functions.token':b'x'*64}
            def make(values):
                with tarfile.open(source,'w') as archive:
                    for name,raw in values.items():
                        info=tarfile.TarInfo(name);info.size=len(raw);archive.addfile(info,io.BytesIO(raw))
                source.chmod(0o600)
            make({'../escape':b'bad'})
            # Windows modes are not Unix file permissions; the archive validation
            # itself is exercised with a private stat result.
            original_stat=Path.stat
            def stat(path,*args,**kwargs):
                value=original_stat(path,*args,**kwargs)
                return types.SimpleNamespace(st_mode=0o100600) if path==source else value
            with patch.object(Path,'stat',stat),self.assertRaises(ValueError):import_state(source,data)
            self.assertFalse((base/'escape').exists())
            make(entries)
            with patch.object(Path,'stat',stat),patch('import_state.subprocess.run',return_value=types.SimpleNamespace(stdout=b'ssh-ed25519 PUBLIC\n')):
                self.assertTrue(import_state(source,data))
            self.assertFalse(source.exists())
            self.assertEqual((data/'functions.token').read_text(),'x'*64)
            self.assertTrue((data/'migration.complete.json').exists())

    def test_url_and_partial_llm_rejected(self):
        for url in ('http://127.0.0.1:18080','http://8.8.8.8:18080','http://192.168.10.10','http://user:pass@192.168.10.10:18080','http://192.168.10.10:18080/extra','https://192.168.10.10:18080','http://192.168.10.10:18080?token=bad'):
            with self.subTest(url=url),self.assertRaises(ValueError):options({'advertised_url':url})
        with self.assertRaises(ValueError):options({'llm_model':'test'})
        self.assertEqual(options({'advertised_url':'http://192.168.10.10:18080'})['advertised_url'],'http://192.168.10.10:18080')

    @patch('pathlib.Path.symlink_to',lambda self,target:shutil.copyfile(target,self))
    def test_private_secrets_persist_and_empty_setup_blocks_install(self):
        with tempfile.TemporaryDirectory() as directory:
            base=Path(directory);root=base/'app';data=base/'data'
            (root/'artifacts').mkdir(parents=True)
            (root/'bridge-config.json').write_text(json.dumps({'routing':{'default_route':'llm'}}))
            for name in ('quasar-bridge','LICENSE','NOTICE','THIRD_PARTY_NOTICES'):(root/'artifacts'/name).write_text('test')
            def keygen(*args,**kwargs):
                key=data/'ssh/id_ed25519';key.write_text('private-test-key');key.with_suffix('.pub').write_text('public-test-key')
            with patch('bootstrap.subprocess.run',side_effect=keygen) as run:
                _,first=configure(data,root,{})
                _,second=configure(data,root,{'advertised_url':'http://192.168.10.10:18080'})
                self.assertEqual(run.call_count,1)
            self.assertEqual(first,second)
            self.assertEqual(json.loads((data/'artifacts/config.json').read_text())['routing']['default_route'],'yandex')
            manager=json.loads((data/'manager.json').read_text())
            self.assertTrue(manager['local_only']);self.assertTrue(manager['installation_ready']);self.assertEqual(manager['advertised_url'],'http://192.168.10.10:18080')
            configure(data,root,{})
            self.assertFalse(json.loads((data/'manager.json').read_text())['installation_ready'])
            self.assertNotIn('SUPERVISOR_TOKEN',(data/'artifacts/runtime.env').read_text())
            self.assertIn("QUASAR_LLM_DISABLED=1",(data/'artifacts/runtime.env').read_text())

    def test_wiki_links_and_safe_renderer(self):
        for page in (ROOT/'wiki').glob('*.md'):
            for name in re.findall(r'\]\(([A-Za-z0-9_-]+)\.md\)',page.read_text(encoding='utf-8')):
                self.assertTrue((ROOT/'wiki'/(name+'.md')).exists(),str(page)+': '+name)
        rendered=markdown('# Wiki\n<script>alert(1)</script>\n[Мост](Bridge.md)\n```sh\nrm <unsafe>\n```')
        self.assertNotIn('<script>',rendered)
        self.assertIn('wiki/Bridge',rendered)
        self.assertIn('&lt;unsafe&gt;',rendered)

    def test_gateway_routes_do_not_allow_ha_proxy_or_shell(self):
        from proxy import allowed_path
        for path in ('/ha/api/services/light/turn_on','/api/shell','/api/speakers/a/../../shell','/api/speakers/a%2Frestart','/api/setup?token=test'):
            self.assertIsNone(allowed_path(path,'POST'))
        self.assertEqual(allowed_path('/api/speakers/a/restart','POST'),'/api/speakers/a/restart')
        self.assertEqual(allowed_path('/api/speakers/a/adopt','POST'),'/api/speakers/a/adopt')
        self.assertIsNone(allowed_path('/api/speakers/a/adopt','GET'))

    def test_ingress_wiki_and_anonymous_api(self):
        server=ThreadingHTTPServer(('127.0.0.1',0),Handler)
        server.ingress_address='127.0.0.1';server.root=ROOT/'alice';server.status={}
        # Wiki path is a bundled directory in a prepared release.
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        base='http://127.0.0.1:'+str(server.server_port)
        try:
            with urlopen(Request(base+'/',headers={'X-Ingress-Path':'/api/hassio_ingress/test'})) as response:
                body=response.read().decode()
                self.assertIn('/alice-app/speakers',body)
                self.assertIn('wiki/Installation',body)
                self.assertNotIn('SUPERVISOR_TOKEN',body)
            server.status={'dashboard_ready':True,'configured':False}
            with urlopen(Request(base+'/',headers={'X-Ingress-Path':'/api/hassio_ingress/test'})) as response:
                self.assertNotIn('Добро пожаловать!',response.read().decode())
            with self.assertRaises(HTTPError) as error:urlopen(base+'/api/setup')
            self.assertEqual(error.exception.code,401)
            error.exception.close()
            with self.assertRaises(HTTPError) as error:urlopen(base+'/wiki/../../options.json')
            self.assertEqual(error.exception.code,404)
            error.exception.close()
            server.ingress_address='192.0.2.1'
            with self.assertRaises(HTTPError) as error:urlopen(base+'/')
            self.assertEqual(error.exception.code,403)
            error.exception.close()
        finally:server.shutdown();server.server_close();thread.join()


if __name__=='__main__':unittest.main()
