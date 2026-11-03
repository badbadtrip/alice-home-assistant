import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]/'alice/manager'))
from service import Manager, Error, STARTUP_FILES


class AdoptionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.artifact = self.root/'artifacts'
        self.artifact.mkdir()
        packaging = self.root/'packaging'
        packaging.mkdir()
        for name in STARTUP_FILES:
            (packaging/name).write_bytes(('trusted '+name).encode())
        self.manager = Manager({'state_dir':str(self.root/'state'), 'artifact_dir':str(self.artifact),
                                'packaging_dir':str(packaging), 'ssh_key':'/private/key', 'adoption_enabled':True,
                                'installation_ready':True, 'advertised_url':'http://192.168.10.10:18081'})
        self.speaker = self.manager.store.save_speaker({'name':'Station', 'area':'Kitchen', 'host':'192.168.10.20', 'media_entity':'media_player.station'})
        self.expected = self.manager._packaged_startup(self.speaker)
        self.old_runtime = "QUASAR_HOME_SPEAKER_ID=old-station\nQUASAR_HOME_AREA=Old\nQUASAR_HOME_FUNCTIONS_URL=http://192.168.10.10:18080\nQUASAR_HOME_FUNCTIONS_TOKEN="+'O'*48+"\nQUASAR_LLM_API_KEY='keep-private'\n"
        (self.artifact/'runtime.env').write_text('QUASAR_HOME_FUNCTIONS_TOKEN='+'N'*48+'\n')
        self.config = b'{}'
        self.manifest = {'bin/quasar-bridge':'b'*64, 'config.json':hashlib.sha256(self.config).hexdigest()}
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode='w') as archive:
            member = tarfile.TarInfo('bin/quasar-bridge')
            member.size = 3
            archive.addfile(member, io.BytesIO(b'new'))
        self.package = (buffer.getvalue(), {}, b'')
        self.calls = []
        self.mode = 'success'
        self.health_count = 0

    def ssh(self, speaker, command, data=None, **kwargs):
        self.calls.append((command, data))
        if command.startswith('cd ') and 'sha256sum start.sh' in command:
            observed = dict(self.expected)
            if self.mode == 'unknown':
                observed['start.sh'] = 'f'*64
            return ''.join(value+'  '+name+'\n' for name,value in observed.items()).encode()
        if command.endswith('/manifest.json') and command.startswith('cat '):
            return json.dumps(self.manifest).encode()
        if command == 'cat /data/opt/quasar-bridge/config.json':
            return self.config
        if command == 'cat /data/opt/quasar-bridge/runtime.env':
            return self.old_runtime.encode()
        if command.startswith('sha256sum '):
            return ((hashlib.sha256(b'new').hexdigest() if '.manager-' in command else 'b'*64)+'  binary').encode()
        if command == 'df -Pk /data':
            available = '0' if self.mode == 'full' else '90000'
            return ('Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/ubi 100000 10000 '+available+' 10% /data\n').encode()
        if command.endswith('-check-backends') and self.mode == 'backend':
            raise Error('Backend failed', 502)
        if 'healthz' in command:
            self.health_count += 1
            if self.mode in ('health', 'rollback-failed'):
                if self.health_count == 1 or self.mode == 'rollback-failed':
                    return b'{"status":"error"}'
                return b'{"status":"ok","speaker_id":"old-station"}'
            return json.dumps({'status':'ok', 'speaker_id':self.speaker['id'], 'speaker_area':'Kitchen'}).encode()
        return b'{}'

    def adopt(self):
        with patch.object(self.manager, '_check', return_value={'bridge_present':True}), patch.object(self.manager, 'ssh', side_effect=self.ssh), patch.object(self.manager, '_package', return_value=self.package), patch.object(self.manager, '_restart_process'):
            return self.manager._adopt(self.speaker, {'id':'a'*16})

    def test_success_preserves_registry_updates_backend_and_requires_voice(self):
        result = self.adopt()
        item = self.manager.store.speaker(self.speaker['id'])
        self.assertEqual(result['state'], 'awaiting_acceptance')
        self.assertEqual(item['media_entity'], 'media_player.station')
        self.assertEqual(item['name'], 'Station')
        self.assertEqual(item['area'], 'Kitchen')
        runtime = next(data.decode() for command,data in self.calls if 'cat > /data/opt/quasar-bridge/runtime.env.manager-' in command)
        self.assertIn('QUASAR_HOME_FUNCTIONS_URL=http://192.168.10.10:18081', runtime)
        self.assertIn('N'*48, runtime)
        self.assertNotIn('O'*48, runtime)
        self.assertIn('keep-private', runtime)
        self.assertNotIn('keep-private', json.dumps(result))
        self.assertNotIn('N'*48, json.dumps(item))
        self.assertEqual(self.manager._startup_ownership(item)[0], self.expected)
        backup = self.manager.store.directory/'backups'/item['id']/('a'*16)/'runtime.env'
        self.assertEqual(backup.read_text(), self.old_runtime)
        with patch.object(self.manager, '_verified_health') as health, patch.object(self.manager, 'ssh') as ssh:
            self.manager._confirm(item, {'id':'c'*16})
        health.assert_called_once()
        ssh.assert_not_called()
        self.assertEqual(self.manager.store.speaker(item['id'])['install']['state'], 'ready')

    def test_unknown_startup_cannot_write_or_create_trust(self):
        self.mode = 'unknown'
        with self.assertRaises(Error):
            self.adopt()
        self.assertFalse(any('cat >' in command or 'mkdir ' in command or command.startswith('mv ') for command,_ in self.calls))
        self.assertNotIn('install', self.manager.store.speaker(self.speaker['id']))
        self.assertFalse((self.manager.store.directory/'backups').exists())

    def test_full_disk_and_failed_backend_do_not_switch(self):
        for mode in ('full', 'backend'):
            with self.subTest(mode=mode):
                self.mode = mode
                self.calls.clear()
                # Each run gets its own private receipt directory.
                if mode == 'backend':
                    import shutil
                    shutil.rmtree(self.manager.store.directory/'backups')
                with self.assertRaises(Error):
                    self.adopt()
                self.assertFalse(any(command.startswith('mv ') for command,_ in self.calls))
                self.assertNotIn('install', self.manager.store.speaker(self.speaker['id']))

    def test_failed_health_restores_and_verifies_previous_identity(self):
        self.mode = 'health'
        with self.assertRaises(Error) as result:
            self.adopt()
        self.assertIn('восстановлены', str(result.exception))
        self.assertEqual(self.manager.store.speaker(self.speaker['id'])['install']['state'], 'rolled_back')
        self.assertTrue(any('/quasar-bridge.rollback' in command for command,_ in self.calls))
        self.assertEqual(self.health_count, 2)

    def test_unconfirmed_rollback_keeps_recovery_available(self):
        self.mode = 'rollback-failed'
        with self.assertRaises(Error) as result:
            self.adopt()
        self.assertIn('восстановление пока не подтверждено', str(result.exception))
        self.assertEqual(self.manager.store.speaker(self.speaker['id'])['install']['state'], 'adopting')

    def test_user_can_restore_previous_bridge_without_changing_media(self):
        self.adopt()
        item = self.manager.store.speaker(self.speaker['id'])
        self.mode, self.health_count = 'health', 1
        with patch.object(self.manager, 'ssh', side_effect=self.ssh), patch.object(self.manager, '_restart_process'):
            result = self.manager._rollback(item, {'id':'b'*16})
        self.assertEqual(result['state'], 'rolled_back')
        self.assertEqual(self.manager.store.speaker(item['id'])['media_entity'], 'media_player.station')

    def test_pending_voice_prevents_repeated_install_or_reconfigure(self):
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='SHA256:test', install={'state':'awaiting_acceptance'})
        for operation in ('adopt', 'install', 'configure', 'restart'):
            with self.subTest(operation=operation), self.assertRaises(Error):
                self.manager.job(self.speaker['id'], operation)

    def test_runtime_is_requoted_without_executing_values(self):
        value = self.manager._adoption_runtime(self.speaker, "QUASAR_HOME_SPEAKER_ID=old\nCUSTOM='$(do-not-execute)'\n")
        self.assertIn("CUSTOM='$(do-not-execute)'", value)
        with self.assertRaises(Error):
            self.manager._adoption_runtime(self.speaker, 'CUSTOM=one\nCUSTOM=two')

    def test_adoption_requires_explicit_capability_and_existing_bridge(self):
        self.manager.config['adoption_enabled'] = False
        with self.assertRaises(Error):
            self.manager._packaged_startup(self.speaker)
        with patch.object(self.manager, '_check', return_value={'bridge_present':False}), patch.object(self.manager, '_upgrade') as upgrade:
            with self.assertRaises(Error):
                self.manager._adopt(self.speaker, {'id':'a'*16})
        upgrade.assert_not_called()


if __name__ == '__main__':
    unittest.main()
