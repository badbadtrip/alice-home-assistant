import base64
import hashlib
import io
import json
from pathlib import Path
import tempfile
import threading
import types
import sys
import unittest
from unittest.mock import patch
from http.server import ThreadingHTTPServer
from urllib.request import Request, urlopen
from urllib.error import HTTPError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]/'alice/manager'))
from service import Error, Handler, Manager, Store, STARTUP_FILES, authenticate, atomic_json, lan_ip


class ManagerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.manager = Manager({'state_dir': str(self.root / 'state'), 'ssh_key': '/private/key',
                                'artifact_dir': str(self.root / 'artifacts'), 'packaging_dir': str(self.root / 'packaging')})
        self.speaker = self.manager.store.save_speaker({'name': 'Кухня', 'area': 'Кухня', 'host': '192.168.1.99'})

    def tearDown(self):
        self.temp.cleanup()

    def rollout(self, actual='old'):
        import updater
        updater._next_run.clear()
        ssh = lambda speaker, command, *a, **k: (actual + ' x').encode() if 'sha256sum' in command else b'{"status":"ok","speaker_id":"%s","client_frames":2}' % speaker['id'].encode()
        with patch.object(updater, 'target', return_value='new'), \
             patch.object(updater, 'notify'), patch.object(self.manager, 'ssh', side_effect=ssh), \
             patch.object(self.manager, 'job', return_value={'job_id': 'j'}) as job:
            updater.cycle(self.manager)
        return job, self.manager.store.speaker(self.speaker['id']).get('bridge_update')

    def ready(self):
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='SHA256:test', install={'state': 'ready'})
        self.manager.connections[self.speaker['id']] = {'status': 'online'}

    def test_auto_update_starts_at_once_and_skips_current_bridge(self):
        self.ready()
        job, update = self.rollout(actual='new')
        job.assert_not_called(); self.assertEqual(update['state'], 'current')
        self.manager.store.metadata(self.speaker['id'], bridge_update=None)
        job, update = self.rollout()
        job.assert_called_once_with(self.speaker['id'], 'install'); self.assertEqual(update['state'], 'updating')

    def test_auto_update_verifies_then_asks_for_voice(self):
        import updater
        self.ready()
        self.rollout()
        self.manager.jobs['j'] = {'status': 'succeeded'}
        self.assertEqual(self.rollout()[1]['state'], 'verifying')
        self.assertEqual(self.rollout()[1]['state'], 'awaiting_voice')
        job, _ = self.rollout(); job.assert_not_called()
        self.assertEqual(updater.confirm(self.manager, self.speaker['id']), {'state': 'current'})

    def test_auto_update_failure_stops_rollout_and_switch_disables_it(self):
        import updater
        self.ready()
        self.rollout()
        self.manager.jobs['j'] = {'status': 'failed'}
        self.assertEqual(self.rollout()[1]['state'], 'failed')
        job, _ = self.rollout(); job.assert_not_called()
        self.manager.store.metadata(self.speaker['id'], bridge_update=None)
        self.assertTrue(updater.enabled(self.manager))
        updater.set_enabled(self.manager, False)
        job, _ = self.rollout(); job.assert_not_called()

    def airplay_cycle(self, lost):
        self.manager.airplay_next_check = self.manager.airplay_next_try = 0.0
        with patch.object(self.manager, '_airplay_lost', return_value=lost), patch.object(self.manager, 'ssh') as ssh:
            self.manager.airplay_cycle()
        return ssh.call_count

    def test_airplay_reannounce_waits_retries_and_gives_up(self):
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='SHA256:test')
        self.assertEqual(self.airplay_cycle(True), 0)
        self.assertEqual([self.airplay_cycle(True) for _ in range(4)], [1, 1, 1, 0])
        self.assertEqual(self.airplay_cycle(False), 0)
        self.assertEqual(self.manager.airplay_tries, 0)

    def test_airplay_reannounce_skips_untrusted_speaker(self):
        self.assertEqual([self.airplay_cycle(True) for _ in range(2)], [0, 0])

    def test_lan_address_rejects_public_loopback_hostname_and_shell(self):
        for value in ('127.0.0.1', '0.0.0.0', '8.8.8.8', 'alice-lite2', '192.168.1.1;reboot', '169.254.1.1', '::1'):
            with self.subTest(value=value), self.assertRaises(Error):
                lan_ip(value)
        self.assertEqual(lan_ip('192.168.1.99'), '192.168.1.99')

    def test_assignment_per_target_is_durable_and_null_means_all(self):
        sid = self.speaker['id']
        store = self.manager.store
        store.assign('script.first', [sid, sid])
        store.assign('automation.second', [])
        self.assertEqual(Store(store.directory).read('assignments.json'), {'script.first': [sid], 'automation.second': []})
        store.assign('script.first', None)
        self.assertEqual(store.read('assignments.json'), {'automation.second': []})
        with self.assertRaises(Error):
            store.assign('script.first', ['unknown'])
        with self.assertRaises(Error):
            store.assign('../../path', [])

    def test_deleting_speaker_does_not_turn_explicit_target_into_all(self):
        self.manager.store.assign('script.test', [self.speaker['id']])
        self.manager.store.delete(self.speaker['id'])
        self.assertEqual(self.manager.store.read('assignments.json'), {'script.test': []})

    def test_metadata_fields_cannot_be_injected_and_address_change_resets_trust(self):
        with self.assertRaises(Error):
            self.manager.store.save_speaker({'trusted_fingerprint': 'forged'}, self.speaker['id'])
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='old')
        changed = self.manager.store.save_speaker({'host': '192.168.1.98'}, self.speaker['id'])
        self.assertNotIn('trusted_fingerprint', changed)

    def test_fingerprint_needs_matching_nonce_and_never_replaces_changed_key(self):
        key = base64.b64encode(b'test public key').decode()
        line = '[192.168.1.99]:2222 ssh-ed25519 ' + key + '\n'
        with patch('service.run', return_value=line.encode()):
            value = self.manager.fingerprints(self.speaker['id'])
        with self.assertRaises(Error):
            self.manager.trust(self.speaker['id'], {**value, 'nonce': 'bad'})
        trusted = self.manager.trust(self.speaker['id'], value)
        self.assertEqual(trusted['trusted_fingerprint'], value['fingerprint'])
        with patch('service.run', return_value=line.replace(key, base64.b64encode(b'changed').decode()).encode()):
            changed = self.manager.fingerprints(self.speaker['id'])
        with self.assertRaises(Error):
            self.manager.trust(self.speaker['id'], changed)

    def test_ssh_pins_key_knownhosts_and_root_without_config_inheritance(self):
        with patch('service.run', return_value=b'ok') as mocked:
            self.manager.ssh(self.speaker, 'id -u')
        args = mocked.call_args.args[0]
        self.assertIn('StrictHostKeyChecking=yes', args)
        self.assertIn('IdentitiesOnly=yes', args)
        self.assertIn('/dev/null', args)
        self.assertEqual(args[-2:], ['root@192.168.1.99', 'id -u'])

    def test_existing_bridge_refuses_unknown_startup(self):
        with patch.object(self.manager, '_check', return_value={'bridge_present': True}), self.assertRaises(Error) as error:
            self.manager._install(self.speaker, {'id': 'a' * 16})
        self.assertIn('Переход со старого моста', str(error.exception))

    def test_fresh_package_preserves_original_builds_proxy_and_speaker_identity(self):
        artifact, packaging = self.root / 'artifacts', self.root / 'packaging'
        artifact.mkdir()
        packaging.mkdir()
        binary = bytearray(1_000_100)
        binary[:7] = b'\x7fELF\x02\x01\x01'
        binary[16:18] = b'\x02\x00'
        binary[18:20] = b'\xb7\x00'
        binary[20:24] = b'\x01\x00\x00\x00'
        (artifact / 'quasar-bridge').write_bytes(binary)
        (artifact / 'config.json').write_text('{}')
        (artifact / 'runtime.env').write_text("QUASAR_HOME_AREA='old'\nQUASAR_HOME_SPEAKER_ID='old'\nSECRET='kept'\n")
        for name in ('LICENSE', 'NOTICE', 'THIRD_PARTY_NOTICES'):
            (artifact / name).write_text(name)
        for name in ('start.sh', 'supervise.sh', 'rollback-bind.sh', 'S98quasarbridge', 'recovery.sh', 'install-device.sh'):
            (packaging / name).write_text('#!/bin/sh\n/etc/init.d/S99quasar /etc/init.d/S98quasarbridge\n')
        original = b'{"aliced":{"uniProxyUrl":"wss://stock","uniProxyQuicUrl":"quic://stock"}}'
        self.speaker['profile'] = 'midi'
        archive, manifest, installer = self.manager._package(self.speaker, original)
        import tarfile
        with tarfile.open(fileobj=io.BytesIO(archive)) as tar:
            self.assertEqual(tar.extractfile('original.cfg').read(), original)
            env = tar.extractfile('runtime.env').read().decode()
            self.assertIn('QUASAR_HOME_SPEAKER_ID=' + self.speaker['id'], env)
            self.assertNotIn("SPEAKER_ID='old'", env)
            self.assertIn("SECRET='kept'", env)
            candidate = json.load(tar.extractfile('candidate.cfg'))
            self.assertEqual(candidate['aliced']['uniProxyUrl'], 'ws://127.0.0.1:18861/uni.ws')
            self.assertEqual(candidate['aliced']['uniProxyQuicUrl'], '')
        self.assertIn(b'/etc/init.d/S97quasar', installer)
        self.assertNotIn('runtime.env', manifest)

    def test_confirm_requires_explicit_voice_test(self):
        handler = object.__new__(Handler)
        handler.command = 'POST'
        with self.assertRaises(Error):
            handler.route(self.manager, ['api', 'speakers', self.speaker['id'], 'confirm'], {})

    def test_http_requires_auth_rejects_foreign_origin_and_emits_exact_cors(self):
        self.manager.config.update(ha_origin='http://ha.local:8123', ha_url='http://ha.local:8123')
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        server.manager = self.manager
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        url = 'http://127.0.0.1:%d/api/speakers' % server.server_port
        try:
            with self.assertRaises(HTTPError) as missing:
                urlopen(url)
            self.assertEqual(missing.exception.code, 401)
            missing.exception.close()
            with patch('service.authenticate') as auth, patch.object(self.manager, 'setup', return_value={'public_key': 'ssh-ed25519 test'}):
                with self.assertRaises(HTTPError) as foreign:
                    urlopen(Request(url, headers={'Origin': 'http://evil.local', 'Authorization': 'Bearer secret'}))
                self.assertEqual(foreign.exception.code, 403)
                foreign.exception.close()
                auth.assert_not_called()
                with urlopen(Request(url, headers={'Origin': 'http://ha.local:8123', 'Authorization': 'Bearer secret'})) as response:
                    self.assertEqual(response.headers['Access-Control-Allow-Origin'], 'http://ha.local:8123')
                    self.assertEqual(response.headers['Cache-Control'], 'no-store')
                    self.assertEqual(len(json.load(response)['speakers']), 1)
                auth.assert_called_once_with('http://ha.local:8123', 'secret')
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)

    def test_existing_upgrade_failed_health_restores_and_restarts_backup(self):
        artifact = self.root / 'artifacts'
        artifact.mkdir()
        expected = {'start.sh': 'a' * 64}
        (artifact / 'existing-startup-sha256.json').write_text(json.dumps(expected))
        original_config = b'{}'
        manifest = {'bin/quasar-bridge': 'b' * 64, 'config.json': hashlib.sha256(original_config).hexdigest()}
        archive = io.BytesIO()
        import tarfile
        with tarfile.open(fileobj=archive, mode='w') as tar:
            info = tarfile.TarInfo('bin/quasar-bridge')
            info.size = 3
            tar.addfile(info, io.BytesIO(b'new'))
        commands = []

        def ssh(_speaker, command, *args, **kwargs):
            commands.append(command)
            if command.startswith('wc -c'):
                return b'100\n'
            if command == 'df -Pk /data':
                return b'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/ubi1_0 100000 10000 90000 10% /bkupgrade\n'
            if 'sha256sum start.sh' in command:
                return ('a' * 64 + '  start.sh\n').encode()
            if command == 'cat /data/opt/quasar-bridge/manifest.json':
                return json.dumps(manifest).encode()
            if command == 'cat /data/opt/quasar-bridge/config.json':
                return original_config
            if command.startswith('sha256sum '):
                if '.manager-' in command:
                    return (hashlib.sha256(b'new').hexdigest() + ' candidate').encode()
                return ('b' * 64 + ' bin/quasar-bridge').encode()
            if command.endswith('/runtime.env'):
                return b"SECRET='keep'\n"
            if 'healthz' in command:
                return b'{"status":"error"}'
            return b'{}'

        with patch.object(self.manager, 'ssh', side_effect=ssh), patch.object(self.manager, '_package', return_value=(archive.getvalue(), {}, b'')), patch.object(self.manager, '_restart_process') as restart:
            with self.assertRaises(Error) as failed:
                self.manager._upgrade(self.speaker, {'id': 'a' * 16})
        self.assertIn('восстановлены', str(failed.exception))
        self.assertEqual(restart.call_count, 2)
        self.assertTrue(any('/quasar-bridge.rollback' in command for command in commands))
        backup_command = next(command for command in commands if ' && mkdir ' in command)
        self.assertIn(' && ln /data/opt/quasar-bridge/bin/quasar-bridge ', backup_command)
        self.assertNotIn('cp -p /data/opt/quasar-bridge/bin/quasar-bridge', backup_command)

    def test_full_filesystem_is_detected_before_staging(self):
        with patch.object(self.manager, 'ssh', return_value=b'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/ubi1_0 89340 89332 0 100% /bkupgrade\n'):
            with self.assertRaises(Error) as full:
                self.manager._space(self.speaker, 50 * 1024 * 1024)
        self.assertIn('Недостаточно места', str(full.exception))
        self.assertIn('свободно 0 МБ', str(full.exception))

    def test_prune_keeps_only_this_jobs_backup_and_ignores_ssh_errors(self):
        commands = []
        fake = types.SimpleNamespace(ssh=lambda _speaker, command: commands.append(command))
        Manager._prune_backups(fake, {}, 'abc')
        self.assertIn('[ "$d" = manager-backup-abc ] || rm -rf', commands[0])
        def fail(_speaker, _command):
            raise Error('offline', 502)
        Manager._prune_backups(types.SimpleNamespace(ssh=fail), {}, 'abc')

    def test_hardlink_failure_refuses_upgrade_before_candidate_upload(self):
        artifact = self.root / 'artifacts'
        artifact.mkdir()
        (artifact / 'existing-startup-sha256.json').write_text(json.dumps({'start.sh': 'a' * 64}))
        manifest = {'bin/quasar-bridge': 'b' * 64, 'config.json': hashlib.sha256(b'{}').hexdigest()}
        archive = io.BytesIO()
        import tarfile
        with tarfile.open(fileobj=archive, mode='w') as tar:
            info = tarfile.TarInfo('bin/quasar-bridge')
            info.size = 3
            tar.addfile(info, io.BytesIO(b'new'))
        responses = [b'a' * 64 + b' start.sh', b'', json.dumps(manifest).encode(), b'{}',
                     b'b' * 64 + b' bin/quasar-bridge', b'{}', b"SECRET='keep'\n",
                     b'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/ubi1_0 100000 10000 90000 10% /bkupgrade\n',
                     Error('Hard links unavailable', 502)]
        with patch.object(self.manager, 'ssh', side_effect=responses) as ssh, patch.object(self.manager, '_package', return_value=(archive.getvalue(), {}, b'')), patch.object(self.manager, '_restart_process') as restart:
            with self.assertRaises(Error):
                self.manager._upgrade(self.speaker, {'id': 'a' * 16})
        commands = [call.args[1] for call in ssh.call_args_list]
        self.assertFalse(any('cat >' in command for command in commands))
        self.assertIn(' && ln ', commands[-1])
        restart.assert_not_called()

    def test_authentication_rejects_valid_nonadmin_and_sends_current_user_command(self):
        sent = []

        class Socket:
            def __init__(self, admin):
                self.responses = iter([{'type': 'auth_required'}, {'type': 'auth_ok'},
                                       {'id': 1, 'success': True, 'result': {'is_admin': admin}}])

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                pass

            def recv(self, **_kwargs):
                return json.dumps(next(self.responses))

            def send(self, value):
                sent.append(json.loads(value))

        module = types.ModuleType('websockets.sync.client')
        module.connect = lambda *_args, **_kwargs: Socket(False)
        with patch.dict(sys.modules, {'websockets.sync.client': module}):
            with self.assertRaises(Error) as forbidden:
                authenticate('http://ha.local:8123', 'secret')
            self.assertEqual(forbidden.exception.status, 403)
            self.assertEqual(sent[-1], {'id': 1, 'type': 'auth/current_user'})
            module.connect = lambda *_args, **_kwargs: Socket(True)
            authenticate('http://ha.local:8123', 'secret')

    def test_setup_exposes_only_public_key_and_profile_specific_instructions(self):
        public = self.root / 'manager.pub'
        public.write_text('ssh-ed25519 ' + base64.b64encode(b'public only').decode() + ' manager\n')
        self.manager.config['ssh_public_key'] = str(public)
        value = self.manager.setup()
        self.assertTrue(value['public_key'].startswith('ssh-ed25519 '))
        self.assertTrue(value['installation_ready'])
        self.manager.config['installation_ready'] = False
        self.manager._setup_cache = None
        self.assertFalse(self.manager.setup()['installation_ready'])
        self.assertEqual(value['profiles']['midi']['authorized_keys_path'], '/root/.ssh/authorized_keys')
        self.assertEqual(value['profiles']['default']['authorized_keys_path'], '/data/opt/etc/dropbear/authorized_keys')
        self.assertNotIn('ssh_key', value)
        public.write_text('-----BEGIN OPENSSH PRIVATE KEY-----')
        self.manager._setup_cache = None
        with self.assertRaises(Error):
            self.manager.setup()

    def test_manager_fresh_install_uses_its_durable_receipt_across_later_upgrades(self):
        job_id = 'c' * 16
        expected = {name: hashlib.sha256(name.encode()).hexdigest() for name in STARTUP_FILES}
        receipt_path = self.manager.store.directory / 'backups' / self.speaker['id'] / job_id / 'receipt.json'
        atomic_json(receipt_path, {'speaker_id': self.speaker['id'], 'job_id': job_id, 'manifest': expected})
        self.manager.store.metadata(self.speaker['id'], install={'job_id': job_id, 'kind': 'fresh', 'state': 'ready', 'startup_receipt_job_id': job_id})
        self.assertEqual(self.manager._startup_ownership(self.speaker), (expected, job_id))
        # A later upgrade changes its own job/backup, while startup ownership stays
        # anchored to the original locally trusted provisioning receipt.
        self.manager.store.metadata(self.speaker['id'], install={'job_id': 'd' * 16, 'kind': 'upgrade', 'state': 'ready', 'startup_receipt_job_id': job_id})
        self.assertEqual(self.manager._startup_ownership(self.speaker), (expected, job_id))
        atomic_json(receipt_path, {'speaker_id': 'other', 'job_id': job_id, 'manifest': expected})
        with self.assertRaises(Error):
            self.manager._startup_ownership(self.speaker)
        atomic_json(receipt_path, {'speaker_id': self.speaker['id'], 'job_id': 'wrong', 'manifest': expected})
        with self.assertRaises(Error):
            self.manager._startup_ownership(self.speaker)
        self.manager.store.metadata(self.speaker['id'], install={'startup_receipt_job_id': '../../outside'})
        with self.assertRaises(Error):
            self.manager._startup_ownership(self.speaker)

    def test_legacy_upgrade_keeps_server_pinned_profile_policy(self):
        artifact = self.root / 'artifacts'
        artifact.mkdir()
        expected = {'start.sh': 'a' * 64}
        atomic_json(artifact / 'existing-startup-sha256.json', {'default': expected, 'midi': {'start.sh': 'b' * 64}})
        self.manager.store.metadata(self.speaker['id'], install={'kind': 'upgrade', 'job_id': 'a' * 16})
        self.assertEqual(self.manager._startup_ownership(self.speaker), (expected, None))

    def test_background_health_online_then_failure_preserves_last_seen(self):
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='SHA256:trusted')
        health = {'status': 'ok', 'speaker_id': self.speaker['id'], 'voice_phrase_count': 2, 'voice_phrase_revision': 'a' * 64}
        with patch.object(self.manager, 'ssh', return_value=json.dumps(health).encode()) as ssh, patch('service.time.time', return_value=1000):
            self.manager.monitor_cycle()
            online = self.manager.speakers()[0]['connection']
        self.assertEqual(online['status'], 'online')
        self.assertEqual(online['last_seen'], 1000)
        self.assertEqual(online['voice_phrase_count'], 2)
        self.assertEqual(online['voice_phrase_revision'], 'a' * 64)
        self.assertEqual(ssh.call_args.args[1], 'wget -T 3 -q -O - http://127.0.0.1:18861/healthz')
        with patch.object(self.manager, 'ssh', side_effect=Error('Secret SSH stderr must not leak', 502)), patch('service.time.time', return_value=1015):
            self.manager.monitor_cycle()
            offline = self.manager.speakers()[0]['connection']
        self.assertEqual(offline['status'], 'offline')
        self.assertEqual(offline['last_seen'], 1000)
        self.assertEqual(offline['checked_at'], 1015)
        self.assertNotIn('Secret', offline['error'])

    def test_monitor_skips_untrusted_and_busy_install_then_stales_without_network_get(self):
        with patch.object(self.manager, 'ssh') as ssh:
            self.manager.monitor_cycle()
            ssh.assert_not_called()
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='SHA256:trusted')
        health = {'status': 'ok', 'speaker_id': self.speaker['id']}
        with patch.object(self.manager, 'ssh', return_value=json.dumps(health).encode()), patch('service.time.time', return_value=1000):
            self.manager.monitor_cycle()
        self.manager.operation_lock.acquire()
        try:
            with patch.object(self.manager, 'ssh') as ssh, patch('service.time.time', return_value=1046):
                self.manager.monitor_cycle()
                stale = self.manager.speakers()[0]['connection']
                ssh.assert_not_called()
        finally:
            self.manager.operation_lock.release()
        self.assertEqual(stale['status'], 'unknown')
        self.assertEqual(stale['last_seen'], 1000)

    def test_monitor_rejects_wrong_speaker_identity_and_changed_address_resets_observation(self):
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='SHA256:trusted')
        with patch.object(self.manager, 'ssh', return_value=b'{"status":"ok","speaker_id":"different","voice_phrase_count":2}'):
            self.manager.monitor_cycle()
        wrong = self.manager.speakers()[0]['connection']
        self.assertEqual(wrong['status'], 'offline')
        self.assertIn('Идентификатор', wrong['error'])
        self.assertIsNone(wrong['last_seen'])
        self.manager.store.save_speaker({'host': '192.168.1.98'}, self.speaker['id'])
        with patch.object(self.manager, 'ssh') as ssh:
            changed = self.manager.speakers()[0]['connection']
            ssh.assert_not_called()
        self.assertEqual(changed['status'], 'unknown')
        self.assertIsNone(changed['checked_at'])

    def test_delivery_requires_fresh_matching_actual_and_expected_revisions(self):
        with patch.object(self.manager, '_hf_request', return_value={'revision': 'abc'}):
            self.assertEqual(self.manager._delivery(self.speaker, {'status': 'online', 'voice_phrase_revision': 'abc'})['status'], 'synced')
            self.assertEqual(self.manager._delivery(self.speaker, {'status': 'online', 'voice_phrase_revision': 'old'})['status'], 'pending')
            offline = self.manager._delivery(self.speaker, {'status': 'offline', 'voice_phrase_revision': 'abc'})
            self.assertEqual(offline['status'], 'unknown')
            self.assertIsNone(offline['actual_revision'])
        with patch.object(self.manager, '_hf_request', side_effect=Error('Unavailable', 502)):
            self.assertEqual(self.manager._delivery(self.speaker, {'status': 'online', 'voice_phrase_revision': 'abc'})['status'], 'error')

    def test_delivery_persistent_mismatch_escalates_and_resets_on_revision_or_offline(self):
        state = {'status': 'online', 'voice_phrase_revision': 'old'}
        with patch.object(self.manager, '_hf_request', return_value={'revision': 'new'}), patch('service.time.monotonic', return_value=100):
            self.assertEqual(self.manager._delivery(self.speaker, state)['status'], 'pending')
        with patch.object(self.manager, '_hf_request', return_value={'revision': 'new'}), patch('service.time.monotonic', return_value=221):
            delayed = self.manager._delivery(self.speaker, state)
            self.assertEqual(delayed['status'], 'error')
            self.assertIn('более двух минут', delayed['message'])
        with patch.object(self.manager, '_hf_request', return_value={'revision': 'newer'}), patch('service.time.monotonic', return_value=222):
            self.assertEqual(self.manager._delivery(self.speaker, state)['status'], 'pending')
        with patch.object(self.manager, '_hf_request', return_value={'revision': 'newer'}), patch('service.time.monotonic', return_value=400):
            self.assertEqual(self.manager._delivery(self.speaker, {'status': 'offline', 'voice_phrase_revision': 'old'})['status'], 'unknown')
            self.assertNotIn(self.speaker['id'], self.manager.pending_delivery)
        with patch.object(self.manager, '_hf_request', return_value={'revision': 'newer'}), patch('service.time.monotonic', return_value=401):
            self.assertEqual(self.manager._delivery(self.speaker, state)['status'], 'pending')
            self.assertEqual(self.manager._delivery(self.speaker, {'status': 'online', 'voice_phrase_revision': 'newer'})['status'], 'synced')
            self.assertNotIn(self.speaker['id'], self.manager.pending_delivery)

    def test_phrase_preview_is_readonly_scoped_per_speaker_and_error_isolated(self):
        second = self.manager.store.save_speaker({'name': 'MIDI', 'area': 'Комната', 'host': '192.168.1.59'})
        with patch.object(self.manager, '_hf_request', side_effect=[{'matched': True, 'target': 'script.test'}, Error('Home Functions unavailable', 502)]) as hf:
            result = self.manager.phrase_check({'text': 'Алиса, свет', 'speakers': [self.speaker['id'], second['id']]})
        self.assertEqual(len(result['results']), 2)
        self.assertTrue(result['results'][0]['matched'])
        self.assertIn('error', result['results'][1])
        self.assertEqual(hf.call_args_list[0].args, ('/preview', self.speaker, {'text': 'Алиса, свет', 'area': 'Кухня'}))
        self.assertTrue(all(call.args[0] == '/preview' for call in hf.call_args_list))
        with self.assertRaises(Error):
            self.manager.phrase_check({'text': 'text', 'speakers': ['unknown']})

    def test_configure_updates_metadata_only_after_verified_runtime_and_preserves_media(self):
        self.manager.store.metadata(self.speaker['id'], media_entity='media_player.kitchen')
        original = b"SECRET='unchanged'\nQUASAR_HOME_AREA='old'\n"
        with patch.object(self.manager, 'ssh', return_value=original) as ssh, patch.object(self.manager, '_restart_process'), patch.object(self.manager, '_verified_health') as health, patch.object(self.manager, '_monitor_speaker'):
            self.manager._configure(self.speaker, {'id': 'a' * 16}, {'area': 'Гостиная', 'name': 'Новая Алиса'})
        health.assert_called_once_with(self.speaker, 'Гостиная')
        item = self.manager.store.speaker(self.speaker['id'])
        self.assertEqual(item['area'], 'Гостиная')
        self.assertEqual(item['media_entity'], 'media_player.kitchen')
        uploaded = next(call.args[2] for call in ssh.call_args_list if 'cat >' in call.args[1])
        self.assertIn(b"SECRET='unchanged'", uploaded)
        self.assertNotIn(b"QUASAR_HOME_AREA='old'", uploaded)

    def test_edit_and_verified_configure_preserve_speaker_order(self):
        second = self.manager.store.save_speaker({'name': 'MIDI', 'area': 'Комната', 'host': '192.168.1.59'})
        expected = [self.speaker['id'], second['id']]
        self.manager.store.save_speaker({'name': 'Кухонная Алиса'}, self.speaker['id'])
        self.assertEqual([item['id'] for item in self.manager.store.read('speakers.json')], expected)
        with patch.object(self.manager, 'ssh', return_value=b"SECRET='keep'\n"), patch.object(self.manager, '_restart_process'), patch.object(self.manager, '_verified_health'), patch.object(self.manager, '_monitor_speaker'):
            self.manager._configure(self.speaker, {'id': 'a' * 16}, {'area': 'Кухня'})
        self.assertEqual([item['id'] for item in self.manager.store.read('speakers.json')], expected)

    def test_failed_configure_restores_runtime_and_keeps_registry_unchanged(self):
        with patch.object(self.manager, 'ssh', return_value=b"SECRET='keep'\n") as ssh, patch.object(self.manager, '_restart_process') as restart, patch.object(self.manager, '_verified_health', side_effect=[Error('Area mismatch', 502), {}]):
            with self.assertRaises(Error) as error:
                self.manager._configure(self.speaker, {'id': 'a' * 16}, {'area': 'Wrong room'})
        self.assertIn('восстановлена', str(error.exception))
        self.assertEqual(restart.call_count, 2)
        self.assertEqual(self.manager.store.speaker(self.speaker['id'])['area'], 'Кухня')
        self.assertTrue(any('cp -p /data/opt/quasar-bridge/runtime.env.backup-' in call.args[1] for call in ssh.call_args_list))

    def test_room_patch_is_rejected_and_configure_forbids_extra_commands(self):
        handler = object.__new__(Handler)
        handler.command = 'PATCH'
        with self.assertRaises(Error) as error:
            handler.route(self.manager, ['api', 'speakers', self.speaker['id']], {'area': 'Other'})
        self.assertEqual(error.exception.status, 409)
        self.manager.store.metadata(self.speaker['id'], trusted_fingerprint='SHA256:trusted')
        with self.assertRaises(Error):
            self.manager.job(self.speaker['id'], 'configure', {'area': 'Room', 'command': 'reboot'})
        with self.assertRaises(Error):
            self.manager.job(self.speaker['id'], 'configure', {'area': 'Room', 'media_entity': 'switch.light'})
        with self.assertRaises(Error):
            self.manager.store.save_speaker({'media_entity': 'media_player.foo;reboot'}, self.speaker['id'])

    def test_failed_ssh_check_clears_previous_success(self):
        self.manager.store.metadata(self.speaker['id'], last_check={'root': True})
        job={'id':'a'*16,'speaker_id':self.speaker['id'],'operation':'check','status':'running'}
        with patch.object(self.manager,'ssh',side_effect=Error('SSH недоступен')):
            self.manager._job(job,self.speaker)
        self.assertEqual(job['status'],'failed')
        self.assertFalse(self.manager.store.speaker(self.speaker['id'])['last_check']['root'])

    def test_restart_rejects_unrecognized_process_before_kill(self):
        with patch.object(self.manager, 'ssh', side_effect=[b'123', b'/bin/foreign\0']) as ssh:
            with self.assertRaises(Error):
                self.manager._restart(self.speaker, {'id': 'a' * 16})
        self.assertFalse(any(call.args[1].startswith('kill ') for call in ssh.call_args_list))

    def test_home_functions_transport_uses_private_server_token_and_speaker_scope(self):
        self.manager.config['home_functions_url'] = 'http://192.168.10.10:18080'
        env = self.root / 'private.env'
        env.write_text("HOME_FUNCTIONS_TOKEN='server-test-token'\n")
        self.manager.config['home_functions_env'] = str(env)
        original_stat = Path.stat

        def stat(path, *args, **kwargs):
            value = original_stat(path, *args, **kwargs)
            return types.SimpleNamespace(st_mode=0o100600) if path == env else value

        connection = unittest.mock.Mock()
        response = connection.getresponse.return_value
        response.status = 200
        response.read.return_value = b'{"matched":false}'
        with patch.object(Path, 'stat', stat), patch('service.http.client.HTTPConnection', return_value=connection):
            self.manager._hf_request('/preview', self.speaker, {'text': 'phrase', 'area': 'Kitchen'})
        call = connection.request.call_args
        self.assertEqual(call.args, ('POST', '/preview'))
        self.assertEqual(call.kwargs['headers']['X-Alice-Speaker'], self.speaker['id'])
        self.assertEqual(call.kwargs['headers']['Authorization'], 'Bearer server-test-token')
        self.assertEqual(json.loads(call.kwargs['body'])['area'], 'Kitchen')


if __name__ == '__main__':
    unittest.main()
