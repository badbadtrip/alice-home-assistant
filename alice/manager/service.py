#!/usr/bin/env python3
"""Private LAN speaker manager with authenticated administrator maintenance tools."""
from __future__ import annotations

import base64
import hashlib
import http.client
import io
import ipaddress
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import subprocess
import sys
import tarfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote, urlsplit
from urllib.request import Request, urlopen

NETWORKS = tuple(ipaddress.ip_network(n) for n in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16'))
DEVICE = '/data/opt/quasar-bridge'
STARTUP_FILES = ('start.sh', 'supervise.sh', 'rollback-bind.sh', 'S98quasarbridge', 'recovery.sh', 'install-device.sh')
ENTITY = re.compile(r'^(automation|script)\.[a-z0-9_]+$')
MEDIA_ENTITY = re.compile(r'^media_player\.[a-z0-9_]+$')
ID = re.compile(r'^[a-z0-9][a-z0-9_-]{0,63}$')
def airplay_state(value):
    if not isinstance(value, dict):
        return None
    return {key: value.get(key) is True for key in ('enabled', 'connected', 'playing')} | {'error': bool(value.get('error'))}


AIRPLAY_ENTITY = re.compile(r'^media_player\.[a-z0-9_]*_airplay$')
# Exit 3: no receiver on this speaker. Exit 4: it is playing, never interrupt that.
AIRPLAY_REANNOUNCE = ('[ -x /data/airplay/ctl.sh ] || exit 3; '
                      "netstat -tn 2>/dev/null | grep -q ':5000 .*ESTABLISHED' && exit 4; "
                      '/data/airplay/ctl.sh restart')


class Error(Exception):
    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status


# Admin helpers import the same Error class when this file is the entry point.
if __name__ == '__main__':
    sys.modules.setdefault('service', sys.modules[__name__])

def lan_ip(value):
    try:
        address = ipaddress.IPv4Address(value)
    except (ValueError, TypeError):
        raise Error('Укажите частный IPv4-адрес колонки.') from None
    if not any(address in network for network in NETWORKS):
        raise Error('Разрешены только адреса частной локальной сети.')
    return str(address)


def text(value, field, maximum=100):
    if not isinstance(value, str) or not value.strip() or len(value) > maximum or any(ord(c) < 32 for c in value):
        raise Error('Некорректное поле: ' + field)
    return value.strip()


def atomic_json(path, value):
    path = Path(path)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    temporary = path.with_name(path.name + '.' + secrets.token_hex(8) + '.tmp')
    try:
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        if os.name != 'nt':
            fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
    finally:
        temporary.unlink(missing_ok=True)


def run(args, data=None, timeout=30):
    try:
        result = subprocess.run(args, input=data, capture_output=True, timeout=timeout)
    except (subprocess.TimeoutExpired, OSError):
        raise Error('Не удалось выполнить операцию подключения.', 502) from None
    if result.returncode:
        # SSH stderr, config and runtime may contain secrets: never expose them.
        raise Error('Операция SSH завершилась ошибкой. Проверьте ключ, отпечаток и доступность колонки.', 502)
    return result.stdout


class Store:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.lock = threading.RLock()
        for name, default in [('speakers.json', []), ('assignments.json', {})]:
            if not (self.directory / name).exists():
                atomic_json(self.directory / name, default)

    def read(self, name):
        return json.loads((self.directory / name).read_text(encoding='utf-8'))

    def speaker(self, speaker_id):
        for speaker in self.read('speakers.json'):
            if speaker['id'] == speaker_id:
                return speaker
        raise Error('Колонка не найдена.', 404)

    def save_speaker(self, values, speaker_id=None):
        allowed = {'name', 'area', 'host', 'port', 'profile', 'media_entity'}
        if set(values) - allowed:
            raise Error('Переданы неизвестные поля колонки.')
        with self.lock:
            speakers = self.read('speakers.json')
            old = self.speaker(speaker_id) if speaker_id else {}
            item = {**old, **values}
            item['id'] = speaker_id or secrets.token_hex(8)
            item['name'] = text(item.get('name'), 'name')
            item['area'] = text(item.get('area'), 'area')
            if 'media_entity' in item and item['media_entity'] is not None and (not isinstance(item['media_entity'], str) or not MEDIA_ENTITY.fullmatch(item['media_entity'])):
                raise Error('Выберите сущность media_player колонки или null.')
            item['host'] = lan_ip(item.get('host'))
            port = item.get('port', 2222)
            if type(port) is not int or not 1 <= port <= 65535:
                raise Error('Некорректный порт SSH.')
            item['port'] = port
            item['profile'] = item.get('profile', 'default')
            if item['profile'] not in ('default', 'midi'):
                raise Error('Выберите профиль default или midi.')
            if any(s['id'] != item['id'] and (s['host'], s['port']) == (item['host'], item['port']) for s in speakers):
                raise Error('Этот адрес колонки уже добавлен.', 409)
            if old and (old['host'], old['port']) != (item['host'], item['port']):
                item.pop('trusted_fingerprint', None)
                item.pop('last_check', None)
            if old:
                speakers = [item if s['id'] == item['id'] else s for s in speakers]
            else:
                speakers.append(item)
            atomic_json(self.directory / 'speakers.json', speakers)
            return item

    def metadata(self, speaker_id, **values):
        with self.lock:
            speakers = self.read('speakers.json')
            item = self.speaker(speaker_id)
            item.update(values)
            atomic_json(self.directory / 'speakers.json', [s if s['id'] != speaker_id else item for s in speakers])
            return item

    def delete(self, speaker_id):
        with self.lock:
            self.speaker(speaker_id)
            atomic_json(self.directory / 'speakers.json', [s for s in self.read('speakers.json') if s['id'] != speaker_id])
            assignments = self.read('assignments.json')
            for target in assignments:
                assignments[target] = [value for value in assignments[target] if value != speaker_id]
            atomic_json(self.directory / 'assignments.json', assignments)

    def assign(self, target, speakers):
        if not ENTITY.fullmatch(target):
            raise Error('Некорректное действие Home Assistant.')
        with self.lock:
            assignments = self.read('assignments.json')
            if speakers is None:
                assignments.pop(target, None)
            else:
                if not isinstance(speakers, list) or len(speakers) > 100 or any(not isinstance(s, str) for s in speakers):
                    raise Error('speakers должен быть списком идентификаторов или null.')
                known = {s['id'] for s in self.read('speakers.json')}
                if any(s not in known for s in speakers):
                    raise Error('Выбрана неизвестная колонка.')
                assignments[target] = sorted(set(speakers))
            atomic_json(self.directory / 'assignments.json', assignments)
            return {'target': target, 'speakers': assignments.get(target), 'assignments': assignments}


class Manager:
    def __init__(self, config):
        self.config = config
        self.store = Store(config['state_dir'])
        self.jobs = {}
        self.jobs_lock = threading.RLock()
        self.trust_pending = {}
        self.operation_lock = threading.Lock()
        self.connection_lock = threading.RLock()
        self.connections = {}
        self.pending_delivery = {}
        self.monitor_stop = threading.Event()
        self.monitor_thread = None
        self.airplay_next_check = 0.0
        self.airplay_next_try = 0.0
        self.airplay_lost_checks = 0
        self.airplay_tries = 0
        self._setup_cache = None
        self.known_hosts = self.store.directory / 'known_hosts'
        if not self.known_hosts.exists():
            self.known_hosts.touch(mode=0o600)
        self.allowed_network = ipaddress.ip_network(config.get('speaker_network', '0.0.0.0/0'))

    def start_monitor(self):
        if self.monitor_thread and self.monitor_thread.is_alive():
            return
        self.monitor_stop.clear()
        self.monitor_thread = threading.Thread(target=self._monitor_loop, name='alice-speaker-health', daemon=True)
        self.monitor_thread.start()

    def stop_monitor(self):
        self.monitor_stop.set()
        if self.monitor_thread and self.monitor_thread is not threading.current_thread():
            self.monitor_thread.join(timeout=9)

    def _monitor_loop(self):
        while not self.monitor_stop.is_set():
            began = time.monotonic()
            try:
                self.monitor_cycle()
            except Exception:
                # A registry read error must not kill the monitor or leak data.
                pass
            try:
                self.airplay_cycle()
            except Exception:
                pass
            try:
                import updater
                updater.cycle(self)
            except Exception:
                pass
            self.monitor_stop.wait(max(0.1, 15 - (time.monotonic() - began)))

    @staticmethod
    def _connection_default():
        return {'status': 'unknown', 'last_seen': None, 'checked_at': None, 'error': None,
                'speaker_id': None, 'voice_phrase_count': None, 'voice_phrase_revision': None, 'latency_ms': None, 'volume_push': False, 'airplay': None,
                'delivery': {'status': 'unknown', 'expected_revision': None, 'actual_revision': None, 'message': 'Ожидается проверка колонки.'}}

    @staticmethod
    def _connection_identity(speaker):
        return (speaker['host'], speaker['port'], speaker.get('trusted_fingerprint'))

    def speakers(self):
        """Read cached observations only; never connect to a speaker in HTTP GET."""
        import updater
        auto_update = updater.enabled(self)
        records = self.store.read('speakers.json')
        now = time.time()
        with self.connection_lock:
            result = []
            for speaker in records:
                cached = self.connections.get(speaker['id'], {})
                connection = dict(cached.get('value', self._connection_default()))
                if cached.get('identity') != self._connection_identity(speaker):
                    connection = self._connection_default()
                elif connection['status'] == 'online' and now - (connection['checked_at'] or 0) > 45:
                    connection['status'] = 'unknown'
                    connection['error'] = 'Статус устарел. Ожидается следующая проверка.'
                    connection['delivery'] = {**connection.get('delivery', {}), 'status': 'unknown', 'message': 'Статус колонки устарел.'}
                result.append({**speaker, 'connection': connection, 'auto_update': auto_update})
            return result

    def _airplay_lost(self):
        token = os.environ.get('SUPERVISOR_TOKEN')
        if not token:
            return False
        request = Request('http://supervisor/core/api/states', headers={'Authorization': 'Bearer ' + token})
        with urlopen(request, timeout=10) as response:
            states = json.load(response)
        return any(AIRPLAY_ENTITY.fullmatch(item['entity_id']) and item['state'] == 'unavailable' for item in states)

    # A speaker announces AirPlay only when the service starts and does not answer
    # later queries, so a restarted Music Assistant loses it until it announces again.
    def airplay_cycle(self):
        now = time.monotonic()
        if now < self.airplay_next_check:
            return
        self.airplay_next_check = now + 60
        if not self._airplay_lost():
            self.airplay_lost_checks = self.airplay_tries = 0
            return
        self.airplay_lost_checks += 1
        # Two checks give Music Assistant time to finish starting; three tries
        # keep a speaker without a working receiver from restarting forever.
        if self.airplay_lost_checks < 2 or self.airplay_tries >= 3 or now < self.airplay_next_try:
            return
        self.airplay_tries += 1
        self.airplay_next_try = now + 300
        for speaker in self.store.read('speakers.json'):
            if not speaker.get('trusted_fingerprint') or not self.operation_lock.acquire(blocking=False):
                continue
            try:
                self.ssh(speaker, AIRPLAY_REANNOUNCE, timeout=25)
            except Exception:
                pass  # An offline speaker or one without a receiver is not an error here.
            finally:
                self.operation_lock.release()

    def monitor_cycle(self):
        records = self.store.read('speakers.json')
        known = {speaker['id'] for speaker in records}
        with self.connection_lock:
            self.connections = {key: value for key, value in self.connections.items() if key in known}
            self.pending_delivery = {key: value for key, value in self.pending_delivery.items() if key in known}
        for speaker in records:
            if self.monitor_stop.is_set():
                break
            if not speaker.get('trusted_fingerprint'):
                continue
            # Never overlap a health poll with install, manual check or reboot.
            # Busy devices naturally become unknown after the stale threshold.
            if not self.operation_lock.acquire(blocking=False):
                continue
            try:
                # Configuration changes and jobs are coordinated by jobs_lock;
                # only publish if the address/key identity is still unchanged.
                self._monitor_speaker(speaker)
            finally:
                self.operation_lock.release()

    def _monitor_speaker(self, speaker):
        identity = self._connection_identity(speaker)
        with self.connection_lock:
            previous = self.connections.get(speaker['id'], {})
            value = dict(previous.get('value', self._connection_default())) if previous.get('identity') == identity else self._connection_default()
        began = time.monotonic()
        try:
            health = json.loads(self.ssh(speaker, 'wget -T 3 -q -O - http://127.0.0.1:18861/healthz', timeout=8))
            if not isinstance(health, dict) or health.get('status') != 'ok':
                raise Error('Мост сообщил о неисправности.', 502)
            if health.get('speaker_id') != speaker['id']:
                raise Error('Идентификатор моста не совпадает с зарегистрированной колонкой.', 409)
            count = health.get('voice_phrase_count')
            revision = health.get('voice_phrase_revision')
            value.update(status='online', last_seen=int(time.time()), error=None,
                         speaker_id=speaker['id'], volume_push=health.get('volume_push') is True, airplay=airplay_state(health.get('airplay')),
                         voice_phrase_count=count if type(count) is int and 0 <= count <= 100000 else None,
                         voice_phrase_revision=revision if isinstance(revision, str) and re.fullmatch(r'[a-zA-Z0-9_-]{1,128}', revision) else None)
        except Error as error:
            message = str(error) if error.status == 409 else 'Нет связи с мостом. Проверьте питание и подключение колонки.'
            value.update(status='offline', error=message)
        except Exception:
            value.update(status='offline', error='Мост не вернул корректный статус.')
        value['delivery'] = self._delivery(speaker, value)
        value.update(checked_at=int(time.time()), latency_ms=round((time.monotonic() - began) * 1000, 1))
        # A successful answer from a previous address must not mark an edited
        # or removed registration online.
        try:
            current = self.store.speaker(speaker['id'])
        except Error:
            return
        if self._connection_identity(current) != identity:
            return
        with self.connection_lock:
            self.connections[speaker['id']] = {'identity': identity, 'value': value}

    def _hf_request(self, path, speaker=None, payload=None):
        if path not in ('/voice-phrases', '/preview', '/events', '/inventory', '/dictionary', '/dictionary/alias', '/music', '/spotify', '/spotify/login', '/spotify/callback', '/spotify/sync'):
            raise Error('Неизвестный внутренний маршрут.', 500)
        parsed = urlsplit(self.config.get('home_functions_url', ''))
        if parsed.scheme not in ('http', 'https') or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ('', '/'):
            raise Error('Некорректный серверный адрес Home Functions.', 503)
        if not (self.config.get('home_functions_local') is True and parsed.hostname == '127.0.0.1'):
            lan_ip(parsed.hostname)
        env_path = Path(self.config.get('home_functions_env', '/nonexistent/alice-functions.env'))
        if env_path.is_symlink() or not env_path.is_file() or env_path.stat().st_mode & 0o077:
            raise Error('Серверные реквизиты Home Functions недоступны.', 503)
        token = None
        for line in env_path.read_text().splitlines():
            key, sep, value = line.partition('=')
            if sep and key.strip() == 'HOME_FUNCTIONS_TOKEN':
                try:
                    values = shlex.split(value, comments=True)
                    token = values[0] if len(values) == 1 else None
                except ValueError:
                    token = None
        if not token or any(ord(c) < 33 or ord(c) > 126 for c in token):
            raise Error('Серверные реквизиты Home Functions недоступны.', 503)
        headers = {'Authorization': 'Bearer ' + token, 'Accept': 'application/json'}
        if speaker:
            headers['X-Alice-Speaker'] = speaker['id']
        body = json.dumps(payload, ensure_ascii=False).encode() if payload is not None else None
        if body is not None:
            headers['Content-Type'] = 'application/json'
        cls = http.client.HTTPSConnection if parsed.scheme == 'https' else http.client.HTTPConnection
        connection = cls(parsed.hostname, parsed.port or (443 if parsed.scheme == 'https' else 80), timeout=5)
        try:
            connection.request('POST' if payload is not None else 'GET', path, body=body, headers=headers)
            response = connection.getresponse()
            raw = response.read(2 * 1024 * 1024 + 1)
            if response.status != 200 or len(raw) > 2 * 1024 * 1024:
                raise Error('Home Functions не подтвердил запрос.', 502)
            return json.loads(raw)
        except Error:
            raise
        except Exception:
            raise Error('Нет связи с Home Functions.', 502) from None
        finally:
            connection.close()

    def _delivery(self, speaker, connection):
        actual = connection.get('voice_phrase_revision') if connection.get('status') == 'online' else None
        value = {'status': 'unknown', 'expected_revision': None, 'actual_revision': actual, 'message': 'Нет свежего статуса колонки.'}
        try:
            expected = self._hf_request('/voice-phrases', speaker).get('revision')
            if not isinstance(expected, str) or not re.fullmatch(r'[a-zA-Z0-9_-]{1,128}', expected):
                raise Error('Нет корректной ревизии Home Functions.', 502)
            value['expected_revision'] = expected
            if actual is not None:
                value.update(status='synced' if actual == expected else 'pending',
                             message='Команды доставлены.' if actual == expected else 'Колонка ещё не получила текущую версию команд.')
                with self.connection_lock:
                    if actual == expected:
                        self.pending_delivery.pop(speaker['id'], None)
                    else:
                        identity = (self._connection_identity(speaker), expected, actual)
                        previous = self.pending_delivery.get(speaker['id'])
                        if not previous or previous['identity'] != identity:
                            previous = {'identity': identity, 'began': time.monotonic()}
                            self.pending_delivery[speaker['id']] = previous
                        if time.monotonic() - previous['began'] >= 120:
                            value.update(status='error', message='Команды не доставлены более двух минут. Проверьте связь колонки с Home Functions или перезапустите мост.')
            else:
                with self.connection_lock:
                    self.pending_delivery.pop(speaker['id'], None)
        except Exception:
            with self.connection_lock:
                self.pending_delivery.pop(speaker['id'], None)
            value.update(status='error', message='Не удалось проверить текущую версию команд Home Functions.')
        return value

    def phrase_check(self, payload):
        if set(payload) != {'text', 'speakers'}:
            raise Error('Ожидаются text и speakers.')
        phrase = text(payload['text'], 'text', 1000)
        ids = payload['speakers']
        if not isinstance(ids, list) or not 1 <= len(ids) <= 100 or any(not isinstance(value, str) for value in ids):
            raise Error('Выберите хотя бы одну колонку.')
        results = []
        for speaker_id in dict.fromkeys(ids):
            speaker = self.store.speaker(speaker_id)
            try:
                result = self._hf_request('/preview', speaker, {'text': phrase, 'area': speaker['area']})
                if not isinstance(result, dict):
                    raise Error('Некорректный результат проверки.', 502)
                results.append({**result, 'speaker_id': speaker_id})
            except Error as error:
                results.append({'speaker_id': speaker_id, 'error': str(error)})
        return {'results': results}

    def setup(self):
        if self._setup_cache is not None:
            return self._setup_cache
        public_path = Path(self.config.get('ssh_public_key', self.config['ssh_key'] + '.pub'))
        if public_path.is_file() and not public_path.is_symlink():
            public_key = public_path.read_text().strip()
        else:
            # Derive only the PUBLIC half; private material never leaves ssh-keygen.
            public_key = run(['ssh-keygen', '-y', '-f', self.config['ssh_key']], timeout=5).decode().strip()
        parts = public_key.split()
        if len(parts) < 2 or parts[0] != 'ssh-ed25519':
            raise Error('Для менеджера нужен серверный SSH-ключ ed25519.', 503)
        try:
            base64.b64decode(parts[1], validate=True)
        except ValueError:
            raise Error('Некорректный публичный SSH-ключ менеджера.', 503) from None
        result = {'public_key': public_key, 'adoption_enabled':self.config.get('adoption_enabled') is True, 'installation_ready': self.config.get('installation_ready') is not False, 'advertised_url': self.config.get('advertised_url', ''), 'profiles': {
            'midi': {'name': 'Станция MIDI', 'authorized_keys_path': '/root/.ssh/authorized_keys',
                     'instructions': [
                         'Откройте уже доверенную root SSH-сессию к MIDI и проверьте способ авторизации своей прошивки.',
                         'У штатного для этого профиля Dropbear ключи root находятся в /root/.ssh/authorized_keys. Сохраните существующие строки и добавьте публичный ключ менеджера отдельной строкой.',
                         'Сначала проверьте фактический параметр -D своего Dropbear: при нём authorized_keys находится внутри указанного каталога. На подключённой MIDI менеджер использует постоянный каталог /data/alice-ssh/authorized_keys и запуск -D /data/alice-ssh. Не создавайте второе хранилище вслепую.',
                         'Все родительские каталоги хранилища ключей должны принадлежать доверенному пользователю и не быть доступны для записи группе/остальным. На MIDI /data/opt может быть group-writable, из-за чего Dropbear отвергает ключи внутри него; не переносите туда хранилище без проверки разрешений.',
                         'Корневая файловая система MIDI может быть только для чтения. Используйте проверенный способ изменения ключей именно своей root-прошивки и верните исходный режим файловой системы. Универсальная команда remount здесь намеренно не предлагается.',
                         'Каталог .ssh должен принадлежать root и иметь права 0700, authorized_keys — 0600. Не закрывайте исходную SSH-сессию до проверки отдельного входа менеджера.',
                         'Менеджер не получает root, не прошивает колонку и не заменяет существующие SSH-ключи.']},
            'default': {'name': 'Станция Lite 2', 'authorized_keys_path': '/data/opt/etc/dropbear/authorized_keys',
                        'instructions': [
                            'Откройте уже доверенную root SSH-сессию к Lite 2. Этот профиль ожидает Dropbear с параметром -D /data/opt/etc/dropbear.',
                            'При таком запуске ключи находятся в /data/opt/etc/dropbear/authorized_keys, а не в /root/.ssh/authorized_keys. Проверьте параметры своего Dropbear: при другой сборке путь может отличаться.',
                            'Сохраните существующие строки и добавьте публичный ключ менеджера отдельной строкой в фактически используемый authorized_keys. Файл и каталог должны принадлежать root; файл — права 0600, каталог — 0700.',
                            'Не закрывайте исходную SSH-сессию до проверки отдельного входа менеджера. Для записи ключа в раздел данных перезапуск Dropbear и изменение rootfs обычно не нужны.',
                            'Если параметры SSH-сервера отличаются, сначала настройте доступ локально по инструкции своей root-прошивки.']}
        }}
        self._setup_cache = result
        return result

    def validate_speaker(self, speaker):
        if ipaddress.ip_address(lan_ip(speaker['host'])) not in self.allowed_network:
            raise Error('Адрес вне разрешённой сети колонок.')

    def ssh(self, speaker, command, data=None, timeout=30):
        self.validate_speaker(speaker)
        return run(['ssh', '-F', '/dev/null', '-i', self.config['ssh_key'], '-o', 'IdentitiesOnly=yes',
                    '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'ConnectTimeout=8',
                    '-o', 'UserKnownHostsFile=' + str(self.known_hosts), '-p', str(speaker['port']),
                    'root@' + speaker['host'], command], data, timeout)

    def fingerprints(self, speaker_id):
        speaker = self.store.speaker(speaker_id)
        self.validate_speaker(speaker)
        raw = run(['ssh-keyscan', '-T', '5', '-p', str(speaker['port']), '-t', 'ed25519', speaker['host']], timeout=8)
        lines = []
        for row in raw.decode().splitlines():
            parts = row.split()
            if len(parts) != 3 or parts[1] != 'ssh-ed25519':
                continue
            expected = speaker['host'] if speaker['port'] == 22 else '[%s]:%s' % (speaker['host'], speaker['port'])
            if parts[0] != expected:
                continue
            try:
                digest = hashlib.sha256(base64.b64decode(parts[2], validate=True)).digest()
            except ValueError:
                continue
            lines.append((row, 'SHA256:' + base64.b64encode(digest).decode().rstrip('=')))
        if len(lines) != 1:
            raise Error('Не удалось получить однозначный ключ ed25519 колонки.', 502)
        nonce = secrets.token_hex(24)
        self.trust_pending[speaker_id] = {'nonce': nonce, 'line': lines[0][0], 'fingerprint': lines[0][1],
                                         'host': speaker['host'], 'port': speaker['port'], 'expires': time.monotonic() + 300}
        return {'fingerprint': lines[0][1], 'nonce': nonce, 'host': speaker['host'], 'port': speaker['port'],
                'instruction': 'Сравните отпечаток с доверенным SSH-подключением перед подтверждением.'}

    def trust(self, speaker_id, values):
        with self.store.lock:
            pending = self.trust_pending.get(speaker_id)
            speaker = self.store.speaker(speaker_id)
            if not pending or pending['expires'] < time.monotonic() or values.get('nonce') != pending['nonce'] or values.get('fingerprint') != pending['fingerprint']:
                raise Error('Отпечаток устарел. Проверьте подключение заново.', 409)
            if (pending['host'], pending['port']) != (speaker['host'], speaker['port']):
                raise Error('Адрес колонки изменился.', 409)
            # Never silently replace a trusted key. A changed key needs local operator review.
            old = speaker.get('trusted_fingerprint')
            if old and old != pending['fingerprint']:
                raise Error('Ключ изменился. Требуется локальная проверка known_hosts.', 409)
            existing = self.known_hosts.read_text()
            if pending['line'] not in existing.splitlines():
                fd = os.open(self.known_hosts, os.O_WRONLY | os.O_APPEND)
                with os.fdopen(fd, 'w') as stream:
                    stream.write(pending['line'] + '\n')
                    stream.flush()
                    os.fsync(stream.fileno())
            self.trust_pending.pop(speaker_id, None)
            return self.store.metadata(speaker_id, trusted_fingerprint=pending['fingerprint'])

    def job(self, speaker_id, operation, params=None):
        if operation not in ('check', 'install', 'adopt', 'reboot', 'confirm', 'rollback', 'restart', 'configure'):
            raise Error('Неизвестная операция.')
        speaker = self.store.speaker(speaker_id)
        if not speaker.get('trusted_fingerprint'):
            raise Error('Сначала проверьте и подтвердите SSH-ключ колонки.', 409)
        if speaker.get('install', {}).get('state') in ('awaiting_acceptance', 'adopting') and operation not in ('check', 'confirm', 'rollback'):
            raise Error('Сначала подтвердите голосовую проверку или верните прежний мост.', 409)
        params = dict(params or {})
        if operation == 'configure':
            if set(params) - {'area', 'name', 'media_entity'} or 'area' not in params:
                raise Error('Для настройки нужны area и необязательные name, media_entity.')
            params['area'] = text(params['area'], 'area')
            if 'name' in params:
                params['name'] = text(params['name'], 'name')
            if 'media_entity' in params and params['media_entity'] is not None and (not isinstance(params['media_entity'], str) or not MEDIA_ENTITY.fullmatch(params['media_entity'])):
                raise Error('Выберите media_player колонки или null.')
        elif params:
            raise Error('Эта операция не принимает дополнительные параметры.')
        with self.jobs_lock:
            if any(j['speaker_id'] == speaker_id and j['status'] == 'running' for j in self.jobs.values()):
                raise Error('На этой колонке уже выполняется операция.', 409)
            job_id = secrets.token_hex(8)
            value = {'id': job_id, 'speaker_id': speaker_id, 'operation': operation, 'status': 'running',
                     'stage': 'Подключение', 'started_at': int(time.time())}
            self.jobs[job_id] = value
            threading.Thread(target=self._job, args=(value, speaker, params), daemon=True).start()
            return {'job_id': job_id}

    def _job(self, job, speaker, params=None):
        try:
            # Serializes provisioning and protects server-side backup/output paths.
            with self.operation_lock:
                if job['operation'] == 'configure':
                    result = self._configure(speaker, job, params or {})
                else:
                    result = getattr(self, '_' + job['operation'])(speaker, job)
            job.update(status='succeeded', stage='Готово', result=result)
        except Error as error:
            if job['operation'] == 'check':
                self.store.metadata(speaker['id'], last_check={'root':False,'checked_at':int(time.time()),'error':str(error)})
            job.update(status='failed', stage='Операция остановлена', error=str(error))
        except Exception:
            if job['operation'] == 'check':
                self.store.metadata(speaker['id'], last_check={'root':False,'checked_at':int(time.time())})
            job.update(status='failed', stage='Операция остановлена', error='Внутренняя ошибка. Подробности и секреты не передаются в браузер.')
        finally:
            job['finished_at'] = int(time.time())
            with self.jobs_lock:
                # Bounded in-memory history; durable device state is separate.
                finished = [key for key, value in self.jobs.items() if value['status'] != 'running']
                for key in finished[:-100]:
                    self.jobs.pop(key, None)

    def _check(self, speaker, job):
        result = self.ssh(speaker, 'id -u; uname -m; if test -e ' + DEVICE + '; then echo bridge_present; else echo bridge_absent; fi').decode().splitlines()
        if result[:2] != ['0', 'aarch64']:
            raise Error('Поддерживаются только колонки с root SSH и архитектурой aarch64.', 409)
        present = 'bridge_present' in result
        health = None
        if present:
            try:
                health = json.loads(self.ssh(speaker, 'wget -T 3 -q -O - http://127.0.0.1:18861/healthz'))
            except (Error, ValueError):
                pass
        result = {'root': True, 'architecture': 'aarch64', 'bridge_present': present,
                  'bridge_healthy': bool(health and health.get('status') == 'ok'),
                  'voice_phrase_count': health.get('voice_phrase_count') if health else None,
                  'checked_at': int(time.time())}
        self.store.metadata(speaker['id'], last_check=result)
        return result

    def _package(self, speaker, original):
        artifact = Path(self.config['artifact_dir'])
        packaging = Path(self.config['packaging_dir'])
        binary = (artifact / 'quasar-bridge').read_bytes()
        if (len(binary) < 1_000_000 or binary[:7] != b'\x7fELF\x02\x01\x01'
                or binary[16:18] not in (b'\x02\x00', b'\x03\x00')
                or binary[18:20] != b'\xb7\x00' or binary[20:24] != b'\x01\x00\x00\x00'):
            raise Error('Серверный артефакт не является проверенным ELF aarch64.', 503)
        try:
            firmware = json.loads(original)
            if not isinstance(firmware['aliced']['uniProxyUrl'], str) or not isinstance(firmware['aliced']['uniProxyQuicUrl'], str):
                raise ValueError()
            firmware['aliced']['uniProxyUrl'] = 'ws://127.0.0.1:18861/uni.ws'
            firmware['aliced']['uniProxyQuicUrl'] = ''
        except (ValueError, KeyError, TypeError):
            raise Error('Неподдерживаемая конфигурация прошивки; колонка не изменена.', 409) from None
        config = json.loads((artifact / 'config.json').read_text())
        runtime = (artifact / 'runtime.env').read_text()
        # Strip any template identity and area. Assign each speaker its durable ID.
        runtime = '\n'.join(line for line in runtime.splitlines() if not line.startswith(('QUASAR_HOME_AREA=', 'QUASAR_HOME_SPEAKER_ID=')))
        runtime += '\nQUASAR_HOME_AREA=' + shlex.quote(speaker['area']) + '\nQUASAR_HOME_SPEAKER_ID=' + shlex.quote(speaker['id']) + '\n'
        files = {'bin/quasar-bridge': binary, 'config.json': (json.dumps(config, ensure_ascii=False) + '\n').encode(),
                 'runtime.env': runtime.encode(), 'original.cfg': original,
                 'candidate.cfg': (json.dumps(firmware, ensure_ascii=False, indent=2) + '\n').encode()}
        for name in ('start.sh', 'supervise.sh', 'rollback-bind.sh', 'S98quasarbridge', 'recovery.sh', 'install-device.sh'):
            raw = (packaging / name).read_bytes()
            if speaker['profile'] == 'midi':
                raw = raw.replace(b'/etc/init.d/S99quasar', b'/etc/init.d/S97quasar').replace(b'/etc/init.d/S98quasarbridge', b'/etc/init.d/S96quasarbridge')
            files[name] = raw
        files['config.device.example.json'] = files['config.json']
        for name in ('LICENSE', 'NOTICE', 'THIRD_PARTY_NOTICES'):
            files[name] = (artifact / name).read_bytes()
        manifest = {name: hashlib.sha256(data).hexdigest() for name, data in files.items() if name not in ('runtime.env', 'original.cfg', 'candidate.cfg')}
        files['manifest.json'] = (json.dumps(manifest) + '\n').encode()
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode='w') as tar:
            for name, data in files.items():
                info = tarfile.TarInfo(name)
                info.size = len(data)
                info.mode = 0o700 if name.endswith('.sh') or name in ('bin/quasar-bridge', 'S98quasarbridge') else 0o600
                tar.addfile(info, io.BytesIO(data))
        return archive.getvalue(), manifest, files['install-device.sh']

    def _space(self, speaker, required):
        # Check before creating a backup or staging a binary. UBIFS can be much
        # smaller than the uncompressed artifacts; do not rely on compression.
        output = self.ssh(speaker, 'df -Pk /data').decode().splitlines()
        try:
            available = int(output[-1].split()[-3]) * 1024
            if available < 0:
                raise ValueError()
        except (ValueError, IndexError):
            raise Error('Не удалось определить свободное место на колонке. Установка не начата.', 409) from None
        if available < required:
            need_mb = (required + 1024 * 1024 - 1) // (1024 * 1024)
            free_mb = available // (1024 * 1024)
            raise Error('Недостаточно места на колонке: свободно ' + str(free_mb) + ' МБ, требуется не менее ' + str(need_mb)
                        + ' МБ для загрузки и резервной копии. Работающий мост не изменён; проверьте старые резервные копии локально.', 409)

    def _install(self, speaker, job):
        if self.config.get('installation_ready') is False:
            raise Error('Укажите advertised_url приложения и перезапустите его до установки моста.', 409)
        # Only fresh rooted devices. Never mutate unknown existing bridges.
        job['stage'] = 'Проверка совместимости и резервная копия'
        if self._check(speaker, job)['bridge_present']:
            return self._upgrade(speaker, job)
        original = self.ssh(speaker, 'cat /system/vendor/quasar/quasar.cfg')
        archive, manifest, installer = self._package(speaker, original)
        self.ssh(speaker, 'sh -s -- preflight', installer)
        self._space(speaker, len(archive) + 4 * 1024 * 1024)
        backup = self.store.directory / 'backups' / speaker['id'] / job['id']
        backup.mkdir(parents=True, mode=0o700)
        (backup / 'original.cfg').write_bytes(original)
        os.chmod(backup / 'original.cfg', 0o600)
        atomic_json(backup / 'receipt.json', {'speaker_id': speaker['id'], 'job_id': job['id'], 'manifest': manifest, 'created_at': int(time.time())})
        job['stage'] = 'Загрузка моста и проверка файлов'
        self.ssh(speaker, 'umask 077; mkdir ' + DEVICE + ' && tar -C ' + DEVICE + ' -xf -', archive, timeout=120)
        readback = self.ssh(speaker, 'cd ' + DEVICE + ' && sha256sum ' + ' '.join(manifest)).decode()
        observed = {row.split(None, 1)[1].strip().lstrip('*'): row.split(None, 1)[0] for row in readback.splitlines()}
        if observed != manifest:
            raise Error('Контрольные суммы не совпали. Загрузочные файлы не изменены.', 502)
        job['stage'] = 'Установка контролируемого запуска'
        try:
            self.ssh(speaker, 'sh ' + DEVICE + '/install-device.sh install', timeout=240)
        except Error:
            # Installer owns exact hook/mount checks; rollback may safely refuse if hook never existed.
            try:
                self.ssh(speaker, 'sh ' + DEVICE + '/install-device.sh rollback', timeout=120)
            except Error:
                pass
            raise Error('Установка остановлена. Резервная копия сохранена; состояние требует проверки перед повтором.', 502) from None
        self.store.metadata(speaker['id'], install={'job_id': job['id'], 'state': 'awaiting_reboot', 'backup': str(backup),
                                                  'kind': 'fresh', 'startup_receipt_job_id': job['id']})
        return {'state': 'awaiting_reboot', 'message': 'Мост установлен. Перезагрузите колонку, затем проверьте голос и «стоп». После загрузки действует 10-минутное автоматическое восстановление.'}

    def _packaged_startup(self, speaker):
        # Trust comes from shipped source, never from observed device hashes.
        if self.config.get('adoption_enabled') is not True:
            raise Error('Переход со старого моста недоступен в этой версии приложения.', 409)
        expected = {}
        for name in STARTUP_FILES:
            path = Path(self.config['packaging_dir']) / name
            if not path.is_file() or path.is_symlink():
                raise Error('Нет проверенного профиля старого моста.', 503)
            raw = path.read_bytes()
            if speaker['profile'] == 'midi':
                raw = raw.replace(b'/etc/init.d/S99quasar', b'/etc/init.d/S97quasar').replace(b'/etc/init.d/S98quasarbridge', b'/etc/init.d/S96quasarbridge')
            expected[name] = hashlib.sha256(raw).hexdigest()
        return expected

    @staticmethod
    def _runtime_values(raw):
        result = {}
        for line in raw.splitlines():
            if not line.strip() or line.lstrip().startswith('#'):
                continue
            key, sep, value = line.partition('=')
            if not sep or not re.fullmatch(r'[A-Z][A-Z0-9_]*', key):
                raise Error('Неподдерживаемый формат runtime старого моста. Колонка не изменена.', 409)
            try:
                parts = shlex.split(value, comments=True)
            except ValueError:
                raise Error('Неподдерживаемый формат runtime старого моста. Колонка не изменена.', 409) from None
            if len(parts) > 1 or key in result:
                raise Error('Неоднозначная настройка runtime. Колонка не изменена.', 409)
            result[key] = parts[0] if parts else ''
        return result

    def _adoption_runtime(self, speaker, original):
        values = self._runtime_values(original)
        template = self._runtime_values((Path(self.config['artifact_dir']) / 'runtime.env').read_text())
        url = self.config.get('advertised_url', '')
        parsed = urlsplit(url)
        try:
            valid = parsed.scheme == 'http' and lan_ip(parsed.hostname) == parsed.hostname and parsed.port and parsed.path in ('', '/') and not parsed.username and not parsed.password and not parsed.query and not parsed.fragment
        except (Error, ValueError, TypeError):
            valid = False
        token = template.get('QUASAR_HOME_FUNCTIONS_TOKEN', '')
        if not valid or not re.fullmatch(r'[A-Za-z0-9_-]{40,100}', token):
            raise Error('Сначала сохраните локальный адрес приложения в первом шаге мастера.', 409)
        values.update(QUASAR_HOME_FUNCTIONS_URL=url, QUASAR_HOME_FUNCTIONS_TOKEN=token,
                      QUASAR_HOME_AREA=speaker['area'], QUASAR_HOME_SPEAKER_ID=speaker['id'])
        return ''.join(key+'='+shlex.quote(value)+'\n' for key, value in values.items())

    def _adopt(self, speaker, job):
        if self.config.get('installation_ready') is False:
            raise Error('Сначала сохраните адрес приложения в первом шаге мастера.', 409)
        if not self._check(speaker, job)['bridge_present']:
            raise Error('Старый мост не найден. Используйте обычную установку.', 409)
        expected = self._packaged_startup(speaker)
        return self._upgrade(speaker, job, adoption=expected)

    def _restart_process(self, speaker):
        pids = self.ssh(speaker, 'pidof quasar-bridge').decode().split()
        if not pids or any(not p.isdecimal() for p in pids):
            raise Error('Не удалось найти процесс существующего моста.', 409)
        for pid in pids:
            command = self.ssh(speaker, 'cat /proc/' + pid + '/cmdline').split(b'\0')[0]
            if command not in (b'/data/opt/quasar-bridge/bin/quasar-bridge', b'/bkupgrade/data/opt/quasar-bridge/bin/quasar-bridge'):
                raise Error('Обнаружен неизвестный процесс моста; перезапуск запрещён.', 409)
        self.ssh(speaker, 'kill ' + ' '.join(pids))

    def _verified_health(self, speaker, area=None):
        try:
            health = json.loads(self.ssh(speaker, 'sleep 5; wget -T 4 -q -O - http://127.0.0.1:18861/healthz', timeout=15))
        except (Error, ValueError):
            raise Error('Мост не подтвердил успешный запуск.', 502) from None
        if not isinstance(health, dict) or health.get('status') != 'ok' or health.get('speaker_id') != speaker['id']:
            raise Error('Мост не подтвердил исправность и идентификатор колонки.', 502)
        if area is not None and health.get('speaker_area') != area:
            raise Error('Мост не подтвердил новую комнату.', 502)
        return health

    def _restart(self, speaker, job):
        job['stage'] = 'Перезапуск моста'
        self._restart_process(speaker)
        self._verified_health(speaker)
        self._monitor_speaker(speaker)
        return {'state': 'ready', 'message': 'Мост перезапущен и проверен.'}

    def _configure(self, speaker, job, params):
        job['stage'] = 'Проверка новой комнаты'
        # Secrets and all unrelated environment variables stay on their device.
        original = self.ssh(speaker, 'cat ' + DEVICE + '/runtime.env').decode()
        updated = '\n'.join(line for line in original.splitlines() if not line.startswith(('QUASAR_HOME_AREA=', 'QUASAR_HOME_SPEAKER_ID=')))
        updated += '\nQUASAR_HOME_AREA=' + shlex.quote(params['area']) + '\nQUASAR_HOME_SPEAKER_ID=' + shlex.quote(speaker['id']) + '\n'
        suffix = '.configure-' + job['id']
        stage = DEVICE + '/runtime.env' + suffix
        backup = DEVICE + '/runtime.env.backup-' + job['id']
        self.ssh(speaker, 'umask 077; cat > ' + stage, updated.encode())
        diagnostic = 'set -a; . ' + stage + '; set +a; ' + DEVICE + '/bin/quasar-bridge -config ' + DEVICE + '/config.json -check-config'
        try:
            self.ssh(speaker, diagnostic)
        except Error:
            self.ssh(speaker, 'rm -f ' + stage)
            raise
        self.ssh(speaker, 'cp -p ' + DEVICE + '/runtime.env ' + backup + ' && mv ' + stage + ' ' + DEVICE + '/runtime.env')
        try:
            job['stage'] = 'Применение комнаты и проверка моста'
            self._restart_process(speaker)
            self._verified_health(speaker, params['area'])
            item = self.store.save_speaker(params, speaker['id'])
        except Exception:
            try:
                self.ssh(speaker, 'cp -p ' + backup + ' ' + stage + ' && mv ' + stage + ' ' + DEVICE + '/runtime.env')
                self._restart_process(speaker)
                self._verified_health(speaker, speaker['area'])
            except Exception:
                raise Error('Настройка не прошла, восстановление не подтверждено. Проверьте колонку локально; резерв runtime сохранён.', 502) from None
            raise Error('Новая комната не подтверждена. Предыдущая настройка восстановлена.', 502) from None
        self._monitor_speaker(item)
        return {'state': 'ready', 'message': 'Комната применена и проверена.', 'speaker': item}

    def _upgrade(self, speaker, job, adoption=None):
        # Existing installations are recognized by server-pinned startup hashes,
        # their own current manifest and an unchanged, exactly owned init hook.
        artifact = Path(self.config['artifact_dir'])
        expected, receipt_job_id = (adoption, None) if adoption is not None else self._startup_ownership(speaker)
        if not isinstance(expected, dict) or not expected or any(not re.fullmatch(r'[A-Za-z0-9_.-]+', key) or not re.fullmatch(r'[a-f0-9]{64}', value) for key, value in expected.items()):
            raise Error('Некорректный серверный список разрешённых файлов запуска.', 503)
        startup = self.ssh(speaker, 'cd ' + DEVICE + ' && sha256sum ' + ' '.join(expected)).decode()
        observed = {row.split(None, 1)[1].strip().lstrip('*'): row.split(None, 1)[0] for row in startup.splitlines()}
        if observed != expected:
            raise Error('Файлы запуска существующего моста неизвестны. Автоматическое обновление запрещено.', 409)
        if adoption is not None:
            for name in (*STARTUP_FILES, 'config.json', 'runtime.env', 'manifest.json', 'original.cfg', 'bin/quasar-bridge'):
                self.ssh(speaker, 'test -f '+DEVICE+'/'+name+' && test ! -L '+DEVICE+'/'+name)
        hook = '/etc/init.d/S96quasarbridge' if speaker['profile'] == 'midi' else '/etc/init.d/S98quasarbridge'
        self.ssh(speaker, 'test ! -f ' + DEVICE + '/acceptance.pending && test ! -f ' + DEVICE + '/disabled && cmp -s ' + hook + ' ' + DEVICE + '/S98quasarbridge')
        manifest_raw = self.ssh(speaker, 'cat ' + DEVICE + '/manifest.json')
        manifest = json.loads(manifest_raw)
        config_bytes = self.ssh(speaker, 'cat ' + DEVICE + '/config.json')
        old_binary_hash = self.ssh(speaker, 'sha256sum ' + DEVICE + '/bin/quasar-bridge').decode().split()[0]
        if manifest.get('bin/quasar-bridge') != old_binary_hash or manifest.get('config.json') != hashlib.sha256(config_bytes).hexdigest():
            raise Error('Текущие файлы не совпадают с манифестом моста.', 409)
        original = self.ssh(speaker, 'cat ' + DEVICE + '/original.cfg')
        archive, _, _ = self._package(speaker, original)
        # _package validates artifacts, but upgrades preserve existing routing and secrets.
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            binary = tar.extractfile('bin/quasar-bridge').read()
        runtime = self.ssh(speaker, 'cat ' + DEVICE + '/runtime.env').decode()
        old_runtime = runtime
        old_id = self._runtime_values(runtime).get('QUASAR_HOME_SPEAKER_ID') if adoption is not None else None
        runtime = '\n'.join(line for line in runtime.splitlines() if not line.startswith(('QUASAR_HOME_AREA=', 'QUASAR_HOME_SPEAKER_ID=')))
        runtime += '\nQUASAR_HOME_AREA=' + shlex.quote(speaker['area']) + '\nQUASAR_HOME_SPEAKER_ID=' + shlex.quote(speaker['id']) + '\n'
        if adoption is not None:
            if not isinstance(old_id, str) or not ID.fullmatch(old_id):
                raise Error('Старый мост не сообщил корректный идентификатор. Переход остановлен.', 409)
            runtime = self._adoption_runtime(speaker, old_runtime)
            # Store only private rollback data; never expose runtime via the API.
            server_backup = self.store.directory / 'backups' / speaker['id'] / job['id']
            server_backup.mkdir(parents=True, mode=0o700)
            for name, raw in [('runtime.env', old_runtime.encode()), ('config.json', config_bytes), ('manifest.json', json.dumps(manifest).encode()), ('original.cfg', original)]:
                path = server_backup/name
                fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
                with os.fdopen(fd, 'wb') as stream:
                    stream.write(raw)
            atomic_json(server_backup/'receipt.json', {'speaker_id':speaker['id'], 'job_id':job['id'], 'manifest':expected, 'source':'packaged-startup'})
            receipt_job_id = job['id']
        def unchanged():
            if adoption is not None:
                hashes = {**expected, 'bin/quasar-bridge':old_binary_hash, 'config.json':hashlib.sha256(config_bytes).hexdigest(),
                          'runtime.env':hashlib.sha256(old_runtime.encode()).hexdigest(), 'manifest.json':hashlib.sha256(manifest_raw).hexdigest()}
                self.ssh(speaker, 'cd '+DEVICE+' && sha256sum -c -', ''.join(value+'  '+name+'\n' for name,value in hashes.items()).encode())
        self._space(speaker, len(binary) + len(runtime.encode()) + 512 * 1024)
        suffix = '.manager-' + job['id']
        backup = DEVICE + '/manager-backup-' + job['id']
        # Same-filesystem hard link preserves the old inode across atomic mv,
        # without allocating a second full binary on small UBIFS partitions.
        unchanged()
        self.ssh(speaker, 'umask 077; test -f ' + DEVICE + '/bin/quasar-bridge && test ! -L ' + DEVICE + '/bin/quasar-bridge && mkdir ' + backup
                 + ' && ln ' + DEVICE + '/bin/quasar-bridge ' + backup + '/quasar-bridge && cp -p ' + DEVICE + '/config.json ' + DEVICE + '/runtime.env ' + DEVICE + '/manifest.json ' + backup + '/')
        try:
            self.ssh(speaker, 'umask 077; cat > ' + DEVICE + '/bin/quasar-bridge' + suffix + ' && chmod 700 ' + DEVICE + '/bin/quasar-bridge' + suffix, binary, timeout=90)
            self.ssh(speaker, 'umask 077; cat > ' + DEVICE + '/runtime.env' + suffix, runtime.encode())
            uploaded = self.ssh(speaker, 'sha256sum ' + DEVICE + '/bin/quasar-bridge' + suffix).decode().split()[0]
            if uploaded != hashlib.sha256(binary).hexdigest():
                raise Error('Контрольная сумма новой версии не совпала. Работающий мост не изменён.', 502)
            preflight = 'set -a; . ' + DEVICE + '/runtime.env' + suffix + '; set +a; ' + DEVICE + '/bin/quasar-bridge' + suffix + ' -config ' + DEVICE + '/config.json '
            self.ssh(speaker, preflight + '-check-config', timeout=90)
            self.ssh(speaker, preflight + '-check-backends', timeout=90)
            unchanged()
        except Exception:
            # Remove only this job's exact staging paths; backups stay until the upgrade is verified.
            try:
                self.ssh(speaker, 'rm -f ' + DEVICE + '/bin/quasar-bridge' + suffix + ' ' + DEVICE + '/runtime.env' + suffix)
            except Error:
                pass
            raise
        job['stage'] = 'Обновление и проверка запуска'
        pending = {'job_id':job['id'], 'kind':'upgrade', 'adopted':True, 'state':'adopting', 'backup':backup,
                   'previous_speaker_id':old_id, 'startup_receipt_job_id':receipt_job_id}
        if adoption is not None:
            self.store.metadata(speaker['id'], install=pending)
        try:
            self.ssh(speaker, 'mv ' + DEVICE + '/bin/quasar-bridge' + suffix + ' ' + DEVICE + '/bin/quasar-bridge && mv ' + DEVICE + '/runtime.env' + suffix + ' ' + DEVICE + '/runtime.env')
            self._restart_process(speaker)
            health = json.loads(self.ssh(speaker, 'sleep 5; wget -T 4 -q -O - http://127.0.0.1:18861/healthz', timeout=15))
            if health.get('status') != 'ok' or health.get('speaker_id') != speaker['id']:
                raise Error('Новая версия не подтвердила идентификатор и исправность.', 502)
            if adoption is not None and health.get('speaker_area') != speaker['area']:
                raise Error('Мост не подтвердил комнату после перехода.', 502)
            manifest['bin/quasar-bridge'] = hashlib.sha256(binary).hexdigest()
            self.ssh(speaker, 'umask 077; cat > ' + DEVICE + '/manifest.json', (json.dumps(manifest) + '\n').encode())
        except Exception:
            try:
                self.ssh(speaker, 'ln ' + backup + '/quasar-bridge ' + DEVICE + '/bin/quasar-bridge.rollback && mv ' + DEVICE + '/bin/quasar-bridge.rollback ' + DEVICE + '/bin/quasar-bridge && cp -p ' + backup + '/config.json ' + backup + '/runtime.env ' + backup + '/manifest.json ' + DEVICE + '/')
                self._restart_process(speaker)
                if adoption is not None:
                    self._verified_health({**speaker, 'id':old_id})
                    self.store.metadata(speaker['id'], install={**pending, 'state':'rolled_back'})
            except Exception:
                raise Error('Переход остановлен, восстановление пока не подтверждено. Резерв сохранён. Проверьте связь и нажмите «Вернуть прежний мост».', 502) from None
            raise Error('Проверка обновления не прошла. Предыдущие файлы восстановлены; проверьте состояние колонки.', 502) from None
        install = {'job_id': job['id'], 'state': 'ready', 'backup': backup, 'kind': 'upgrade'}
        if receipt_job_id:
            install['startup_receipt_job_id'] = receipt_job_id
        if adoption is not None:
            install.update(adopted=True, previous_speaker_id=old_id, state='awaiting_acceptance')
        self.store.metadata(speaker['id'], install=install)
        if adoption is None:
            self._prune_backups(speaker, job['id'])
        return {'state': install['state'], 'message': 'Колонка подключена к приложению. Проверьте голос; при проблеме нажмите «Вернуть прежний мост».' if adoption is not None else 'Мост обновлён, идентификатор и исправность проверены.', 'backup': backup}

    def _prune_backups(self, speaker, job_id):
        # Speaker flash is small: after a verified upgrade only this job's rollback copy stays.
        keep = 'manager-backup-' + job_id
        try:
            self.ssh(speaker, 'cd ' + DEVICE + ' && for d in manager-backup-*; do [ "$d" = ' + keep + ' ] || rm -rf "./$d"; done')
        except Error:
            pass

    def drop_backups(self, speaker):
        # A voice request proved the update, and the server keeps the package: flash stays free.
        self._prune_backups(speaker, '')
        install = self.store.speaker(speaker['id']).get('install', {})
        self.store.metadata(speaker['id'], install={**install, 'backup': ''})

    def _startup_ownership(self, speaker):
        install = self.store.speaker(speaker['id']).get('install', {})
        receipt_job_id = install.get('startup_receipt_job_id')
        if receipt_job_id:
            if not ID.fullmatch(speaker['id']) or not isinstance(receipt_job_id, str) or not re.fullmatch(r'[a-f0-9]{16}', receipt_job_id):
                raise Error('Некорректная ссылка на серверную квитанцию установки.', 409)
            backup_root = self.store.directory.resolve() / 'backups'
            directory = backup_root / speaker['id'] / receipt_job_id
            receipt_path = directory / 'receipt.json'
            # Never read a browser-supplied path or follow a replacement symlink.
            if directory.resolve() != directory or receipt_path.is_symlink() or not receipt_path.is_file():
                raise Error('Серверная квитанция установки недоступна; обновление запрещено.', 409)
            receipt = json.loads(receipt_path.read_text())
            if receipt.get('speaker_id') != speaker['id'] or receipt.get('job_id') != receipt_job_id:
                raise Error('Квитанция относится к другой колонке или установке.', 409)
            manifest = receipt.get('manifest', {})
            expected = {name: manifest.get(name) for name in STARTUP_FILES}
            if any(not isinstance(value, str) or not re.fullmatch(r'[a-f0-9]{64}', value) for value in expected.values()):
                raise Error('Квитанция не содержит полный проверенный состав запуска.', 409)
            return expected, receipt_job_id
        ownership_path = Path(self.config['artifact_dir']) / 'existing-startup-sha256.json'
        if not ownership_path.is_file():
            raise Error('На колонке уже установлен мост от другой установки. Новое приложение не получило данные для его обновления. Откройте руководство «Переход со старого моста». Повторная установка без переноса заблокирована.', 409)
        expected = json.loads(ownership_path.read_text())
        if speaker['profile'] in expected:
            expected = expected[speaker['profile']]
        return expected, None

    def _managed_install(self, speaker):
        install = self.store.speaker(speaker['id']).get('install')
        if not install:
            raise Error('Менеджер не устанавливал этот мост; управление загрузкой запрещено.', 409)
        return install

    def _reboot(self, speaker, job):
        install = self._managed_install(speaker)
        if install['state'] != 'awaiting_reboot':
            raise Error('Колонка не ожидает первой перезагрузки.', 409)
        self.ssh(speaker, 'sync; (sleep 2; reboot) </dev/null >/dev/null 2>&1 &')
        self.store.metadata(speaker['id'], install={**install, 'state': 'awaiting_acceptance'})
        return {'state': 'awaiting_acceptance', 'message': 'Перезагрузка запрошена. Дождитесь запуска и проверьте голос, ответ и остановку. Затем подтвердите проверку в течение 10 минут.'}

    def _confirm(self, speaker, job):
        install = self._managed_install(speaker)
        if install['state'] != 'awaiting_acceptance':
            raise Error('Колонка не ожидает проверки голоса.', 409)
        if install.get('adopted'):
            self._verified_health(speaker, speaker['area'])
        else:
            self.ssh(speaker, 'sh ' + DEVICE + '/install-device.sh confirm', timeout=30)
        self.store.metadata(speaker['id'], install={**install, 'state': 'ready'})
        return {'state': 'ready'}

    def _rollback(self, speaker, job):
        install = self._managed_install(speaker)
        if install.get('kind') == 'upgrade':
            backup = install.get('backup', '')
            if not re.fullmatch(re.escape(DEVICE) + r'/manager-backup-[a-f0-9]{16}', backup):
                raise Error('Неизвестная резервная копия.', 409)
            self.ssh(speaker, 'ln ' + backup + '/quasar-bridge ' + DEVICE + '/bin/quasar-bridge.rollback && mv ' + DEVICE + '/bin/quasar-bridge.rollback ' + DEVICE + '/bin/quasar-bridge && cp -p ' + backup + '/config.json ' + backup + '/runtime.env ' + backup + '/manifest.json ' + DEVICE + '/')
            self._restart_process(speaker)
            if install.get('adopted'):
                self._verified_health({**speaker, 'id':install['previous_speaker_id']})
            self.store.metadata(speaker['id'], install={**install, 'state': 'rolled_back'})
            return {'state': 'rolled_back', 'message': 'Предыдущая версия восстановлена.'}
        self.ssh(speaker, 'sh ' + DEVICE + '/install-device.sh rollback', timeout=120)
        self.store.metadata(speaker['id'], install={**install, 'state': 'rolled_back'})
        return {'state': 'rolled_back', 'message': 'Мост отключён, исходная конфигурация восстановлена. Перезагрузите колонку для сброса кешированного маршрута.'}


def authenticate(ha_url, token):
    """HA has WS auth/current_user, not a documented REST current_user endpoint."""
    from websockets.sync.client import connect
    parsed = urlsplit(ha_url)
    uri = ('wss' if parsed.scheme == 'https' else 'ws') + '://' + parsed.netloc + '/api/websocket'
    try:
        with connect(uri, open_timeout=5, close_timeout=1, max_size=65536) as websocket:
            if json.loads(websocket.recv(timeout=5)).get('type') != 'auth_required':
                raise Error('Home Assistant не подтвердил авторизацию.', 401)
            websocket.send(json.dumps({'type': 'auth', 'access_token': token}))
            if json.loads(websocket.recv(timeout=5)).get('type') != 'auth_ok':
                raise Error('Войдите в Home Assistant заново.', 401)
            websocket.send(json.dumps({'id': 1, 'type': 'auth/current_user'}))
            result = json.loads(websocket.recv(timeout=5))
            if not result.get('success') or result.get('result', {}).get('is_admin') is not True:
                raise Error('Управление колонками доступно только администратору.', 403)
    except Error:
        raise
    except Exception:
        raise Error('Не удалось проверить права в Home Assistant.', 503) from None


class Handler(BaseHTTPRequestHandler):
    server_version = 'AliceManager/1'

    def log_message(self, *_args):
        pass  # No tokens, URLs, request bodies or SSH diagnostics in access logs.

    def send(self, status, value):
        data = json.dumps(value, ensure_ascii=False).encode()
        self.send_response(status)
        origin = self.headers.get('Origin')
        if origin == self.server.manager.config['ha_origin']:
            self.send_header('Access-Control-Allow-Origin', origin)
        self.send_header('Vary', 'Origin')
        self.send_header('Cache-Control', 'no-store')
        self.send_header('Content-Type', 'application/json; charset=utf-8')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_OPTIONS(self):
        if self.headers.get('Origin') != self.server.manager.config['ha_origin']:
            return self.send(403, {'error': 'Origin запрещён.'})
        self.send_response(204)
        self.send_header('Access-Control-Allow-Origin', self.server.manager.config['ha_origin'])
        self.send_header('Access-Control-Allow-Methods', 'GET, POST, PATCH, PUT, DELETE, OPTIONS')
        self.send_header('Access-Control-Allow-Headers', 'Authorization, Content-Type')
        self.send_header('Access-Control-Max-Age', '600')
        self.send_header('Vary', 'Origin')
        self.end_headers()

    def _request(self):
        manager = self.server.manager
        try:
            origin = self.headers.get('Origin')
            if origin is not None and origin != manager.config['ha_origin']:
                raise Error('Origin запрещён.', 403)
            bearer = self.headers.get('Authorization', '')
            if not bearer.startswith('Bearer ') or not bearer[7:] or len(bearer) > 10000:
                raise Error('Требуется авторизация Home Assistant.', 401)
            authenticate(manager.config['ha_url'], bearer[7:])
            path = unquote(urlsplit(self.path).path).strip('/').split('/')
            payload = {}
            if self.command in ('POST', 'PATCH', 'PUT'):
                try:
                    length = int(self.headers.get('Content-Length', '0'))
                    if not 0 <= length <= 65536:
                        raise ValueError()
                    payload = json.loads(self.rfile.read(length) or b'{}')
                    if not isinstance(payload, dict):
                        raise ValueError()
                except (ValueError, UnicodeDecodeError):
                    raise Error('Некорректный JSON запроса.') from None
            status, result = self.route(manager, path, payload)
            self.send(status, result)
        except Error as error:
            self.send(error.status, {'error': str(error)})
        except Exception:
            self.send(500, {'error': 'Внутренняя ошибка менеджера.'})

    def route(self, manager, path, payload):
        method = self.command
        if path == ['api', 'inventory'] and method == 'GET':
            return 200, manager._hf_request('/inventory')
        if path == ['api', 'access'] and method in ('GET', 'PUT'):
            from admin import access
            return 200, access(manager, payload if method == 'PUT' else None)
        if len(path) == 4 and path[:2] == ['api', 'speakers'] and ID.fullmatch(path[2]) and path[3] in ('file-read', 'file-write', 'console', 'indicator', 'volume', 'airplay') and method == 'POST':
            from admin import station
            return 200, station(manager, path[2], path[3], payload)
        if path == ['api', 'setup'] and method == 'GET':
            return 200, manager.setup()
        if len(path) == 4 and path[:2] == ['api', 'speakers'] and ID.fullmatch(path[2]) and path[3] == 'dictionary' and method == 'GET':
            return 200, manager._hf_request('/dictionary', manager.store.speaker(path[2]))
        if path == ['api', 'dictionary', 'alias'] and method == 'POST':
            if not {'alias', 'entity_id'} <= set(payload) <= {'alias', 'entity_id', 'remove'}:
                raise Error('Ожидаются alias и entity_id.')
            return 200, manager._hf_request('/dictionary/alias', None, {'alias': text(payload['alias'], 'alias', 60), 'entity_id': text(payload['entity_id'], 'entity_id', 200), 'remove': payload.get('remove') is True})
        if path == ['api', 'spotify'] and method == 'GET':
            return 200, manager._hf_request('/spotify')
        if len(path) == 3 and path[:2] == ['api', 'spotify'] and path[2] in ('login', 'callback', 'sync') and method == 'POST':
            return 200, manager._hf_request('/spotify/' + path[2], None, payload or {})
        if path == ['api', 'music'] and method == 'GET':
            return 200, manager._hf_request('/music')
        if path == ['api', 'music'] and method == 'PUT':
            return 200, manager._hf_request('/music', None, payload)
        if path == ['api', 'phrase-check'] and method == 'POST':
            return 200, manager.phrase_check(payload)
        if path == ['api', 'activity'] and method == 'GET':
            return 200, manager._hf_request('/events')
        if path == ['api', 'auto-update'] and method == 'POST':
            import updater
            if set(payload) == {'enabled'} and isinstance(payload['enabled'], bool):
                return 200, updater.set_enabled(manager, payload['enabled'])
            raise Error('Ожидается enabled.')
        if path == ['api', 'speakers']:
            if method == 'GET':
                return 200, {'speakers': manager.speakers(), **manager.setup()}
            if method == 'POST':
                candidate = {**payload, 'host': lan_ip(payload.get('host'))}
                manager.validate_speaker(candidate)
                return 201, manager.store.save_speaker(candidate)
        if len(path) >= 3 and path[:2] == ['api', 'speakers'] and ID.fullmatch(path[2]):
            speaker_id = path[2]
            if len(path) == 3:
                if method == 'PATCH':
                    old = manager.store.speaker(speaker_id)
                    if 'area' in payload and payload['area'] != old['area']:
                        raise Error('Изменение комнаты применяйте через операцию configure с проверкой моста.', 409)
                    candidate = {**old, **payload}
                    manager.validate_speaker(candidate)
                    with manager.jobs_lock:
                        if any(j['speaker_id'] == speaker_id and j['status'] == 'running' for j in manager.jobs.values()):
                            raise Error('Дождитесь завершения операции на колонке.', 409)
                        return 200, manager.store.save_speaker(payload, speaker_id)
                if method == 'DELETE':
                    with manager.jobs_lock:
                        if any(j['speaker_id'] == speaker_id and j['status'] == 'running' for j in manager.jobs.values()):
                            raise Error('Дождитесь завершения операции на колонке.', 409)
                        manager.store.delete(speaker_id)
                    return 200, {'deleted': True}
            if len(path) == 4 and method == 'POST':
                operation = path[3]
                if operation == 'fingerprint':
                    return 200, manager.fingerprints(speaker_id)
                if operation == 'trust':
                    return 200, manager.trust(speaker_id, payload)
                if operation == 'confirm' and payload.get('voice_tested') is not True:
                    raise Error('Подтвердите, что голос, ответ и остановка проверены на колонке.')
                return 202, manager.job(speaker_id, operation, payload if operation == 'configure' else None)
        if path == ['api', 'assignments'] and method == 'GET':
            return 200, {'assignments': manager.store.read('assignments.json')}
        if len(path) == 3 and path[:2] == ['api', 'assignments'] and method == 'PUT':
            if set(payload) != {'speakers'}:
                raise Error('Ожидается поле speakers.')
            return 200, manager.store.assign(path[2], payload['speakers'])
        if len(path) == 3 and path[:2] == ['api', 'jobs'] and method == 'GET':
            with manager.jobs_lock:
                if path[2] not in manager.jobs:
                    raise Error('Операция не найдена. История очищается при перезапуске менеджера.', 404)
                return 200, dict(manager.jobs[path[2]])
        raise Error('Маршрут не найден.', 404)

    do_GET = do_POST = do_PATCH = do_PUT = do_DELETE = _request


def main():
    config_path = Path(os.environ.get('ALICE_MANAGER_CONFIG', '/opt/alice-manager/config.json'))
    config = json.loads(config_path.read_text())
    config.setdefault('ha_origin', 'http://homeassistant:8123')
    config.setdefault('ha_url', config['ha_origin'])
    bind = '127.0.0.1' if config.get('local_only') is True else lan_ip(config['bind'])
    parsed = urlsplit(config['ha_origin'])
    if parsed.scheme not in ('http', 'https') or parsed.path or parsed.query or parsed.fragment or parsed.username:
        raise Error('ha_origin должен быть точным HTTP(S) origin.')
    key = Path(config['ssh_key'])
    if key.is_symlink() or not key.is_file() or key.stat().st_mode & 0o077:
        raise Error('SSH-ключ должен быть приватным обычным файлом с правами 0600.')
    server = ThreadingHTTPServer((bind, config.get('port', 18085)), Handler)
    server.manager = Manager(config)
    server.manager.setup()  # Derive/cache public metadata before accepting HTTP.
    server.manager.start_monitor()
    try:
        server.serve_forever()
    finally:
        server.manager.stop_monitor()
        server.server_close()


if __name__ == '__main__':
    main()
