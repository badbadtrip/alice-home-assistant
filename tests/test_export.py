import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import types
import os
from contextlib import nullcontext

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT/'alice/app'))
from import_state import import_state

spec = importlib.util.spec_from_file_location('export_legacy', ROOT/'tools/export_legacy.py')
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)


class ExportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.old = self.root/'old'
        (self.old/'state').mkdir(parents=True)
        (self.old/'ssh').mkdir()
        for name, value in [('speakers.json', [{'id':'midi'}]), ('assignments.json', {})]:
            (self.old/'state'/name).write_text(json.dumps(value))
        (self.old/'state/known_hosts').write_text('synthetic-host-key')
        self.token = self.root/'token'
        self.token.write_text('A'*48)
        self.output = self.root/'import.tar'

    def key(self):
        subprocess.run(['ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(self.old/'ssh/id_ed25519')], check=True)

    def test_export_roundtrip_and_no_overwrite(self):
        self.key()
        (self.old/'state/history.tar').write_bytes(b'not imported')
        (self.old/'state/backups').mkdir()
        (self.old/'state/backups/receipt.json').write_text('{}')
        self.assertEqual(exporter.export_legacy(self.old, self.output, token_file=self.token), 1)
        with tarfile.open(self.output) as archive:
            self.assertNotIn('state/history.tar', archive.getnames())
            self.assertIn('state/backups/receipt.json', archive.getnames())
        original = self.output.read_bytes()
        with self.assertRaises(FileExistsError):
            exporter.export_legacy(self.old, self.output, token_file=self.token)
        self.assertEqual(self.output.read_bytes(), original)
        self.output.chmod(0o600)
        destination = self.root/'new'
        destination.mkdir()
        original_stat = Path.stat
        def private_stat(path, *args, **kwargs):
            value = original_stat(path, *args, **kwargs)
            # Windows chmod does not expose Unix mode 0600.
            return types.SimpleNamespace(st_mode=0o100600) if path == self.output else value
        # Staged files need Windows ACLs rather than chmod for OpenSSH. The
        # importer runs on Linux; exercise real key validation there.
        keycheck = patch('import_state.subprocess.run', return_value=types.SimpleNamespace(stdout=(self.old/'ssh/id_ed25519.pub').read_bytes())) if os.name == 'nt' else nullcontext()
        with patch.object(Path, 'stat', private_stat), keycheck:
            self.assertTrue(import_state(self.output, destination))
        self.assertEqual((destination/'functions.token').read_text().strip(), 'A'*48)
        self.assertEqual(json.loads((destination/'state/speakers.json').read_text()), [{'id':'midi'}])

    def test_invalid_token_writes_nothing(self):
        self.token.write_text('bad')
        with self.assertRaises(ValueError):
            exporter.export_legacy(self.old, self.output, token_file=self.token)
        self.assertFalse(self.output.exists())

    def test_env_is_parsed_without_execution(self):
        self.key()
        env = self.root/'legacy.env'
        env.write_text('HOME_FUNCTIONS_TOKEN="'+'B'*48+'" # comment\nOTHER=$(do-not-execute)\n')
        exporter.export_legacy(self.old, self.output, env_file=env)
        with tarfile.open(self.output) as archive:
            self.assertEqual(archive.extractfile('functions.token').read(), b'B'*48)


if __name__ == '__main__':
    unittest.main()
