"""Build a distributable context from an explicit allowlist, never a workspace copy."""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import re

ROOT=Path(__file__).resolve().parents[1]
WORK=ROOT.parent
# Maintained only in this repository; no external source can restore them.
APP_OWNED=('manager/admin.py','ui/IMAGE_SOURCES.md','ui/alice-voice-card.js')


def copy(source,target):
    target.parent.mkdir(parents=True,exist_ok=True)
    if source.is_symlink():raise ValueError('Source symlinks are not packaged')
    # All allowlisted inputs are source text. Normalize Windows checkouts for sh/Go.
    target.write_text(source.read_text(encoding='utf-8-sig').replace('\r\n','\n'),encoding='utf-8',newline='\n')


def prepare(bridge):
    bridge=Path(bridge).resolve()
    if not all((bridge/x).is_file() for x in ('go.mod','go.sum','LICENSE','NOTICE','THIRD_PARTY_NOTICES')):
        raise ValueError('Expected audited bridge source with Go module and license notices')
    out=ROOT/'alice'
    owned={name:(out/name).read_bytes() for name in APP_OWNED}
    for directory in ('bridge','server','manager','packaging','ui','wiki'):
        target=out/directory
        if target.exists():
            # Fixed children of this repository, never arbitrary input paths.
            if target.is_symlink() or target.resolve().parent!=out.resolve():raise ValueError('Unsafe output')
            shutil.rmtree(target)
    for source in bridge.rglob('*'):
        relative=source.relative_to(bridge)
        if source.is_file() and '.git' not in relative.parts and (source.suffix=='.go' or relative.as_posix() in ('go.mod','go.sum','LICENSE','NOTICE','THIRD_PARTY_NOTICES','config.example.json','config.mcp.example.json','config.observe.example.json')):
            copy(source,out/'bridge'/relative)
    hf=WORK/'home-functions'
    for directory in ('src','test'):
        for source in (hf/directory).rglob('*.ts'):
            copy(source,out/'server'/source.relative_to(hf))
    for name in ('package.json','pnpm-lock.yaml','pnpm-workspace.yaml','tsconfig.json','eslint.config.mjs'):
        copy(hf/name,out/'server'/name)
    # package.json repository is provenance, not a destination for user data.
    for name in ('service.py','requirements.txt'):
        copy(hf/'manager'/name,out/'manager'/name)
    copy(hf/'manager/ha-ingress-proxy/proxy.py',out/'manager/proxy.py')
    proxy=out/'manager/proxy.py'
    proxy.write_text(re.sub(r"UPSTREAM_HOST = '[^']+'", "UPSTREAM_HOST = '127.0.0.1'", proxy.read_text(encoding='utf-8')),encoding='utf-8',newline='\n')
    for name,raw in owned.items():
        (out/name).parent.mkdir(parents=True,exist_ok=True)
        (out/name).write_bytes(raw)
    # Gateway imports only allowed_path; never starts the legacy fixed transport.
    kit=WORK/'alice-gemini-bridge'
    for name in ('start.sh','supervise.sh','rollback-bind.sh','S98quasarbridge','recovery.sh','install-device.sh'):
        copy(kit/'packaging'/name,out/'packaging'/name)
    config=json.loads((kit/'config.example.json').read_text(encoding='utf-8-sig'))
    # Read-only source template; no deployed runtime, provider key or personal rules.
    config['conversation_log']={'enabled':False}
    config['routing']['rules'].insert(0,{'name':'home_lighting','route':'home','match':{'regex':r'^\s*(?:алиса[,\s]+)?(?:включи|выключи|погаси|зажги)\s+(?:свет|освещение)(?:\s+(?:(?:в|на)\s+[а-яёa-z0-9 -]+|здесь|тут))?\s*[.!]?\s*$'}})
    # The app supplies endpoints and secrets at runtime, not from a personal template.
    (out/'bridge-config.json').write_text(json.dumps(config,ensure_ascii=False,indent=2)+'\n',encoding='utf-8',newline='\n')
    for source in (ROOT/'wiki').glob('*.md'):
        copy(source,out/'wiki'/source.name)
    copy(ROOT/'wiki/Installation.md',out/'DOCS.md')
    copy(ROOT/'CHANGELOG.md',out/'CHANGELOG.md')
    copy(kit/'LICENSE',out/'LICENSE')
    copy(ROOT/'NOTICE',out/'NOTICE')
    if (ROOT/'BRIDGE-SOURCE.json').exists():copy(ROOT/'BRIDGE-SOURCE.json',out/'BRIDGE-SOURCE.json')
    sources=[p for p in out.rglob('*') if p.is_file() and p.name!='source-manifest.json' and '__pycache__' not in p.parts]
    # App-owned files are edited on Windows too. Hash the LF bytes Git ships,
    # not local CRLF bytes that .gitattributes will normalize during commit.
    for source in sources:
        raw=source.read_bytes()
        if b'\r\n' in raw:source.write_bytes(raw.replace(b'\r\n',b'\n'))
    manifest={p.relative_to(out).as_posix():hashlib.sha256(p.read_bytes()).hexdigest() for p in sources}
    (out/'source-manifest.json').write_text(json.dumps(manifest,indent=2)+'\n',encoding='utf-8')
    print('Prepared source context:',len(manifest),'files; no device state or credentials copied.')


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('--bridge-source',required=True,type=Path)
    args=parser.parse_args()
    prepare(args.bridge_source)
