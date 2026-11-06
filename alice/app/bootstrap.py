"""Persistent, instance-independent configuration. Never logs secret values."""
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import subprocess
from urllib.parse import urlsplit


def write_private(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temp = path.with_suffix(path.suffix + '.tmp')
    fd = os.open(temp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w', encoding='utf-8') as f:
        f.write(data)
        f.flush()
        os.fsync(f.fileno())
    os.chmod(temp, 0o600)
    os.replace(temp, path)


def local_url(network, port):
    """The address a speaker can reach, read from Supervisor network info, or ''."""
    interfaces=[item for item in network.get('interfaces',[]) if item.get('enabled',True) and item.get('connected',True)]
    for item in sorted(interfaces,key=lambda item:not item.get('primary')):
        for address in (item.get('ipv4') or {}).get('address') or []:
            host=ipaddress.ip_interface(address).ip
            if host.is_private and not host.is_loopback and port:
                return 'http://'+str(host)+':'+str(port)
    return ''


def options(raw):
    result = {**raw}
    label = result.get('voice_command_label', 'alice_voice_commands')
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,63}', label):
        raise ValueError('voice_command_label: используйте латиницу, цифры и подчёркивание')
    result['voice_command_label'] = label
    network = ipaddress.ip_network(result.get('speaker_network', '0.0.0.0/0'))
    if network.version != 4:
        raise ValueError('speaker_network: ожидается IPv4 CIDR')
    result['speaker_network'] = str(network)
    labels = result.get('device_labels', ['alice'])
    if not isinstance(labels, list) or any(not isinstance(x, str) or not x.strip() for x in labels):
        raise ValueError('device_labels: ожидается список меток HA')
    result['device_labels'] = labels
    url = result.get('advertised_url', '').rstrip('/')
    if url:
        parsed = urlsplit(url)
        try:
            host = ipaddress.IPv4Address(parsed.hostname)
            port = parsed.port
        except (ValueError, TypeError):
            raise ValueError('advertised_url: укажите локальный IPv4 и порт приложения') from None
        private = any(host in ipaddress.ip_network(n) for n in ('10.0.0.0/8','172.16.0.0/12','192.168.0.0/16'))
        if not private or parsed.scheme != 'http' or not port or parsed.path or parsed.query or parsed.fragment or parsed.username or parsed.password:
            raise ValueError('advertised_url: ожидается http://ЛОКАЛЬНЫЙ_IP:ПОРТ без пути')
    result['advertised_url'] = url
    llm = [result.get(x, '') for x in ('llm_base_url','llm_model','llm_api_key')]
    if any(llm) and not all(llm):
        raise ValueError('Для LLM заполните одновременно URL, модель и ключ')
    if llm[0]:
        parsed = urlsplit(llm[0])
        if parsed.scheme not in ('http','https') or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError('Некорректный URL LLM')
    if any(not isinstance(x,str) or any(ord(c)<32 for c in x) for x in llm):
        raise ValueError('Поля LLM не должны содержать управляющие символы')
    return result


def configure(data, root, raw, overrides=Path('/config/functions-overrides.json')):
    data, root = Path(data), Path(root)
    value = options(raw)
    data.mkdir(parents=True, exist_ok=True, mode=0o700)
    key = data/'ssh/id_ed25519'
    key.parent.mkdir(exist_ok=True, mode=0o700)
    if not key.exists():
        subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(key)],check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    os.chmod(key,0o600)
    secret_path = data/'functions.token'
    if not secret_path.exists():
        write_private(secret_path,secrets.token_urlsafe(48))
    token = secret_path.read_text().strip()
    if not re.fullmatch(r'[A-Za-z0-9_-]{40,100}',token):
        raise ValueError('Повреждён functions.token; восстановите резервную копию')
    write_private(data/'functions.env','HOME_FUNCTIONS_TOKEN='+shlex.quote(token)+'\n')
    functions={'labels':value['device_labels'],'voice_command_label':value['voice_command_label']}
    if overrides.exists():
        extra=json.loads(overrides.read_text())
        if not isinstance(extra,dict) or set(extra)-{'entities','voice_commands'} or any(not isinstance(v,dict) for v in extra.values()):
            raise ValueError('functions-overrides.json поддерживает объекты entities и voice_commands')
        functions.update(extra)
    panel_access = overrides.parent/'panel-access.json'
    if panel_access.exists():
        import sys
        sys.path.insert(0, str(root/'manager'))
        from admin import validate_access
        saved = validate_access(json.loads(panel_access.read_text()))
        functions.update({k: v for k, v in saved.items() if k != 'voice_command_label'})
    write_private(data/'functions.json', json.dumps(functions))
    artifacts=data/'artifacts'
    artifacts.mkdir(exist_ok=True,mode=0o700)
    for name in ('quasar-bridge','LICENSE','NOTICE','THIRD_PARTY_NOTICES'):
        target=artifacts/name
        if target.exists() or target.is_symlink():
            target.unlink()
        target.symlink_to(root/'artifacts'/name)
    config=json.loads((root/'bridge-config.json').read_text())
    if not value.get('llm_base_url'):
        config['routing']['default_route']='yandex'
    else:
        config['llm_base_url']=value['llm_base_url']
        config['llm_model']=value['llm_model']
        if value['advertised_url']:
            tools=config.setdefault('assistant_tools', {})
            tools['enabled']=True
            tools['mcp_servers']=[server for server in tools.get('mcp_servers', []) if server.get('name')!='home']+[
                {'name':'home','url':value['advertised_url']+'/mcp','bearer_token_env':'QUASAR_HOME_FUNCTIONS_TOKEN',
                 'headers_env':{'X-Alice-Speaker':'QUASAR_HOME_SPEAKER_ID'},
                 'allowed_tools':['home_catalog','home_state','home_history']}]
    write_private(artifacts/'config.json',json.dumps(config,ensure_ascii=False))
    runtime={'QUASAR_HOME_FUNCTIONS_URL':value['advertised_url'],'QUASAR_HOME_FUNCTIONS_TOKEN':token}
    if not value.get('llm_base_url'):
        runtime['QUASAR_LLM_DISABLED']='1'
    if value.get('llm_api_key'):
        runtime['QUASAR_LLM_API_KEY']=value['llm_api_key']
    write_private(artifacts/'runtime.env',''.join(k+'='+shlex.quote(v)+'\n' for k,v in runtime.items()))
    manager={'state_dir':str(data/'state'),'ssh_key':str(key),'artifact_dir':str(artifacts),'packaging_dir':str(root/'packaging'),
             'speaker_network':value['speaker_network'],'bind':'127.0.0.1','local_only':True,'port':18085,
             'ha_origin':'http://homeassistant:8123','ha_url':'http://homeassistant:8123',
             'home_functions_url':'http://127.0.0.1:18080','home_functions_local':True,'home_functions_env':str(data/'functions.env'),
             'installation_ready':bool(value['advertised_url']),'advertised_url':value['advertised_url'],'adoption_enabled':True}
    write_private(data/'manager.json',json.dumps(manager))
    return value,token
