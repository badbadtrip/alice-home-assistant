"""Supervise both child services; one failed service stops the app."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time
from urllib.request import Request,urlopen
from bootstrap import configure
from provision import provision,dashboard_path
from gateway import serve
from import_state import import_state


def self_info():
    req=Request('http://supervisor/addons/self/info',headers={'Authorization':'Bearer '+os.environ['SUPERVISOR_TOKEN']})
    with urlopen(req,timeout=15) as response:
        return json.load(response)['data']


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
    value,token=configure(data,root,json.loads((data/'options.json').read_text()))
    info=self_info()
    status={'configured':bool(value['advertised_url']),'dashboard_ready':False,'dashboard_path':dashboard_path(info['slug'])}
    server=serve(status)
    threading.Thread(target=server.serve_forever,daemon=True).start()
    children=[];stop=threading.Event()
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
             'ALICE_ASSIGNMENTS_PATH':str(data/'state/assignments.json'),'ALICE_EVENTS_PATH':str(data/'state/activity/events.json')}
        timezone=home_timezone()
        if timezone:env['HOME_TIMEZONE']=timezone
        # Manager creates durable state before Home Functions starts reading it.
        children.append(subprocess.Popen([sys.executable,str(root/'manager/service.py')],env={**env,'ALICE_MANAGER_CONFIG':str(data/'manager.json')}))
        children.append(subprocess.Popen(['node',str(root/'home-functions/node_modules/tsx/dist/cli.mjs'),str(root/'home-functions/src/main.ts')],cwd=root/'home-functions',env=env))
        print('Алиса запущена. Откройте веб-интерфейс и Wiki. Установка мостов требует настроенного advertised_url.',flush=True)
        while not stop.wait(1):
            if any(child.poll() is not None for child in children):raise RuntimeError('Один из сервисов остановлен')
        return 0
    finally:
        server.shutdown();server.server_close()
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
