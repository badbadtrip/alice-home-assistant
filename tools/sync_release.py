"""Copy the legal notices into the app, build DOCS.md and refresh the SHA-256 manifest."""
import hashlib
import json
from pathlib import Path
import shutil

ROOT=Path(__file__).resolve().parents[1]
APP=ROOT/'alice'
IGNORED={'node_modules','__pycache__'}


def sync():
    # alice/ is the only copy of the changelog and the wiki: it is what the app ships.
    for name in ('NOTICE','BRIDGE-SOURCE.json'):
        shutil.copyfile(ROOT/name,APP/name)
    shutil.copyfile(APP/'wiki/Installation.md',APP/'DOCS.md')
    files=[p for p in APP.rglob('*') if p.is_file() and p.name!='source-manifest.json' and not IGNORED&set(p.parts)]
    # Hash the LF bytes Git ships, not CRLF from a Windows editor.
    for path in files:
        raw=path.read_bytes()
        if b'\r\n' in raw:path.write_bytes(raw.replace(b'\r\n',b'\n'))
    manifest={p.relative_to(APP).as_posix():hashlib.sha256(p.read_bytes()).hexdigest() for p in files}
    (APP/'source-manifest.json').write_text(json.dumps(dict(sorted(manifest.items())),indent=2)+'\n',encoding='utf-8',newline='\n')
    print('Manifest refreshed:',len(manifest),'files.')


if __name__=='__main__':
    sync()
