"""Staged automatic bridge updates: right after an app update, one speaker at a time, with rollback.

Health cannot prove the microphone works, so an update counts as finished only
after the next voice request reaches the bridge. Any failure stops the rollout of that version on every speaker.
"""
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import time
from urllib.request import Request, urlopen

INTERVAL = 30
VERIFY_SECONDS = 300
SETTINGS = 'auto-update.json'
# Silence proves the mixer accepts audio: a stale dmix lock hangs every player.
MIXER_CHECK = ('(head -c 44100 /dev/zero | aplay -q -D default -t raw -f S16_LE -c 2 -r 44100) & p=$!; i=0; '
               'while [ $i -lt 6 ]; do sleep 1; i=$((i+1)); kill -0 $p 2>/dev/null || exit 0; done; killall aplay; sleep 1; '
               # A restart during playback can leave dmix locked; with no clients left it is safe to clear.
               'n=$(ipcs -m | awk \'$1=="0x00000400"||$1=="0x00000401"{s+=$6} END{print s+0}\'); '
               '[ "$n" = 0 ] && ipcrm -M 0x400 -M 0x401 -S 0x400; exit 1')
AIRPLAY_RETRY = 600
_next_run = {}
_airplay_retry = {}
_music_sent = {}


def enabled(manager):
    path = manager.store.directory / SETTINGS
    return not path.exists() or json.loads(path.read_text(encoding='utf-8')).get('enabled') is not False


def set_enabled(manager, value):
    from service import atomic_json
    atomic_json(manager.store.directory / SETTINGS, {'enabled': value})
    return {'enabled': value}


def target(manager):
    path = Path(manager.config['artifact_dir']) / 'quasar-bridge'
    return hashlib.sha256(path.read_bytes()).hexdigest() if path.is_file() else None


def _ha(path, payload=None):
    token = os.environ.get('SUPERVISOR_TOKEN')
    if not token:
        return None
    data = None if payload is None else json.dumps(payload).encode()
    request = Request('http://supervisor/core/api/' + path, data=data, headers={'Authorization': 'Bearer ' + token, 'Content-Type': 'application/json'})
    with urlopen(request, timeout=10) as response:
        return json.load(response)


def notify(message):
    try:
        _ha('services/persistent_notification/create', {'title': 'Мост Алисы', 'message': message, 'notification_id': 'alice_bridge_update'})
    except Exception:
        pass


def _verified(manager, speaker):
    try:
        health = json.loads(manager.bridge_get(speaker, '/healthz'))
        if health.get('status') != 'ok' or health.get('speaker_id') != speaker['id'] or not health.get('client_frames'):
            return False
        manager.ssh(speaker, MIXER_CHECK, timeout=12)
        return True
    except Exception:
        return False


def _set(manager, speaker, version, state, **values):
    manager.store.metadata(speaker['id'], bridge_update={'state': state, 'target': version, 'at': int(time.time()), **values})


def _job_status(manager, job_id):
    with manager.jobs_lock:
        return (manager.jobs.get(job_id) or {}).get('status', 'lost')


def _heard(manager, speaker, since):
    for event in manager._hf_request('/events').get('events', []):
        if event.get('speaker_id') == speaker['id'] and datetime.fromisoformat(event['at'].replace('Z', '+00:00')).timestamp() >= since:
            return True
    return False


def _advance(manager, speaker, update, now):
    """Moves an update in flight. Returns True while it blocks the next speaker."""
    state, version = update.get('state'), update.get('target')
    if state == 'updating':
        status = _job_status(manager, update.get('job_id'))
        if status == 'running':
            return True
        if status == 'succeeded':
            _set(manager, speaker, version, 'verifying')
            return True
        _set(manager, speaker, version, 'failed')
        notify(speaker['name'] + ': обновление моста не прошло, предыдущая версия оставлена.')
        return True
    if state == 'verifying':
        if _verified(manager, speaker):
            _set(manager, speaker, version, 'awaiting_voice')
        elif now - update.get('at', 0) >= VERIFY_SECONDS:
            job = manager.job(speaker['id'], 'rollback')
            _set(manager, speaker, version, 'rolling_back', job_id=job['job_id'])
        return True
    if state == 'awaiting_voice':
        try:
            if _heard(manager, speaker, update.get('at', now)):
                _set(manager, speaker, version, 'current')
                manager.drop_backups(speaker)
        except Exception:
            pass
        return False
    if state == 'rolling_back':
        if _job_status(manager, update.get('job_id')) == 'running':
            return True
        _set(manager, speaker, version, 'failed')
        notify(speaker['name'] + ': после обновления проверка не прошла, мост возвращён на прежнюю версию.')
        return True
    return False


def airplay_default(manager, speakers, now):
    """AirPlay is on unless the user switched it off; a capable bridge starts with it off."""
    from admin import station
    for speaker in speakers:
        with manager.connection_lock:
            value = manager.connections.get(speaker['id'], {}).get('value', {})
        airplay = value.get('airplay')
        if value.get('status') != 'online' or not airplay or airplay.get('enabled') or speaker.get('airplay_off'):
            continue
        if now < _airplay_retry.get(speaker['id'], 0):
            continue
        _airplay_retry[speaker['id']] = now + AIRPLAY_RETRY
        try:
            station(manager, speaker['id'], 'airplay', {'action': 'on'})
        except Exception:
            pass


def music_state(manager, speakers):
    """Tells a bridge what HA knows about the stock player, which the bridge cannot see."""
    for speaker in speakers:
        entity = speaker.get('media_entity')
        with manager.connection_lock:
            value = manager.connections.get(speaker['id'], {}).get('value', {})
        if not entity or value.get('status') != 'online' or not value.get('airplay'):
            continue
        try:
            state = _ha('states/' + entity)
            playing = bool(state) and state.get('state') == 'playing'
            if _music_sent.get(speaker['id']) != playing:
                manager.bridge_get(speaker, '/music-state?playing=%d' % playing)
                _music_sent[speaker['id']] = playing
        except Exception:
            pass


def cycle(manager, now=None):
    now = time.time() if now is None else now
    if now < _next_run.get(id(manager), 0):
        return
    _next_run[id(manager)] = now + INTERVAL
    speakers = [s for s in manager.store.read('speakers.json') if s.get('trusted_fingerprint')]
    airplay_default(manager, speakers, now)
    music_state(manager, speakers)
    # In-flight work finishes even if the switch was turned off meanwhile.
    for speaker in speakers:
        if _advance(manager, speaker, speaker.get('bridge_update') or {}, now):
            return
    version = target(manager)
    if not version or not enabled(manager):
        return
    if any((s.get('bridge_update') or {}).get('state') == 'failed' and s['bridge_update'].get('target') == version for s in speakers):
        return
    for speaker in speakers:
        update = speaker.get('bridge_update') or {}
        if update.get('target') == version and update.get('state') in ('current', 'awaiting_voice'):
            continue
        if speaker.get('install', {}).get('state') != 'ready':
            continue
        with manager.connection_lock:
            online = manager.connections.get(speaker['id'], {}).get('value', {}).get('status') == 'online'
        if not online:
            continue
        try:
            actual = manager.ssh(speaker, 'sha256sum /data/opt/quasar-bridge/bin/quasar-bridge', timeout=12).decode().split()[0]
            if actual == version:
                _set(manager, speaker, version, 'current')
                continue
            job = manager.job(speaker['id'], 'install')
        except Exception:
            continue
        _set(manager, speaker, version, 'updating', job_id=job['job_id'])
        return
