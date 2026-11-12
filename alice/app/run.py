"""Supervise both child services; one failed service stops the app."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from urllib.parse import urlsplit
from urllib.request import Request,urlopen
from bootstrap import configure,local_url
from provision import provision,dashboard_path
from gateway import serve
from import_state import import_state


def self_info():
    req=Request('http://supervisor/addons/self/info',headers={'Authorization':'Bearer '+os.environ['SUPERVISOR_TOKEN']})
    with urlopen(req,timeout=15) as response:
        return json.load(response)['data']


def detected_url(info):
    # A first start needs no typed address; a saved one always wins.
    req=Request('http://supervisor/network/info',headers={'Authorization':'Bearer '+os.environ['SUPERVISOR_TOKEN']})
    try:
        with urlopen(req,timeout=15) as response:
            network=json.load(response)['data']
        return local_url(network,(info.get('network') or {}).get('18080/tcp'))
    except (OSError,ValueError,KeyError):
        return ''


def music_assistant_urls(raw,value):
    # Music Assistant runs on the host network: the Supervisor gateway reaches it, and the
    # home's LAN address is the fallback. A typed address replaces both.
    typed=(raw.get('music_assistant_url') or '').strip()
    if typed:return typed
    urls=['ws://172.30.32.1:8927/sendspin']
    host=urlsplit(value.get('advertised_url') or '').hostname
    if host:urls.append(f'ws://{host}:8927/sendspin')
    return ','.join(urls)


def home_timezone():
    # History periods must follow the home's calendar, not a built-in default.
    req=Request('http://supervisor/core/api/config',headers={'Authorization':'Bearer '+os.environ['SUPERVISOR_TOKEN']})
    try:
        with urlopen(req,timeout=15) as response:
            value=json.load(response).get('time_zone')
    except (OSError,ValueError):
        return None
    return value if isinstance(value,str) and value else None


def refresh_store(stop,interval=900):
    # Supervisor polls repositories rarely; only re-read them, never install here.
    while not stop.wait(interval):
        req=Request('http://supervisor/store/reload',data=b'',method='POST',headers={'Authorization':'Bearer '+os.environ['SUPERVISOR_TOKEN']})
        try:
            with urlopen(req,timeout=120):pass
        except OSError as error:
            print('Проверка обновлений не выполнена: '+type(error).__name__,flush=True)


def main():
    os.umask(0o077)
    root=Path('/app');data=Path('/data')
    if import_state(data=data):print('Приватная миграция реестра и ключей выполнена.',flush=True)
    info=self_info()
    raw=json.loads((data/'options.json').read_text())
    if not raw.get('advertised_url'):raw['advertised_url']=detected_url(info)
    value,token=configure(data,root,raw)
    status={'configured':bool(value['advertised_url']),'dashboard_ready':False,'dashboard_path':dashboard_path(info['slug'])}
    server=serve(status)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    children=[];music=None;stop=threading.Event()
    for sig in (signal.SIGTERM,signal.SIGINT):signal.signal(sig,lambda *_:stop.set())
    threading.Thread(target=refresh_store,args=(stop,),daemon=True).start()
    try:
        for attempt in range(30):
            if stop.is_set():return 0
            try:
                provision(value,info['slug'],data,root)
                status['dashboard_ready']=True
                break
            except Exception as error:
                detail=str(error) if isinstance(error,RuntimeError) else type(error).__name__
                print('Панель HA недоступна: '+detail+'; повтор подготовки '+str(attempt+1)+'/30.',flush=True)
                if stop.wait(5):return 0
        if not status['dashboard_ready']:raise RuntimeError('Не удалось подготовить панель HA; проверьте доступ к Core API.')
        env={**os.environ,'CONFIG_PATH':str(data/'functions.json'),'PORT':'18080','HOME_FUNCTIONS_TOKEN':token,
             'ALICE_ASSIGNMENTS_PATH':str(data/'state/assignments.json'),'ALICE_EVENTS_PATH':str(data/'state/activity/events.json'),
             'ALICE_ROUTING_PATH':str(data/'artifacts/config.json'),
             'ALICE_MUSIC_PATH':str(data/'state/music.json'),
             'ALICE_SPEAKERS_PATH':str(data/'state/speakers.json'),
             'ALICE_SPOTIFY_PATH':str(data/'state/spotify.json')}
        timezone=home_timezone()
        if timezone:env['HOME_TIMEZONE']=timezone
        # Manager creates durable state before Home Functions starts reading it.
        children.append(subprocess.Popen([sys.executable,str(root/'manager/service.py')],env={**env,'ALICE_MANAGER_CONFIG':str(data/'manager.json')}))
        children.append(subprocess.Popen(['node',str(root/'home-functions/main.mjs')],cwd=root/'home-functions',env=env))
        # Music Assistant players are optional: their failure must never stop the app.
        music_env={**env,'ALICE_SENDSPIN_DATA':str(data/'state/sendspin'),'ALICE_SENDSPIN_URL':music_assistant_urls(raw,value)}
        music=subprocess.Popen([sys.executable,str(root/'music/sendspin_service.py')],env=music_env);music_restart=0.0
        print('Алиса запущена. Откройте веб-интерфейс и Wiki. Установка мостов требует настроенного advertised_url.',flush=True)
        while not stop.wait(1):
            if any(child.poll() is not None for child in children):raise RuntimeError('Один из сервисов остановлен')
            if music.poll() is not None:
                if not music_restart:
                    print('Плееры Music Assistant остановлены; перезапуск через 30 с.',flush=True);music_restart=time.monotonic()+30
                elif time.monotonic()>=music_restart:
                    music=subprocess.Popen([sys.executable,str(root/'music/sendspin_service.py')],env=music_env);music_restart=0.0
        return 0
    finally:
        server.shutdown();server.server_close()
        if music is not None:children.append(music)
        for child in children:
            if child.poll() is None:child.terminate()
        for child in children:
            try:child.wait(timeout=15)
            except subprocess.TimeoutExpired:child.kill();child.wait()


if __name__=='__main__':
    try:sys.exit(main())
    except Exception as error:
        detail=str(error) if isinstance(error,(ValueError,RuntimeError)) else type(error).__name__
        print('Приложение остановлено: '+detail+'. Проверьте настройки, соединение с HA и Wiki / Восстановление.',flush=True)
        sys.exit(1)
