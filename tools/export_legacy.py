"""Create a private archive for the app's offline importer. Never print secrets."""
import argparse
import io
import json
import os
from pathlib import Path
import re
import shlex
import tarfile


def export_legacy(manager, output, token_file=None, env_file=None):
    manager, output = Path(manager), Path(output)
    if (token_file is None) == (env_file is None):
        raise ValueError('Choose one token file or environment file')
    if token_file:
        token = Path(token_file).read_text().strip()
    else:
        values = {}
        for line in Path(env_file).read_text().splitlines():
            name, sep, raw = line.partition('=')
            if sep and name.strip() == 'HOME_FUNCTIONS_TOKEN':
                parsed = shlex.split(raw, comments=True)
                if len(parsed) == 1:
                    values[name.strip()] = parsed[0]
        token = values.get('HOME_FUNCTIONS_TOKEN', '')
    if not re.fullmatch(r'[A-Za-z0-9_-]{40,100}', token):
        raise ValueError('A valid HOME_FUNCTIONS_TOKEN was not found')
    if manager.is_symlink() or (manager/'state').is_symlink():
        raise ValueError('Source directories must not be symbolic links')
    files = {}
    for path in (manager/'state').rglob('*'):
        if path.is_symlink():
            raise ValueError('State must not contain symbolic links')
        if path.is_file() and path.suffix not in ('.tar', '.gz', '.zip'):
            files['state/'+path.relative_to(manager/'state').as_posix()] = path
    for name in ('id_ed25519', 'id_ed25519.pub'):
        files['ssh/'+name] = manager/'ssh'/name
    pin = manager/'artifacts/existing-startup-sha256.json'
    if pin.exists():
        files['existing-startup-sha256.json'] = pin
    required = {'state/speakers.json', 'state/assignments.json', 'state/known_hosts'}
    if not required.issubset(files):
        raise ValueError('Source manager registry is incomplete')
    payload = {}
    for name, path in files.items():
        if path.is_symlink() or not path.is_file() or path.stat().st_size > 32*1024*1024:
            raise ValueError('Unsupported source file: '+name)
        payload[name] = path.read_bytes()
    if sum(map(len, payload.values())) > 256*1024*1024:
        raise ValueError('Source state exceeds import size limit')
    if not isinstance(json.loads(payload['state/speakers.json']), list):
        raise ValueError('Invalid source speaker registry')
    if not isinstance(json.loads(payload['state/assignments.json']), dict):
        raise ValueError('Invalid source assignments')
    # Reject changes during the snapshot instead of exporting mixed revisions.
    if any(path.read_bytes() != payload[name] for name, path in files.items()):
        raise ValueError('Source changed during export; pause the old manager and retry')
    payload['functions.token'] = token.encode()
    descriptor = os.open(output, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, 'wb') as stream, tarfile.open(fileobj=stream, mode='w') as archive:
            for name, raw in payload.items():
                item = tarfile.TarInfo(name)
                item.size, item.mode = len(raw), 0o600
                archive.addfile(item, io.BytesIO(raw))
    except Exception:
        output.unlink(missing_ok=True)
        raise
    return len(json.loads(payload['state/speakers.json']))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--manager-dir', required=True)
    parser.add_argument('--output', required=True)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument('--token-file')
    source.add_argument('--env-file')
    args = parser.parse_args()
    try:
        count = export_legacy(args.manager_dir, args.output, args.token_file, args.env_file)
    except (ValueError, OSError, tarfile.TarError) as error:
        parser.exit(1, 'Export failed: '+str(error)+'\n')
    print('Private archive created; speakers:', count)
