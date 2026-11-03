"""Own HA dashboard/label registration via supported WebSocket APIs."""
import base64
import json
import os
from pathlib import Path
from websockets.sync.client import connect
from bootstrap import write_private


class HA:
    def __enter__(self):
        token=os.environ['SUPERVISOR_TOKEN']
        self.ws=connect('ws://supervisor/core/websocket',additional_headers={'Authorization':'Bearer '+token},open_timeout=10,close_timeout=2,max_size=4*1024*1024)
        self.seq=0
        if json.loads(self.ws.recv(timeout=10)).get('type')!='auth_required':
            raise RuntimeError('HA WS handshake')
        self.ws.send(json.dumps({'type':'auth','access_token':token}))
        if json.loads(self.ws.recv(timeout=10)).get('type')!='auth_ok':
            raise RuntimeError('HA WS auth')
        return self

    def __exit__(self,*_):
        self.ws.close()

    def call(self,kind,**kwargs):
        self.seq+=1
        self.ws.send(json.dumps({'id':self.seq,'type':kind,**kwargs}))
        for _ in range(100):
            result=json.loads(self.ws.recv(timeout=15))
            if result.get('id')==self.seq:
                if not result.get('success'):
                    raise RuntimeError(kind+': '+result.get('error',{}).get('code','failed'))
                return result.get('result')
        raise RuntimeError('HA WS response limit')


def dashboard_path(slug):
    # Local and repository installations may coexist during a clean-install test.
    return 'alice-app' if slug=='local_alice' else 'alice-'+slug.replace('_','-')


def provision(value,slug,data=Path('/data'),root=Path('/app')):
    path=dashboard_path(slug)
    with HA() as ha:
        labels=ha.call('config/label_registry/list')
        known={x['label_id'] for x in labels}
        for label in [value['voice_command_label'],*value['device_labels']]:
            if label not in known:
                created=ha.call('config/label_registry/create',name=label)
                if created['label_id']!=label:
                    raise RuntimeError('Label ID conflict; select existing label ID in options')
                known.add(label)
        dashboards=ha.call('lovelace/dashboards/list')
        existing=next((d for d in dashboards if d['url_path']==path),None)
        if existing:
            previous=ha.call('lovelace/config',url_path=path)
            if previous.get('alice_app_managed') is not True:
                raise RuntimeError('Dashboard alice-app exists and belongs to another owner')
            write_private(data/'dashboard.previous.json',json.dumps(previous))
        else:
            ha.call('lovelace/dashboards/create',url_path=path,title='Алиса',show_in_sidebar=False,require_admin=True,icon='mdi:account-voice')
        source=(root/'ui/alice-voice-card.js').read_text()
        source=source.replace('alice-voice-card','alice-app-card')
        url='data:text/javascript;base64,'+base64.b64encode(source.encode()).decode()
        resources=ha.call('lovelace/resources')
        record_path=data/'resource.json'
        record=json.loads(record_path.read_text()) if record_path.exists() else {}
        resource=next((r for r in resources if r['id']==record.get('id') or r['url']==url),None)
        if resource:
            if resource['url']!=url:
                ha.call('lovelace/resources/update',resource_id=resource['id'],url=url,res_type='module')
        else:
            resource=ha.call('lovelace/resources/create',url=url,res_type='module')
        write_private(record_path,json.dumps({'id':resource['id']}))
        config={'alice_app_managed':True,'views':[{'title':'Алиса','path':'speakers','type':'panel','cards':[{
            'type':'custom:alice-app-card','label':value['voice_command_label'],'manager_url':'/','manager_app':slug,'view':'home','app_embedded':True}]}]}
        ha.call('lovelace/config/save',url_path=path,config=config)
        verified=ha.call('lovelace/config',url_path=path)
        if verified!=config:
            raise RuntimeError('Dashboard verification failed')
