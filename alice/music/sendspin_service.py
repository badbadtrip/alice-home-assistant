"""Music Assistant players for the speakers, over Sendspin.

Each managed speaker becomes one Sendspin player. This process dials Music
Assistant itself (client-initiated), so the app needs no host network. Audio
arrives as raw PCM and is relayed to the speaker's bridge, which holds one
long-lived request on GET /stream and plays it through the speaker's own ALSA
helper. Home Functions proxies that path and checks the speaker token; this
server listens on loopback only.

Frames to the bridge (big-endian):
  b'F' u32 rate u8 channels   a stream starts or changes format
  b'A' u32 length + PCM       S16LE interleaved audio
  b'V' u8 volume u8 muted     player volume 0-100 and mute from Music Assistant
  b'C'                        drop buffered audio now (seek, skip)
  b'E'                        the stream ended (pause, stop)
  b'K'                        keepalive while idle
"""
from __future__ import annotations

import asyncio
import base64
import collections
import contextlib
import json
import logging
import os
import re
import struct
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit
from typing import Any

from aiohttp import ClientSession, ClientTimeout, web
from aiosendspin.client import PairingSupport, SendspinClient
from aiosendspin.models.core import DeviceInfo
from aiosendspin.models.player import ClientHelloPlayerSupport, SupportedAudioFormat
from aiosendspin.models.types import AudioCodec, MediaCommand, PlayerCommand, Roles
from aiosendspin.noise import Identity
from aiosendspin.noise.trust_store import FileClientPairingStore

log = logging.getLogger('alice.sendspin')

FORMATS = [
    SupportedAudioFormat(codec=AudioCodec.PCM, channels=2, sample_rate=48000, bit_depth=16),
    SupportedAudioFormat(codec=AudioCodec.PCM, channels=2, sample_rate=44100, bit_depth=16),
]
# What Music Assistant may send ahead of real time (~1.25 s at 48 kHz). The bridge plays
# as audio arrives, so this bounds how late a pause or skip is heard.
BUFFER_CAPACITY = 240_000
# Audio waiting for the bridge. Beyond this the oldest goes: a speaker that stopped
# reading must not grow memory, and a slow drift between clocks must not grow delay.
MAX_QUEUED = 2 * 48000 * 4
KEEPALIVE_S = 5.0
RECONNECT_S = (2, 5, 10, 30)
MANUFACTURER = 'Alice for Home Assistant'
MEDIA_TYPES = {'track': 'track', 'artist': 'artist', 'album': 'album', 'playlist': 'playlist'}
# How long the speaker waits for Music Assistant before the search goes on in the background.
PLAY_ANSWER_S = 8
PLAY_TIMEOUT_S = 60
MIX_SEEDS = 5
TOP_TRACKS = 25
MA_API_PORT = 8095
COMMANDS = {'pause': MediaCommand.PAUSE, 'play': MediaCommand.PLAY, 'resume': MediaCommand.PLAY,
            'stop': MediaCommand.STOP, 'next': MediaCommand.NEXT, 'previous': MediaCommand.PREVIOUS}


def frame(kind: bytes, payload: bytes = b'') -> bytes:
    return kind + payload


class Outlet:
    """The bridge's open request: an ordered, bounded queue of frames."""

    def __init__(self) -> None:
        self.frames: collections.deque[bytes] = collections.deque()
        self.audio_bytes = 0
        self.ready = asyncio.Event()
        self.closed = False
        self.dropped = 0

    def put(self, data: bytes, *, audio: bool = False) -> None:
        if self.closed:
            return
        self.frames.append(data)
        if audio:
            self.audio_bytes += len(data)
            while self.audio_bytes > MAX_QUEUED:
                for i, old in enumerate(self.frames):
                    if old[:1] == b'A':
                        del self.frames[i]
                        self.audio_bytes -= len(old)
                        self.dropped += len(old)
                        break
                else:
                    break
        self.ready.set()

    def drop_audio(self) -> None:
        self.frames = collections.deque(f for f in self.frames if f[:1] != b'A')
        self.audio_bytes = 0

    async def take(self, timeout: float) -> bytes | None:
        if not self.frames:
            self.ready.clear()
            with contextlib.suppress(TimeoutError):
                await asyncio.wait_for(self.ready.wait(), timeout)
        if not self.frames:
            return None
        data = self.frames.popleft()
        if data[:1] == b'A':
            self.audio_bytes -= len(data)
        return data


class Player:
    def __init__(self, service: Service, speaker: dict[str, Any]) -> None:
        self.service = service
        self.id = speaker['id']
        self.name = player_name(speaker)
        # The speaker's stock media player: Alice's own voice for a late "not found".
        self.media_entity = speaker.get('media_entity') if isinstance(speaker.get('media_entity'), str) else None
        self.dir = service.data / self.id
        self.client: SendspinClient | None = None
        self.task: asyncio.Task | None = None
        self.outlet: Outlet | None = None
        self.format: tuple[int, int] | None = None
        self.streaming = False
        self.connected = False
        self.pairing_code: str | None = None
        self.metadata: dict[str, Any] = {}
        # A skip asked by the speaker: (action, when) until the next track sounds.
        self.switching: tuple[str, float] | None = None
        self.cleared = 0.0
        settings = self.settings()
        self.volume = int(settings.get('volume', 100))
        self.muted = bool(settings.get('muted', False))

    # -- persistence ---------------------------------------------------------------
    def settings(self) -> dict[str, Any]:
        try:
            return json.loads((self.dir / 'settings.json').read_text())
        except (OSError, ValueError):
            return {}

    def save(self, **values: Any) -> None:
        path = self.dir / 'settings.json'
        tmp = path.with_suffix('.tmp')
        tmp.write_text(json.dumps({**self.settings(), **values}))
        os.replace(tmp, path)

    def identity(self) -> Identity:
        path = self.dir / 'identity.json'
        if path.exists():
            raw = json.loads(path.read_text())['private_key']
            return Identity.from_private_bytes(base64.urlsafe_b64decode(raw + '=' * (-len(raw) % 4)))
        identity = Identity.generate()
        tmp = path.with_suffix('.tmp')
        tmp.write_text(json.dumps({'private_key': identity.private_b64u}))
        os.chmod(tmp, 0o600)
        os.replace(tmp, path)
        return identity

    # -- lifecycle -----------------------------------------------------------------
    async def start(self) -> None:
        self.dir.mkdir(mode=0o700, parents=True, exist_ok=True)
        store = await FileClientPairingStore.open(self.dir / 'pairing.json')
        client = SendspinClient(
            identity=self.identity(), client_name=self.name,
            roles=[Roles.PLAYER, Roles.METADATA, Roles.CONTROLLER],
            pairing_store=store,
            pairing_support=PairingSupport(pairing_code_display=self.show_code),
            device_info=DeviceInfo(product_name='Яндекс Станция', manufacturer=MANUFACTURER),
            player_support=ClientHelloPlayerSupport(supported_formats=FORMATS, buffer_capacity=BUFFER_CAPACITY),
            state_supported_commands=[PlayerCommand.VOLUME, PlayerCommand.MUTE],
            initial_volume=self.volume, initial_muted=self.muted,
        )
        client.add_stream_start_listener(self.on_start)
        client.add_audio_chunk_listener(self.on_chunk)
        client.add_stream_clear_listener(self.on_clear)
        client.add_stream_end_listener(self.on_end)
        client.add_server_command_listener(self.on_command)
        client.add_metadata_listener(self.on_metadata)
        client.add_disconnect_listener(self.on_disconnect)
        self.client = client
        self.task = asyncio.create_task(self.keep_connected())

    async def stop(self) -> None:
        if self.task:
            self.task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await self.task
        if self.client:
            with contextlib.suppress(Exception):
                await self.client.disconnect()
        self.end_stream()

    async def keep_connected(self) -> None:
        attempt = 0
        while True:
            if not self.connected:
                try:
                    await self.client.connect(self.service.urls[attempt % len(self.service.urls)])
                    self.connected = True
                    attempt = 0
                    log.info('%s: подключено к Music Assistant', self.name)
                except asyncio.CancelledError:
                    raise
                except Exception as error:  # noqa: BLE001 - the server may be absent for hours
                    delay = RECONNECT_S[min(attempt, len(RECONNECT_S) - 1)]
                    if attempt in (0, 3):
                        log.warning('%s: Music Assistant недоступен (%s), повтор', self.name, type(error).__name__)
                    attempt += 1
                    await asyncio.sleep(delay)
                    continue
            await asyncio.sleep(2)

    def on_disconnect(self) -> None:
        if self.connected:
            log.info('%s: Music Assistant отключился', self.name)
        self.connected = False
        self.end_stream()

    async def show_code(self, code: str | None, *, grouped: str | None = None) -> None:
        self.pairing_code = grouped or code
        await self.service.notify_pairing(self)

    # -- audio ---------------------------------------------------------------------
    def on_start(self, message: Any) -> None:
        player = message.payload.player
        if player is None:
            return
        if self.switching is not None and not self.cleared:
            self.cleared = time.monotonic()
        fmt = (player.sample_rate, player.channels)
        if not self.streaming or fmt != self.format:
            self.format = fmt
            self.send(frame(b'F', struct.pack('>IB', *fmt)))
        self.streaming = True

    def on_chunk(self, _ts: int, data: bytes, _fmt: Any, _ahead: int) -> None:
        if self.switching is not None and self.cleared:
            # The first sound of the next track: how long a skip really took.
            action, began = self.switching
            log.info('%s: переключение (%s): сброс через %.0f мс, первый звук через %.0f мс', self.name, action,
                     (self.cleared - began) * 1000, (time.monotonic() - began) * 1000)
            self.switching, self.cleared = None, 0.0
        if self.streaming and self.outlet is not None:
            self.outlet.put(frame(b'A', struct.pack('>I', len(data)) + data), audio=True)

    def on_clear(self, roles: list[str] | None) -> None:
        if self.switching is not None and not self.cleared:
            self.cleared = time.monotonic()
        if roles is None or 'player' in roles:
            if self.outlet is not None:
                self.outlet.drop_audio()
            self.send(b'C')

    def on_end(self, roles: list[str] | None) -> None:
        if roles is None or 'player' in roles:
            self.end_stream()

    def end_stream(self) -> None:
        was = self.streaming
        self.streaming = False
        self.format = None
        if self.outlet is not None:
            self.outlet.drop_audio()
            if was:
                self.outlet.put(b'E')

    def send(self, data: bytes) -> None:
        if self.outlet is not None:
            self.outlet.put(data)

    def on_command(self, payload: Any) -> None:
        command = payload.player
        if command is None:
            return
        if command.command == PlayerCommand.VOLUME and command.volume is not None:
            self.volume = max(0, min(100, int(command.volume)))
        elif command.command == PlayerCommand.MUTE and command.mute is not None:
            self.muted = bool(command.mute)
        else:
            return
        self.save(volume=self.volume, muted=self.muted)
        self.send(frame(b'V', struct.pack('>BB', self.volume, int(self.muted))))
        if self.client is not None and self.connected:
            asyncio.create_task(self.report())

    async def report(self) -> None:
        with contextlib.suppress(Exception):
            await self.client.send_player_state(available=True, volume=self.volume, muted=self.muted)

    def on_metadata(self, state: Any) -> None:
        metadata = getattr(state, 'metadata', None) if state is not None else None
        if metadata is not None and hasattr(metadata, 'title'):
            self.metadata = {'title': metadata.title, 'artist': metadata.artist, 'album': getattr(metadata, 'album', None)}

    # -- the bridge's request ------------------------------------------------------
    async def serve(self, request: web.Request) -> web.StreamResponse:
        if self.outlet is not None:
            self.outlet.closed = True
            self.outlet.ready.set()  # the older request ends; this one takes over
        outlet = self.outlet = Outlet()
        outlet.put(frame(b'V', struct.pack('>BB', self.volume, int(self.muted))))
        if self.streaming and self.format:
            outlet.put(frame(b'F', struct.pack('>IB', *self.format)))
        response = web.StreamResponse(headers={'Content-Type': 'application/octet-stream', 'Cache-Control': 'no-store'})
        await response.prepare(request)
        try:
            while not outlet.closed:
                data = await outlet.take(KEEPALIVE_S)
                if outlet.closed:
                    break
                await asyncio.wait_for(response.write(data if data is not None else b'K'), 15)
        except (ConnectionResetError, TimeoutError, asyncio.CancelledError):
            pass
        finally:
            outlet.closed = True
            if self.outlet is outlet:
                self.outlet = None
            if outlet.dropped:
                log.warning('%s: колонка не успевала читать поток, отброшено %d байт', self.name, outlet.dropped)
        return response

    async def command(self, action: str) -> None:
        if self.client is None or not self.connected:
            raise web.HTTPServiceUnavailable(text='Music Assistant не подключён')
        if action not in COMMANDS:
            raise web.HTTPBadRequest(text='unknown action')
        if action in ('next', 'previous'):
            self.switching, self.cleared = (action, time.monotonic()), 0.0
        try:
            await self.client.send_group_command(COMMANDS[action])
        except ValueError as error:
            raise web.HTTPConflict(text=str(error)) from error

    def status(self) -> dict[str, Any]:
        return {'name': self.name, 'connected': self.connected, 'streaming': self.streaming,
                'listening': self.outlet is not None, 'pairing_code': self.pairing_code,
                'volume': self.volume, 'muted': self.muted, 'now_playing': self.metadata if self.streaming else None}


def fold(value: Any) -> str:
    return ' '.join(str(value or '').casefold().replace('ё', 'е').split())


def stem(value: Any) -> str:
    # "веселый" asks for "Весёлая": Russian endings differ, the stem does not.
    return ' '.join(word.rstrip('аеиоуыэюяйь') or word for word in fold(value).split())


ARTIST_TAIL = re.compile(r'\s+(?:группы|исполнителя|артиста|певицы|певца|от)\s+(.+)$')


def split_artist(query: str) -> tuple[str, str]:
    # "выходи за меня группы anacondaz" -> the title and the artist to prefer.
    match = ARTIST_TAIL.search(query)
    return (query[:match.start()].strip(), match.group(1).strip()) if match else (query, '')


def player_name(speaker: dict[str, Any]) -> str:
    area = str(speaker.get('area') or '').strip()
    name = str(speaker.get('name') or '').strip()
    return f'Алиса {area}' if area else (name or 'Алиса')


class Service:
    def __init__(self, speakers_path: Path, data: Path, url: str) -> None:
        self.speakers_path = speakers_path
        self.data = data
        self.url = url
        self.urls = [u.strip() for u in url.split(',') if u.strip()]
        self.players: dict[str, Player] = {}
        self.notified: dict[str, str | None] = {}
        self.tasks: set[asyncio.Task] = set()

    def read_speakers(self) -> list[dict[str, Any]]:
        try:
            value = json.loads(self.speakers_path.read_text(encoding='utf-8'))
        except (OSError, ValueError):
            return []
        return [s for s in value if isinstance(s, dict) and isinstance(s.get('id'), str)] if isinstance(value, list) else []

    async def sync(self) -> None:
        wanted = {s['id']: s for s in self.read_speakers()}
        for speaker_id in [i for i in self.players if i not in wanted or player_name(wanted[i]) != self.players[i].name]:
            await self.players.pop(speaker_id).stop()
        for speaker_id, speaker in wanted.items():
            if speaker_id in self.players:
                media = speaker.get('media_entity')
                self.players[speaker_id].media_entity = media if isinstance(media, str) else None
            if speaker_id not in self.players:
                player = Player(self, speaker)
                self.players[speaker_id] = player
                await player.start()

    async def watch(self) -> None:
        while True:
            with contextlib.suppress(Exception):
                await self.sync()
            await asyncio.sleep(15)

    async def notify_pairing(self, player: Player) -> None:
        code = player.pairing_code
        if self.notified.get(player.id) == code:
            return
        self.notified[player.id] = code
        if code:
            log.warning('%s: Music Assistant просит PIN-код %s', player.name, code)
        token = os.environ.get('SUPERVISOR_TOKEN')
        if not token:
            return
        notification = f'alice_sendspin_{player.id}'
        if code:
            service, data = 'create', {'notification_id': notification, 'title': 'Алиса: подключение к Music Assistant',
                                       'message': f'Music Assistant просит PIN-код для плеера «{player.name}». Введите: **{code}**'}
        else:
            service, data = 'dismiss', {'notification_id': notification}
        with contextlib.suppress(Exception):
            async with ClientSession(timeout=ClientTimeout(total=10)) as session:
                await session.post(f'http://supervisor/core/api/services/persistent_notification/{service}',
                                   json=data, headers={'Authorization': f'Bearer {token}'})

    # -- HTTP ----------------------------------------------------------------------
    def player(self, request: web.Request) -> Player:
        player = self.players.get(request.headers.get('X-Alice-Speaker', ''))
        if player is None:
            raise web.HTTPNotFound(text='unknown speaker')
        return player

    async def stream(self, request: web.Request) -> web.StreamResponse:
        return await self.player(request).serve(request)

    async def control(self, request: web.Request) -> web.Response:
        try:
            body = await request.json()
        except ValueError:
            raise web.HTTPBadRequest(text='invalid json') from None
        await self.player(request).command(str(body.get('action', '')) if isinstance(body, dict) else '')
        return web.json_response({'ok': True})

    async def play(self, request: web.Request) -> web.Response:
        """Find and play music on this speaker's player through Home Assistant's Music Assistant."""
        try:
            body = await request.json()
        except ValueError:
            raise web.HTTPBadRequest(text='invalid json') from None
        if not isinstance(body, dict):
            raise web.HTTPBadRequest(text='invalid json')
        player = self.player(request)
        kind, query = str(body.get('kind') or ''), str(body.get('query') or '').strip()[:200]
        if not query and kind not in ('liked', ''):
            raise web.HTTPNotFound(text='nothing to play')
        clock = asyncio.get_running_loop().time
        started = clock()
        entity, state, entry = await self.entity(player)
        found_entity = clock()
        if not entity:
            raise web.HTTPServiceUnavailable(text='Music Assistant player is not in Home Assistant')
        if state == 'unavailable':
            raise web.HTTPServiceUnavailable(text='Music Assistant player is unavailable')
        data: dict[str, Any] = {'entity_id': entity, 'media_id': query, 'enqueue': 'replace'}
        rest: list[str] = []
        radio = False
        if kind in MEDIA_TYPES:
            data['media_type'] = MEDIA_TYPES[kind]
        if not kind and not query:
            # "Включи музыку": a mix made for the owner. Liked tracks seed Music Assistant's
            # radio, which goes on with similar music from the streaming service.
            seeds = (await self.favorites(entry))[:MIX_SEEDS] if entry else []
            log.info('%s: подборка: %d треков-образцов за %.0f мс', player.name, len(seeds), (clock() - found_entity) * 1000)
            if not seeds:  # nothing liked yet: the queue that was playing goes on
                await player.command('play')
                return web.json_response({'ok': True})
            # Radio mode makes Music Assistant gather similar music before the first note
            # (over 8 s with Apple Music). The first liked track starts at once; the other
            # seeds join the queue with radio mode while it already sounds.
            data, rest, radio = {'entity_id': entity, 'media_id': seeds[0], 'enqueue': 'replace'}, seeds[1:] or seeds[:1], True
        elif kind == 'liked':
            # "Мои лайки": the tracks marked as favourite in Music Assistant, shuffled.
            liked = await self.favorites(entry) if entry else []
            log.info('%s: избранное: %d треков за %.0f мс', player.name, len(liked), (clock() - found_entity) * 1000)
            if not liked:
                raise web.HTTPNotFound(text='no favourite tracks')
            data, rest = {'entity_id': entity, 'media_id': liked[0], 'enqueue': 'replace'}, liked[1:]
        elif entry and kind in MEDIA_TYPES:
            # Music Assistant would take the first loose match (or, for an artist, fetch the
            # whole discography first). The app picks the item itself and starts it at once.
            title, by = split_artist(query)
            if kind == 'artist':
                tracks = await self.by_artist(entry, title)
                first, rest = (tracks[0], tracks[1:]) if tracks else ('', [])
            else:
                first, found_artist, found_name = await self.pick(entry, kind, title, by)
                rest = []
                if first and kind == 'track':
                    # One song would leave silence after it: more by the same artist follows.
                    artist = by or found_artist
                    rest = [uri for uri in (await self.by_artist(entry, artist, found_name) if artist else []) if uri != first]
            log.info('%s: поиск «%s» (%s) за %.0f мс: %s', player.name, query, kind, (clock() - found_entity) * 1000,
                     'найдено' if first else 'ничего')
            if not first:
                raise web.HTTPNotFound(text='nothing found')
            # Music Assistant loads every listed track before it starts, so the first item
            # plays alone and the rest join the queue while it already sounds.
            data = {'entity_id': entity, 'media_id': first, 'enqueue': 'replace'}
        # A slow provider can hold Music Assistant's search for many seconds. The speaker
        # waits a short while; after that the request goes on here, and only a real
        # failure is spoken, late, in Alice's own voice.
        task = asyncio.create_task(self.home('POST', '/services/music_assistant/play_media', data, timeout=PLAY_TIMEOUT_S))
        if rest:
            self.background(self.queue_rest(task, data['entity_id'], rest, radio=radio))
        asked = clock()
        done, _ = await asyncio.wait({task}, timeout=PLAY_ANSWER_S)
        log.info('%s: Music Assistant: сущность %.0f мс, запуск %s за %.0f мс', player.name, (found_entity - started) * 1000,
                 'ещё идёт' if not done else 'ответил', (clock() - asked) * 1000)
        if not done:
            self.background(self.finish_play(player, task))
            return web.json_response({'ok': True, 'pending': True})
        status, _ = task.result()
        if status == 401 or status == 0:
            raise web.HTTPServiceUnavailable(text='Home Assistant did not answer')
        if status >= 300:  # Music Assistant refuses a query it cannot resolve
            raise web.HTTPNotFound(text='nothing found')
        return web.json_response({'ok': True})

    async def queue_rest(self, first: asyncio.Task, entity: str, rest: list[str], *, radio: bool = False) -> None:
        status, _ = await first
        if status == 200:
            data: dict[str, Any] = {'entity_id': entity, 'media_id': rest, 'enqueue': 'add'}
            if radio:
                data['radio_mode'] = True
            await self.home('POST', '/services/music_assistant/play_media', data, timeout=PLAY_TIMEOUT_S)

    async def finish_play(self, player: Player, task: asyncio.Task) -> None:
        status, _ = await task
        if status == 200:
            return
        text = 'Не нашла это в Music Assistant.' if status >= 300 else 'Music Assistant не ответил.'
        log.info('%s: поиск в Music Assistant завершился с ошибкой %s', player.name, status)
        if player.media_entity:
            await self.home('POST', '/services/media_player/play_media',
                            {'entity_id': player.media_entity, 'media_content_id': text, 'media_content_type': 'text'})

    def background(self, coroutine: Any) -> None:
        task = asyncio.create_task(coroutine)
        self.tasks.add(task)
        task.add_done_callback(self.tasks.discard)

    async def entity(self, player: Player) -> tuple[str, str]:
        # The HA entity belongs to Music Assistant's own player id, not the Sendspin id,
        # so it is found by the device this service describes and the player's name.
        template = ("{%- for e in integration_entities('music_assistant') if e.startswith('media_player.') "
                    "and device_attr(e, 'manufacturer') == '" + MANUFACTURER + "' "
                    "and device_attr(e, 'name') == name -%}{{ e }}|{{ states(e) }}|{{ config_entry_id(e) }};{%- endfor -%}")
        status, text = await self.home('POST', '/template', {'template': template, 'variables': {'name': player.name}})
        if status != 200:
            return '', '', ''
        rows = [row.split('|', 2) for row in text.split(';') if row.count('|') == 2]
        return (rows[0][0], rows[0][1], rows[0][2]) if len(rows) == 1 else ('', '', '')

    async def search(self, entry: str, kind: str, name: str, *, library: bool = False, limit: int = 10) -> list[dict[str, Any]]:
        status, text = await self.home('POST', '/services/music_assistant/search?return_response',
                                       {'config_entry_id': entry, 'name': name, 'media_type': [kind], 'limit': limit,
                                        'library_only': library})
        if status != 200:
            return []
        try:
            found = json.loads(text).get('service_response', {}).get(kind + 's') or []
        except (ValueError, AttributeError):
            return []
        return [item for item in found if isinstance(item, dict) and isinstance(item.get('uri'), str)]

    async def pick(self, entry: str, kind: str, title: str, by: str) -> tuple[str, str, str]:
        # The owner's own playlists come before the catalogue's; for the rest the service's
        # relevance order stands, with the exact title and the named artist first.
        found: list[dict[str, Any]] = []
        if kind == 'playlist':
            found = await self.search(entry, kind, title, library=True)
        found = found or await self.search(entry, kind, f'{title} {by}'.strip())
        wanted, artist, close = fold(title), fold(by), stem(title)
        def score(index_item: tuple[int, dict[str, Any]]) -> tuple[int, int, int, int]:
            index, item = index_item
            name = fold(item.get('name'))
            artists = ' '.join(fold(a.get('name')) for a in item.get('artists') or [] if isinstance(a, dict))
            near = stem(name)
            return (0 if name == wanted else 1 if wanted in name or name in wanted or near == close else
                    2 if close in near or near in close else 3,
                    0 if not artist or artist in artists else 1,
                    1 if kind != 'playlist' and item['uri'].startswith('library://') else 0, index)
        if not found:
            return '', '', ''
        best = min(enumerate(found), key=score)[1]
        if kind == 'playlist' and close not in stem(best.get('name')):
            return '', '', ''  # a stranger's playlist with another name is not what was asked for
        names = [str(a.get('name')) for a in best.get('artists') or [] if isinstance(a, dict) and a.get('name')]
        return best['uri'], names[0] if names else '', str(best.get('name') or '')

    async def favorites(self, entry: str) -> list[str]:
        status, text = await self.home('POST', '/services/music_assistant/get_library?return_response',
                                       {'config_entry_id': entry, 'media_type': 'track', 'favorite': True,
                                        'limit': 50, 'order_by': 'random'})
        if status != 200:
            return []
        try:
            items = json.loads(text).get('service_response', {}).get('items') or []
        except (ValueError, AttributeError):
            return []
        return [item['uri'] for item in items if isinstance(item, dict) and isinstance(item.get('uri'), str)]

    async def by_artist(self, entry: str, artist: str, skip: str = '') -> list[str]:
        # The service's own "top tracks" of the artist when Music Assistant's API is open
        # to the app; otherwise the tracks a search finds under the artist's name.
        return await self.top_tracks(entry, artist, skip) or await self.artist_tracks(entry, artist, skip)

    async def top_tracks(self, entry: str, artist: str, skip: str = '') -> list[str]:
        if not os.environ.get('ALICE_MA_TOKEN'):
            return []
        wanted = fold(artist)
        found = [a for a in await self.search(entry, 'artist', artist, limit=5) if fold(a.get('name')) == wanted]
        # A streaming service's artist has the service's chart; a library one may not.
        found.sort(key=lambda a: a['uri'].startswith('library://'))
        for item in found[:2]:
            match = re.fullmatch(r'([a-z0-9_-]+)://artist/(.+)', item['uri'])
            if not match:
                continue
            tracks = await self.ma_api('music/artists/top_tracks',
                                       {'item_id': match.group(2), 'provider_instance_id_or_domain': match.group(1)})
            uris = [t['uri'] for t in tracks if isinstance(t, dict) and isinstance(t.get('uri'), str)
                    and (not skip or fold(t.get('name')) != fold(skip))] if isinstance(tracks, list) else []
            if uris:
                return uris[:TOP_TRACKS]
        return []

    async def ma_api(self, command: str, args: dict[str, Any]) -> Any:
        token = os.environ.get('ALICE_MA_TOKEN')
        if not token:
            return None
        for base in self.api_urls():
            try:
                async with ClientSession(timeout=ClientTimeout(total=10)) as session:
                    async with session.post(base, json={'command': command, 'args': args},
                                            headers={'Authorization': f'Bearer {token}'}) as reply:
                        if reply.status == 200:
                            return await reply.json(content_type=None)
                        log.warning('Music Assistant API %s: %s %s', command, reply.status, (await reply.text())[:200])
                        return None
            except (OSError, TimeoutError, ValueError):
                continue
        return None

    def api_urls(self) -> list[str]:
        # Music Assistant's API sits next to its Sendspin server, on its web port.
        hosts = [urlsplit(url.strip()).hostname for url in self.url.split(',')]
        return [f'http://{host}:{MA_API_PORT}/api' for host in hosts if host]

    async def artist_tracks(self, entry: str, artist: str, skip: str = '') -> list[str]:
        found = await self.search(entry, 'track', artist, limit=25)
        if skip:  # the song already chosen, also under its other copies
            found = [t for t in found if fold(t.get('name')) != fold(skip)]
        wanted = artist.casefold()
        def by_artist(track: dict[str, Any]) -> bool:
            return any(wanted in str(a.get('name', '')).casefold() for a in track.get('artists') or [] if isinstance(a, dict))
        tracks = [t for t in found if isinstance(t, dict) and isinstance(t.get('uri'), str)]
        chosen = [t['uri'] for t in tracks if by_artist(t)] or [t['uri'] for t in tracks]
        # Library entries may point to a provider that is now off: streaming ones go first.
        return sorted(chosen, key=lambda uri: uri.startswith('library://'))

    async def home(self, method: str, path: str, data: Any, timeout: float = 15) -> tuple[int, str]:
        token = os.environ.get('ALICE_HA_TOKEN') or os.environ.get('SUPERVISOR_TOKEN')
        base = os.environ.get('ALICE_HA_URL') or 'http://supervisor/core/api'
        if not token:
            return 0, ''
        try:
            async with ClientSession(timeout=ClientTimeout(total=timeout)) as session:
                async with session.request(method, base + path, json=data, headers={'Authorization': f'Bearer {token}'}) as reply:
                    return reply.status, (await reply.text())[:65536]
        except (OSError, TimeoutError):
            return 0, ''

    async def state(self, request: web.Request) -> web.Response:
        speaker = request.headers.get('X-Alice-Speaker')
        if speaker:
            return web.json_response(self.player(request).status())
        return web.json_response({i: p.status() for i, p in self.players.items()} | {'_url': self.url})


async def main() -> None:
    logging.basicConfig(level=logging.INFO, stream=sys.stdout, format='sendspin: %(message)s')
    logging.getLogger('aiosendspin').setLevel(logging.WARNING)
    service = Service(Path(os.environ['ALICE_SPEAKERS_PATH']), Path(os.environ['ALICE_SENDSPIN_DATA']),
                      os.environ.get('ALICE_SENDSPIN_URL') or 'ws://172.30.32.1:8927/sendspin')
    service.data.mkdir(mode=0o700, parents=True, exist_ok=True)
    app = web.Application()
    for prefix in ('', '/sendspin'):  # the second form serves a bridge directly during development
        app.router.add_get(prefix + '/stream', service.stream)
        app.router.add_post(prefix + '/control', service.control)
        app.router.add_post(prefix + '/play', service.play)
        app.router.add_get(prefix + '/state', service.state)
    runner = web.AppRunner(app, handle_signals=False, access_log=None)
    await runner.setup()
    await web.TCPSite(runner, os.environ.get('ALICE_SENDSPIN_HOST', '127.0.0.1'), int(os.environ.get('ALICE_SENDSPIN_PORT', '18095'))).start()
    log.info('Sendspin: %s', service.url)
    try:
        await service.watch()
    finally:
        for player in list(service.players.values()):
            await player.stop()
        await runner.cleanup()


if __name__ == '__main__':
    with contextlib.suppress(KeyboardInterrupt):
        asyncio.run(main())
