"""Offline, validated legacy import before services start. No archive extractall."""
import hashlib
import io
import json
import os
from pathlib import Path,PurePosixPath
import re
import shutil
import subprocess
import tarfile
from bootstrap import write_private


def import_state(source=Path('/config/import.tar'),data=Path('/data')):
    if not source.exists():return False
    if source.is_symlink() or source.stat().st_mode & 0o077:
        raise ValueError('import.tar должен быть приватным обычным файлом 0600')
    digest=hashlib.sha256(source.read_bytes()).hexdigest()
    marker=data/'migration.complete.json'
    if marker.exists():
        previous=json.loads(marker.read_text())
        if previous.get('sha256')!=digest:raise ValueError('Миграция уже выполнена; новый импорт заблокирован')
        source.unlink();return False
    current=data/'state/speakers.json'
    if current.exists() and json.loads(current.read_text()):
        raise ValueError('Импорт разрешён только в пустой реестр приложения')
    entries={};total=0
    with tarfile.open(source,mode='r:*') as archive:
        for member in archive:
            path=PurePosixPath(member.name)
            allowed=member.name.startswith('state/') or member.name in ('ssh/id_ed25519','ssh/id_ed25519.pub','functions.token','existing-startup-sha256.json')
            if path.is_absolute() or '..' in path.parts or '\\' in member.name or not allowed:
                raise ValueError('Недопустимый путь в архиве миграции')
            if member.isdir():continue
            total+=member.size
            if not member.isfile() or member.size>32*1024*1024 or total>256*1024*1024 or member.name in entries:
                raise ValueError('Недопустимый размер или тип файла миграции')
            entries[member.name]=archive.extractfile(member).read()
    required={'state/speakers.json','state/assignments.json','state/known_hosts','ssh/id_ed25519','ssh/id_ed25519.pub','functions.token'}
    if not required.issubset(entries):raise ValueError('Неполный архив миграции')
    speakers=json.loads(entries['state/speakers.json']);assignments=json.loads(entries['state/assignments.json'])
    if not isinstance(speakers,list) or not isinstance(assignments,dict):raise ValueError('Некорректный реестр миграции')
    ids=[s.get('id','') for s in speakers]
    if len(set(ids))!=len(ids) or any(not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,63}',x) for x in ids):raise ValueError('Некорректные ID колонок')
    if any(not isinstance(v,list) or any(x not in ids for x in v) for v in assignments.values()):raise ValueError('Некорректные назначения')
    token=entries['functions.token'].decode().strip()
    if not re.fullmatch(r'[A-Za-z0-9_-]{40,100}',token):raise ValueError('Некорректный токен миграции')
    staging=data/'migration-staging'
    staging.mkdir(parents=True,exist_ok=True,mode=0o700)
    for name,raw in entries.items():
        target=staging/name;target.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
        target.write_bytes(raw);target.chmod(0o600)
    public=subprocess.run(['ssh-keygen','-y','-f',str(staging/'ssh/id_ed25519')],capture_output=True,timeout=5,check=True).stdout.decode().split()[:2]
    if public!=entries['ssh/id_ed25519.pub'].decode().split()[:2]:raise ValueError('Пара SSH-ключей не совпадает')
    backup=data/'migration-before'
    if backup.exists():raise ValueError('Найдена незавершённая миграция; сначала восстановите данные')
    backup.mkdir(mode=0o700)
    for name in ('state','ssh','functions.token'):
        path=data/name
        if path.is_dir():shutil.copytree(path,backup/name)
        elif path.exists():shutil.copy2(path,backup/name)
    try:
        for directory in ('state','ssh'):
            old=data/directory
            if old.exists():shutil.rmtree(old)
            os.replace(staging/directory,old)
        write_private(data/'functions.token',token)
        if 'existing-startup-sha256.json' in entries:
            write_private(data/'artifacts/existing-startup-sha256.json',entries['existing-startup-sha256.json'].decode())
        write_private(marker,json.dumps({'sha256':digest,'speaker_count':len(speakers)}))
        source.unlink()
        shutil.rmtree(staging)
        return True
    except Exception:
        marker.unlink(missing_ok=True)
        for name in ('state','ssh','functions.token'):
            target=data/name;saved=backup/name
            if target.is_dir():shutil.rmtree(target)
            elif target.exists():target.unlink()
            if saved.is_dir():shutil.copytree(saved,target)
            elif saved.exists():shutil.copy2(saved,target)
        raise
