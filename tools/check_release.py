"""Verify source integrity and reject deployment material before distribution."""
import argparse
import hashlib
import json
from pathlib import Path
import re

parser=argparse.ArgumentParser()
parser.add_argument('--forbid-value',action='append',default=[],help='Deployment-specific strings to exclude from source')
args=parser.parse_args()
ROOT=Path(__file__).resolve().parents[1]
APP=ROOT/'alice'
manifest=json.loads((APP/'source-manifest.json').read_text())
errors=[]
for name,digest in manifest.items():
    path=APP/name
    if path.is_symlink() or not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest()!=digest:
        errors.append('Source checksum mismatch: '+name)
for path in APP.rglob('*'):
    if not path.is_file() or '__pycache__' in path.parts:continue
    name=path.relative_to(APP).as_posix()
    if path.name in ('runtime.env','.env.local','id_ed25519','speakers.json','assignments.json','import.tar'):
        errors.append('Deployment file: '+name)
    text=path.read_text(encoding='utf-8',errors='replace')
    if any(value in text for value in args.forbid_value):
        errors.append('Legacy deployment address: '+name)
    if re.search(r'-----BEGIN (?:OPENSSH |RSA |EC )?PRIVATE KEY-----|AIza[A-Za-z0-9_-]{30,}|github_pat_[A-Za-z0-9_]{30,}',text):
        errors.append('Possible credential: '+name)
if errors:raise SystemExit('\n'.join(errors))
print('Release source verified:',len(manifest),'checksums; no deployment files, legacy addresses or key signatures.')
