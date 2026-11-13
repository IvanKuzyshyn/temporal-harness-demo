"""Fault doubles cover orchestration; real process tests cover PID/lock ownership.
Real Docker/model/browser persistence is a separate required public-command gate.
"""
import importlib.util
import http.server
import fcntl
import threading
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

MODULE = Path(__file__).resolve().parents[1] / 'lifecycle.py'


class LifecycleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not MODULE.exists():
            return
        spec = importlib.util.spec_from_file_location('lifecycle', MODULE)
        cls.m = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.m)

    def setUp(self):
        self.assertTrue(MODULE.exists(), 'owned lifecycle implementation is missing')
        self.tmp = tempfile.TemporaryDirectory(prefix='webchat-lifecycle-test-')
        self.addCleanup(self.tmp.cleanup)
        self.home = Path(self.tmp.name)

    def lifecycle(self, **faults):
        driver = FakeDriver(**faults)
        app = self.m.Lifecycle(self.home, driver)
        return app, driver

    def test_only_enabled_qwen_is_required_in_fresh_and_existing_config(self):
        qwen = {'name': 'qwen3.5:9b', 'digest': 'd63f830f28d515fefea546f213fca7a75e9dd5176ef51a6deabc952d1c99a911', 'details': {'format': 'safetensors', 'runner': 'mlx'}}
        for existing in (False, True):
            d = self.m.Driver(self.home)
            if existing:
                cfg = json.loads((self.home/'config.json').read_text())
                cfg['models'] = ['qwen3.5:9b', 'gemma4:e4b']
                self.m.atomic(self.home/'config.json', cfg)
                d = self.m.Driver(self.home)
            def http(url):
                return json.dumps({'models': [qwen]} if url.endswith('/api/tags') else {'version': '0.40.15'}).encode()
            def no_download(*args, **kwargs):
                self.fail('existing enabled model must not cause a download')
            d.run = no_download
            with patch.object(self.m, 'http', http):
                d.models(pull=False)
                d.models(pull=True)
                qwen['digest'] = 'wrong'
                with self.assertRaisesRegex(RuntimeError, 'digest'):
                    d.models()
                qwen['digest'] = 'd63f830f28d515fefea546f213fca7a75e9dd5176ef51a6deabc952d1c99a911'

    def test_build_receipt_uses_owned_tag_not_shared_alias_or_unroutable_iid(self):
        d = self.m.Driver(self.home)
        image_a, image_b = 'sha256:'+'a'*64, 'sha256:'+'b'*64
        calls, owned_tags = [], []
        def run(argv, **kwargs):
            calls.append(argv)
            if argv[:2] == ['docker', 'build']:
                if '-t' in argv: owned_tags.append(argv[argv.index('-t')+1])
                if '--iidfile' in argv:
                    # Docker 29/containerd can emit an unroutable config digest
                    # here, while image inspect returns a manifest content ID.
                    Path(argv[argv.index('--iidfile')+1]).write_text('sha256:'+'c'*64+'\n')
            # Simulate another home moving the SHARED alias after our build.
            if argv[:3] == ['docker', 'image', 'inspect']:
                return (image_a if argv[-1] in owned_tags and argv[-1] != 'webchat-attachment-parser:1' else image_b)+'\n'
            if argv[0] == 'pnpm' and 'build' in argv:
                out = Path(argv[2])/'.next'; out.mkdir(parents=True)
                (out/'BUILD_ID').write_text('test')
            return ''
        d.run = run
        with patch.object(self.m, 'stage_frontend', lambda target: target.mkdir()): d.build()
        built = json.loads((self.home/'build.json').read_text())
        self.assertEqual(built['parserImage'], image_a)
        docker_build = next(c for c in calls if c[:2] == ['docker', 'build'])
        self.assertIn('--iidfile', docker_build)
        self.assertEqual(len(owned_tags), 1)
        self.assertTrue(owned_tags[0].startswith('webchat-parser-build:'))
        self.assertNotIn('webchat-attachment-parser:1', owned_tags)
        self.assertEqual([c[-1] for c in calls if c[:3] == ['docker', 'image', 'inspect']], owned_tags)

    def test_both_consumer_launches_receive_receipt_identity_not_inherited_env(self):
        d = self.m.Driver(self.home)
        image_a = 'sha256:'+'a'*64
        d.env['WEBCHAT_PARSER_IMAGE_ID'] = 'sha256:'+'b'*64
        self.m.atomic(self.home/'build.json', {'bin':str(self.home), 'web':str(self.home), 'parserImage':image_a})
        d.active = lambda name: True
        d.receipt = lambda name: {'exited':True, 'child':{'test':True}}
        with open(self.home/'test-lock','w') as lock:
            d.lock = lock
            for consumer in ('api', 'worker'):
                with self.subTest(consumer=consumer), patch.object(self.m.subprocess, 'Popen') as popen:
                    d.start(consumer)
                    self.assertEqual(popen.call_args.kwargs['env']['WEBCHAT_PARSER_IMAGE_ID'], image_a)

    def test_preflight_executes_immutable_image_even_after_alias_moves(self):
        d = self.m.Driver(self.home)
        image_a = 'sha256:'+'a'*64
        self.m.atomic(self.home/'build.json', {'parserImage':image_a})
        d.active = lambda name: True
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            if argv[:3] == ['docker', 'image', 'inspect']: return image_a+'\n'
            return ''
        d.run = run
        d.preflight('up')
        self.assertFalse(any('webchat-attachment-parser:1' in c for c in calls))
        probe = next(c for c in calls if c[:2] == ['docker', 'run'])
        self.assertIn(image_a, probe)
        self.assertIn('--pull=never', probe)

    def test_repeated_setup_up_down_preserves_data_and_shared_ollama(self):
        app, d = self.lifecycle()
        sentinel = self.home / 'attachment-original'
        sentinel.write_bytes(b'persist')
        for _ in range(2): app.execute('setup')
        self.assertEqual(d.running, set())
        app.execute('up'); app.execute('up')
        self.assertEqual(d.starts.count('api'), 1)
        app.execute('down'); app.execute('down')
        self.assertEqual(d.running, set())
        self.assertEqual(sentinel.read_bytes(), b'persist')
        self.assertNotIn('ollama', d.stops)
        app.execute('up')
        self.assertTrue(d.ready)

    def test_missing_docker_or_model_or_port_conflict_starts_nothing(self):
        for fault in ('docker', 'model', 'port'):
            with self.subTest(fault=fault):
                app, d = self.lifecycle(preflight_fault=fault)
                with self.assertRaisesRegex(RuntimeError, fault): app.execute('up')
                self.assertEqual(d.starts, [])

    def test_failed_migration_or_startup_rolls_back_only_new_resources(self):
        for fault in ('migrate', 'worker', 'ready'):
            with self.subTest(fault=fault):
                app, d = self.lifecycle(fail=fault)
                d.running.add('postgres')  # previously owned; not this invocation
                with self.assertRaisesRegex(RuntimeError, fault): app.execute('up')
                self.assertEqual(d.running, {'postgres'})
                self.assertNotIn('postgres', d.stops)
                self.assertFalse((self.home / 'transaction.json').exists())

    def test_setup_temporary_ollama_is_stopped_even_when_build_fails(self):
        app, d = self.lifecycle(shared=False, fail='build')
        with self.assertRaisesRegex(RuntimeError, 'build'): app.execute('setup')
        self.assertEqual(d.running, set())
        self.assertIn('ollama', d.stops)

    def test_controller_death_journal_recovers_only_new_resources(self):
        app, d = self.lifecycle()
        d.running.update({'postgres', 'api', 'worker'})
        (self.home / 'transaction.json').write_text(json.dumps({'before': ['postgres'], 'planned': ['api', 'worker']}))
        app.execute('down')
        self.assertEqual(d.running, set())
        self.assertEqual(d.stops[:2], ['api', 'worker'])

    def test_down_closes_api_streams_before_next_waits_for_requests(self):
        app, d = self.lifecycle()
        d.running.update({'web', 'api', 'worker'})
        original_stop = d.stop
        def stop(name):
            if name == 'web' and 'api' in d.running:
                raise RuntimeError('Next graceful shutdown is waiting for API SSE')
            original_stop(name)
        d.stop = stop
        app.execute('down')
        self.assertEqual(d.running, set())

    def test_failed_rollback_retains_journal_for_recovery(self):
        app, d = self.lifecycle(fail='worker', stop_fault=True)
        with self.assertRaises(RuntimeError): app.execute('up')
        self.assertTrue((self.home / 'transaction.json').exists())
        d.stop_fault = False
        app.execute('down')
        self.assertEqual(d.running, set())

    def test_fresh_frontend_snapshot_never_mutates_checkout_or_reuses_output(self):
        checkout = self.home / 'checkout'
        web = checkout / 'web'; web.mkdir(parents=True)
        (web / 'tsconfig.json').write_text('{"compilerOptions":{}}')
        (web / 'next-env.d.ts').write_text('original types')
        (web / 'node_modules').mkdir()
        (web / '.next').mkdir(); (web / '.next' / 'stale.css').write_text('old')
        target = self.home / 'build' / 'web'
        original = self.m.ROOT
        self.m.ROOT = checkout
        try:
            self.assertTrue(hasattr(self.m, 'stage_frontend'), 'fresh source snapshot is missing')
            self.m.stage_frontend(target)
            self.assertFalse((target / '.next').exists())
            self.assertFalse((target / 'node_modules').exists(), 'active builds must not follow mutable checkout dependencies')
            (target / 'tsconfig.json').write_text('Next generated config')
            (target / 'next-env.d.ts').write_text('Next generated types')
            self.assertEqual((web / 'tsconfig.json').read_text(), '{"compilerOptions":{}}')
            self.assertEqual((web / 'next-env.d.ts').read_text(), 'original types')
            with self.assertRaises(FileExistsError): self.m.stage_frontend(target)
        finally: self.m.ROOT = original

    def test_preflight_distinguishes_listener_from_closed_connection_time_wait(self):
        server = socket.socket()
        server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        server.bind(('127.0.0.1', 0)); server.listen()
        port = server.getsockname()[1]
        d = self.m.Driver.__new__(self.m.Driver)
        with self.assertRaisesRegex(RuntimeError, 'occupied'): d.free_port(port)
        client = socket.create_connection(('127.0.0.1', port))
        accepted, _ = server.accept()
        accepted.close()  # server initiates close and enters TIME_WAIT
        self.assertEqual(client.recv(1), b'')
        client.close(); server.close()
        d.free_port(port)  # closed SSE/HTTP connections must not block restart

    def test_health_transport_uses_private_api_secret_header(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200 if self.headers.get('X-Webchat-Secret') == 'test-secret' else 403)
                self.end_headers(); self.wfile.write(b'ready')
            def log_message(self, *args): pass
        server = http.server.HTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever); thread.start()
        try:
            self.assertEqual(self.m.http(f'http://127.0.0.1:{server.server_port}/readyz', 'test-secret'), b'ready')
        finally:
            server.shutdown(); thread.join(); server.server_close()

    def test_reused_pid_receipt_never_signals_unrelated_process(self):
        p = subprocess.Popen([sys.executable, '-c', 'import time; time.sleep(30)'])
        self.addCleanup(lambda: (p.terminate(), p.wait()) if p.poll() is None else None)
        receipt = {'pid': p.pid, 'identity': 'not the current process'}
        self.assertFalse(self.m.stop_process(receipt, timeout=1))
        self.assertIsNone(p.poll())

    def test_owned_supervisor_stops_and_joins_actual_service(self):
        receipt = self.home / 'service.json'
        p = subprocess.Popen([sys.executable, str(MODULE), '__service', str(receipt), sys.executable, '-c', 'import time; time.sleep(30)'], start_new_session=True)
        self.addCleanup(lambda: (p.terminate(), p.wait()) if p.poll() is None else None)
        for _ in range(100):
            if receipt.exists() and json.loads(receipt.read_text()).get('child'): break
            time.sleep(.05)
        state = json.loads(receipt.read_text())
        child = state['child']
        self.assertTrue(self.m.stop_process(state, timeout=5))
        p.wait(timeout=5)
        self.assertFalse(self.m.process_matches(child))

    def test_retitled_child_remains_owned_after_supervisor_death(self):
        receipt = self.home / 'web.json'
        p = subprocess.Popen([sys.executable, str(MODULE), '__service', str(receipt), 'node', '-e', "process.title='webchat-test-retitled';setInterval(()=>{},1000)"], start_new_session=True)
        child_pid = None
        try:
            for _ in range(100):
                if receipt.exists() and json.loads(receipt.read_text()).get('child'):
                    state = json.loads(receipt.read_text()); child_pid = state['child']['pid']
                    if self.m.identity(child_pid).endswith('webchat-test-retitled'): break
                time.sleep(.05)
            p.kill(); p.wait()
            self.assertTrue(self.m.process_matches(state['child']), 'mutable process title lost ownership identity')
            self.assertTrue(self.m.stop_process(state['child'], timeout=5))
        finally:
            if p.poll() is None: p.terminate(); p.wait(timeout=5)
            if child_pid:
                try: os.kill(child_pid, 15)
                except ProcessLookupError: pass

    def test_supervisor_releases_inherited_lifecycle_lock_only_after_receipt(self):
        receipt = self.home / 'api.json'
        lifecycle = open(self.home / 'lifecycle.lock', 'a+')
        fcntl.flock(lifecycle, fcntl.LOCK_EX)
        env = {**os.environ, 'WEBCHAT_LIFECYCLE_FD':str(lifecycle.fileno())}
        p = subprocess.Popen([sys.executable, str(MODULE), '__service', str(receipt), sys.executable, '-c', 'import time;time.sleep(30)'], env=env, pass_fds=(lifecycle.fileno(),), start_new_session=True)
        lifecycle.close()  # controller death: only unregistered supervisor retains lock
        try:
            with self.assertRaisesRegex(RuntimeError, 'another lifecycle'):
                with self.m.lifecycle_lock(self.home): pass
            for _ in range(100):
                if receipt.exists() and json.loads(receipt.read_text()).get('child'): break
                time.sleep(.05)
            self.assertTrue(receipt.exists(), 'supervisor failed to publish ownership')
            with self.m.lifecycle_lock(self.home): pass
        finally:
            p.terminate(); p.wait(timeout=5)

    def test_integration_retention_stops_without_deleting_owned_test_data(self):
        source = (self.m.ROOT / 'scripts/test-web-integration.sh').read_text()
        cleanup = 'cleanup() {' + source.split('cleanup() {', 1)[1].split('\n}\npython3 -', 1)[0] + '\n}'
        work = self.home / 'integration'; work.mkdir()
        for name in ('compose.yaml', 'receipt', 'temporal.db'): (work/name).write_text('retained')
        script = 'docker() { printf "docker %s\\n" "$*"; }; temporal_pid=; test_pid=; project=webchat-storage-test; volume=test; ' + cleanup + '\ncleanup'
        result = subprocess.run(['bash', '-c', script], env={**os.environ, 'work':str(work), 'WEBCHAT_TEST_RETAIN':'1'}, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stdout+result.stderr)
        self.assertIn('stop --timeout 10', result.stdout)
        self.assertNotIn('down', result.stdout)
        self.assertTrue((work/'receipt').is_file())
        self.assertTrue((work/'temporal.db').is_file())

    def test_concurrent_invocation_lock_and_death_release(self):
        code = 'import fcntl,sys,time; f=open(sys.argv[1],"a+"); fcntl.flock(f,fcntl.LOCK_EX); print("locked",flush=True); time.sleep(30)'
        p = subprocess.Popen([sys.executable, '-c', code, str(self.home / 'lifecycle.lock')], stdout=subprocess.PIPE, text=True)
        self.addCleanup(lambda: (p.terminate(), p.wait()) if p.poll() is None else None)
        self.assertEqual(p.stdout.readline().strip(), 'locked')
        with self.assertRaisesRegex(RuntimeError, 'another lifecycle'):
            with self.m.lifecycle_lock(self.home): pass
        p.kill(); p.wait(); p.stdout.close()
        with self.m.lifecycle_lock(self.home): pass


class FakeDriver:
    """Only slow external edges replaced; transaction and lock logic are real."""
    services = ['ollama', 'postgres', 'temporal-schema', 'temporal', 'temporal-namespace', 'temporal-ui', 'worker', 'api', 'web']
    def __init__(self, preflight_fault='', fail='', shared=True, stop_fault=False):
        self.preflight_fault, self.fail, self.shared, self.stop_fault = preflight_fault, fail, shared, stop_fault
        self.running, self.starts, self.stops, self.ready = set(), [], [], False
    def snapshot(self): return set(self.running)
    def preflight(self, mode):
        if self.preflight_fault: raise RuntimeError(self.preflight_fault)
    def needs_ollama(self): return not self.shared and 'ollama' not in self.running
    def start(self, name):
        if self.fail == name: raise RuntimeError(name)
        self.running.add(name); self.starts.append(name)
    def stop(self, name):
        if self.stop_fault: raise RuntimeError('stop unconfirmed')
        if name in self.running:
            self.running.remove(name); self.stops.append(name)
    def models(self, pull=False): pass
    def build(self):
        if self.fail == 'build': raise RuntimeError('build')
    def migrate(self):
        if self.fail == 'migrate': raise RuntimeError('migrate')
    def health(self):
        if self.fail == 'ready': raise RuntimeError('ready')
        self.ready = True
    def announce(self): pass


if __name__ == '__main__': unittest.main(verbosity=2)
