"""Administrator-only access settings and explicit SSH maintenance operations."""
import base64
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shlex
import threading

LOCK = threading.RLock()


def validate_access(value):
    if not isinstance(value, dict) or set(value) - {'labels', 'expose_all', 'entities', 'voice_commands', 'voice_command_label'}:
        raise ValueError('Ожидаются labels, expose_all, entities и voice_commands.')
    if not isinstance(value.get('labels'), list) or any(not isinstance(x, str) or not re.fullmatch(r'[a-z0-9_]{1,100}', x) for x in value['labels']):
        raise ValueError('Выберите существующие ID меток Home Assistant.')
    if not isinstance(value.get('expose_all', False), bool):
        raise ValueError('expose_all должен быть true или false.')
    entities = value.get('entities', {})
    if not isinstance(entities, dict) or len(entities) > 5000:
        raise ValueError('entities должен быть объектом настроек устройств.')
    for key, item in entities.items():
        if not re.fullmatch(r'[a-z_]+\.[a-z0-9_]+', key) or not isinstance(item, dict):
            raise ValueError('Некорректная сущность: ' + str(key))
        if set(item) - {'area', 'description', 'lighting', 'enabled', 'modes', 'names', 'slug'}:
            raise ValueError('Неизвестное поле устройства: ' + key)
        for field in ('lighting', 'enabled'):
            if field in item and not isinstance(item[field], bool):
                raise ValueError(field + ': ожидается true или false')
        for field in ('area', 'description', 'slug'):
            if field in item and (not isinstance(item[field], str) or len(item[field]) > 500):
                raise ValueError(field + ': ожидается текст')
        if 'names' in item and (not isinstance(item['names'], list) or any(not isinstance(x, str) for x in item['names'])):
            raise ValueError('names: ожидается список названий')
        if 'modes' in item:
            modes = item['modes']
            if not isinstance(modes, dict) or any(not isinstance(v, dict) or any(not isinstance(x, str) and not (isinstance(x, list) and all(isinstance(y, str) for y in x)) for x in v.values()) for v in modes.values()):
                raise ValueError('modes: ожидается объект переименований режимов')
    commands = value.get('voice_commands', {})
    if not isinstance(commands, dict):
        raise ValueError('voice_commands: ожидается объект')
    for key, item in commands.items():
        if not re.fullmatch(r'[a-z][a-z0-9_]*', key) or not isinstance(item, dict) or set(item) - {'automation', 'description', 'phrases', 'verify'}:
            raise ValueError('Некорректная голосовая команда')
        if not re.fullmatch(r'(automation|script)\.[a-z0-9_]+', item.get('automation', '')) or not isinstance(item.get('description'), str) or not item['description'].strip():
            raise ValueError('Укажите действие и описание голосовой команды')
        if not isinstance(item.get('phrases'), list) or not item['phrases'] or any(not isinstance(x, str) or not x.strip() for x in item['phrases']):
            raise ValueError('Укажите фразы голосовой команды')
        if not isinstance(item.get('verify', []), list) or any(not isinstance(x, dict) or not isinstance(x.get('entity_id'), str) or not isinstance(x.get('state'), str) for x in item.get('verify', [])):
            raise ValueError('Некорректные проверки голосовой команды')
    if 'voice_command_label' in value and (not isinstance(value['voice_command_label'], str) or not re.fullmatch(r'[a-z][a-z0-9_]{0,63}', value['voice_command_label'])):
        raise ValueError('Некорректная метка голосовых команд')
    return value


def revision(raw):
    return hashlib.sha256(raw).hexdigest()


def private_write(path, raw):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + '.' + secrets.token_hex(8) + '.tmp')
    try:
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def access(manager, payload=None):
    from service import Error
    path = Path(manager.config.get('access_config', '/data/functions.json'))
    persistent = Path(manager.config.get('access_persistent', '/config/panel-access.json'))
    with LOCK:
        raw = path.read_bytes()
        current = json.loads(raw)
        if payload is None:
            return {'config': current, 'revision': revision(raw)}
        if payload.get('revision') != revision(raw):
            raise Error('Настройки уже изменились. Загрузите актуальные данные.', 409)
        try:
            candidate = validate_access(payload.get('config'))
        except (ValueError, TypeError) as error:
            raise Error(str(error)) from None
        # The command label belongs to the HA dashboard; changing it needs reprovisioning.
        if candidate.get('voice_command_label') != current.get('voice_command_label'):
            raise Error('Метка точных команд меняется в настройках приложения HA.')
        backup = path.parent / 'access-backups' / (revision(raw) + '.json')
        private_write(backup, raw)
        encoded = (json.dumps(candidate, ensure_ascii=False, indent=2) + '\n').encode()
        # Persist first: startup always reapplies this document. No service restart.
        private_write(persistent, encoded)
        private_write(path, encoded)
        return {'config': candidate, 'revision': revision(encoded), 'backup': str(backup), 'applied': True}


def remote_path(value):
    if not isinstance(value, str) or not value.startswith('/') or len(value) > 500 or any(ord(c) < 32 for c in value) or '..' in PurePosixPath(value).parts:
        raise ValueError('Укажите абсолютный путь к файлу без .. и управляющих символов.')
    return shlex.quote(value)


def station(manager, speaker_id, operation, payload):
    from service import Error
    speaker = manager.store.speaker(speaker_id)
    if not speaker.get('trusted_fingerprint'):
        raise Error('Сначала подтвердите SSH-ключ колонки.', 409)
    with manager.operation_lock:
        with manager.jobs_lock:
            if any(j['speaker_id'] == speaker_id and j['status'] == 'running' for j in manager.jobs.values()):
                raise Error('Дождитесь завершения операции на колонке.', 409)
        if operation == 'volume':
            level = payload.get('level')
            if type(level) is not int or not 0 <= level <= 10:
                raise Error('Громкость колонки: от 0 до 10.')
            # GET: BusyBox wget on Lite 2 has no --post-data.
            manager.bridge_get(speaker, f'/volume?level={level}')
            return {'level': level}
        if operation == 'airplay':
            action = payload.get('action')
            if action not in ('on', 'off'):
                raise Error('Выберите: включить или выключить AirPlay.')
            url = 'http://127.0.0.1:18861/airplay?action='
            # Both receivers need UDP 319/320. The disabled file also stops the old
            # service from returning at boot.
            if action == 'on':
                script = ('if [ -x /data/airplay/ctl.sh ]; then touch /data/airplay/disabled; /data/airplay/ctl.sh stop; sleep 1; fi; '
                          'wget -T 12 -q -O - "' + url + 'start"')
            else:
                script = ('wget -T 12 -q -O - "' + url + 'stop" && '
                          'if [ -x /data/airplay/bin/shairport-sync ]; then rm -f /data/airplay/disabled; /data/airplay/ctl.sh start; fi; true')
            output = manager.ssh(speaker, script, timeout=25)
            manager.store.metadata(speaker_id, airplay_off=action == 'off')
            state = json.loads(output.decode().strip().splitlines()[0])
            return {'enabled': state.get('enabled') is True, 'connected': state.get('connected') is True, 'playing': state.get('playing') is True, 'error': bool(state.get('error'))}
        if operation == 'player':
            action = payload.get('action')
            if action not in ('pause', 'resume', 'next', 'previous', 'silence'):
                raise Error('Неизвестная команда плеера.')
            manager.bridge_get(speaker, '/player?action=' + action, timeout=10)
            return {'ok': True}
        if operation == 'console':
            command = payload.get('command')
            if not isinstance(command, str) or not command.strip() or len(command) > 8000 or '\x00' in command:
                raise Error('Введите команду длиной до 8000 символов.')
            # The user explicitly runs commands as root; never execute on page load.
            # MIDI's BusyBox omits timeout. A separate process group and watchdog
            # bound the command including children without depending on that applet.
            script = ('/bin/busybox setsid sh -c ' + shlex.quote(command) + ' 2>&1 & pid=$!; '
                      '(sleep 10; /bin/busybox kill -TERM -- -"$pid" 2>/dev/null; '
                      'sleep 1; /bin/busybox kill -KILL -- -"$pid" 2>/dev/null) >/dev/null 2>&1 & watcher=$!; '
                      'wait "$pid"; code=$?; kill "$watcher" 2>/dev/null; '
                      'printf "\\nALICE_EXIT_CODE=%s\\n" "$code"')
            output = manager.ssh(speaker, 'sh -c ' + shlex.quote(script) + ' | head -c 48000', timeout=14).decode('utf-8', errors='replace')
            match = re.search(r'\nALICE_EXIT_CODE=(\d+)\n$', output)
            return {'output': output[:match.start()] if match else output, 'exit_code': int(match[1]) if match else None, 'truncated': match is None}
        if operation == 'indicator':
            root = '/data/opt/quasar-bridge/'
            status = manager.ssh(speaker, 'if test -f ' + root + 'indicator-ready && { test -d /sys/class/leds/cucumber:red:ring23 || test -d /sys/class/leds/plum:red:ring3; }; then echo ready; else echo unsupported; fi', timeout=12).decode().strip()
            if status != 'ready':
                raise Error('Подсветка доступна на Миди и Лайт 2 после обновления моста.', 409)
            path = root + 'indicator.json'
            current = manager.ssh(speaker, 'if test -L ' + path + '; then exit 1; elif test -f ' + path + '; then head -c 1024 ' + path + '; else printf \'{"enabled":false,"color":"#00bfff"}\\n\'; fi', timeout=12)
            config = json.loads(current)
            config = {'brightness':100,'mode':'steady','period_ms':2400,**config}
            if payload.get('action', 'get') == 'get':
                return {'config': config, 'revision': revision(current)}
            if payload.get('action') != 'save' or not isinstance(payload.get('enabled'), bool) or not isinstance(payload.get('color'), str) or not re.fullmatch(r'#[0-9a-fA-F]{6}', payload['color']):
                raise Error('Выберите цвет и включите или выключите индикацию.')
            if payload.get('revision') != revision(current):
                raise Error('Настройка уже изменена. Загрузите её заново.', 409)
            brightness=payload.get('brightness',100)
            mode=payload.get('mode','steady')
            period=payload.get('period_ms',2400)
            modes=('steady','breathe','double','rainbow','comet','wave','sparkle','fire')
            if type(brightness) is not int or not 1<=brightness<=100 or mode not in modes or type(period) is not int or not 800<=period<=8000:
                raise Error('Яркость: 1–100%, эффект из списка; период: 800–8000 мс.')
            layers={}
            for key in ('music','lamp'):
                layer=payload.get(key)
                if layer is None:
                    continue
                if not isinstance(layer,dict) or not isinstance(layer.get('enabled'),bool) or not isinstance(layer.get('color'),str) or not re.fullmatch(r'#[0-9a-fA-F]{6}',layer['color']) or layer.get('mode') not in modes or type(layer.get('brightness',100)) is not int or not 1<=layer.get('brightness',100)<=100 or type(layer.get('period_ms',2400)) is not int or not 800<=layer.get('period_ms',2400)<=8000:
                    raise Error('Проверьте настройки подсветки для музыки и лампы.')
                layers[key]={'enabled':layer['enabled'],'color':layer['color'].lower(),'mode':layer['mode'],'brightness':layer.get('brightness',100),'period_ms':layer.get('period_ms',2400)}
            if mode not in ('steady','breathe','double') or layers:
                capable = manager.ssh(speaker, 'if test "$(cat /data/opt/quasar-bridge/indicator-ready)" = 3; then echo rainbow; fi', timeout=12).decode().strip()
                if capable != 'rainbow':
                    raise Error('Эти эффекты доступны после обновления моста.', 409)
            config = {'enabled': payload['enabled'], 'color': payload['color'].lower(),'brightness':brightness,'mode':mode,'period_ms':period,**layers}
            raw = (json.dumps(config) + '\n').encode()
            suffix = '.alice-backup-' + secrets.token_hex(8)
            # Color is read on the next model request; no service restart needed.
            script = ('umask 077; test ! -L ' + path + ' && '
                      'if test -f ' + path + '; then test "$(sha256sum ' + path + ' | cut -d " " -f 1)" = ' + revision(current) + ' && cp -p ' + path + ' ' + path + suffix + '; else test ! -e ' + path + '; fi && '
                      'cat > ' + path + suffix + '.tmp && test "$(sha256sum ' + path + suffix + '.tmp | cut -d " " -f 1)" = ' + revision(raw) + ' && mv ' + path + suffix + '.tmp ' + path)
            manager.ssh(speaker, script, data=raw, timeout=12)
            return {'config': config, 'revision': revision(raw), 'restarted': False}
        try:
            path = remote_path(payload.get('path'))
        except ValueError as error:
            raise Error(str(error)) from None
        if operation == 'file-read':
            script = f'test -f {path} && test ! -L {path} && test "$(wc -c < {path})" -le 32000 && sha256sum {path} && base64 {path}'
            raw = manager.ssh(speaker, script, timeout=12).decode()
            digest, encoded = raw.split('\n', 1)
            try:
                content = base64.b64decode(encoded).decode('utf-8')
            except (ValueError, UnicodeError):
                raise Error('Редактор поддерживает текстовые UTF-8 файлы до 32 КБ.') from None
            return {'path': payload['path'], 'content': content, 'revision': digest.split()[0]}
        if operation == 'file-write':
            content = payload.get('content')
            digest = payload.get('revision', '')
            if not isinstance(content, str) or len(content.encode()) > 32000 or not isinstance(digest, str) or not re.fullmatch(r'[0-9a-f]{64}', digest):
                raise Error('Сначала откройте файл. Максимальный размер — 32 КБ.')
            if payload['path'].endswith('.json'):
                try:
                    json.loads(content)
                except ValueError:
                    raise Error('JSON содержит ошибку. Файл не сохранён.') from None
            suffix = '.alice-backup-' + secrets.token_hex(8)
            backup = shlex.quote(payload['path'] + suffix)
            temporary = shlex.quote(payload['path'] + suffix + '.tmp')
            expected = revision(content.encode())
            script = (f'test -f {path} && test ! -L {path} && test "$(sha256sum {path} | cut -d " " -f 1)" = {shlex.quote(digest)} '
                      f'&& cp -p {path} {backup} && cp -p {path} {temporary} && cat > {temporary} '
                      f'&& test "$(sha256sum {temporary} | cut -d " " -f 1)" = {expected} && mv {temporary} {path}')
            manager.ssh(speaker, script, data=content.encode(), timeout=12)
            return {'saved': True, 'path': payload['path'], 'revision': revision(content.encode()), 'backup': payload['path'] + suffix, 'restarted': False}
        raise Error('Неизвестная операция.', 404)
