#!/usr/bin/env python3
"""Local-only lifecycle. Never cancels workflows, deletes data, or kills by port.
State is private to one checkout/home. A journal is written BEFORE acquisition;
interrupted operations reconcile under an OS lock, including after SIGKILL.
"""
import contextlib
import ctypes
import struct
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import shutil
import socket
import subprocess
import sys
import time
import urllib.request
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]
SERVICES = ['ollama', 'postgres', 'temporal-schema', 'temporal', 'temporal-namespace', 'temporal-ui', 'worker', 'api', 'web']
DOCKER_SERVICES = SERVICES[1:6]
# Close upstream SSE before Next's production server waits for open requests.
STOP_ORDER = ['api', 'web', 'worker', 'temporal-ui', 'temporal-namespace', 'temporal', 'temporal-schema', 'postgres', 'ollama']
MODELS = {'qwen3.5:9b': 'd63f830f28d515fefea546f213fca7a75e9dd5176ef51a6deabc952d1c99a911',
          'gemma4:e4b': 'eb2177adf7646d15faead94fdad6d3e170dc8015653418a3b7d5d52a6380be5b'}


def atomic(path, value):
    path = Path(path)
    tmp = path.with_name(path.name + '.' + secrets.token_hex(8) + '.tmp')
    with open(tmp, 'x', opener=lambda p, f: os.open(p, f, 0o600)) as out:
        json.dump(value, out, indent=2); out.flush(); os.fsync(out.fileno())
    os.replace(tmp, path)
    fd = os.open(path.parent, os.O_RDONLY)
    try: os.fsync(fd)
    finally: os.close(fd)


@contextlib.contextmanager
def lifecycle_lock(home):
    home.mkdir(parents=True, exist_ok=True, mode=0o700)
    with open(home / 'lifecycle.lock', 'a+') as lock:
        try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: raise RuntimeError('another lifecycle operation is running; retry after it exits')
        yield lock


def identity(pid):
    # PID alone is not identity. Exact start time + full command, including the
    # unique receipt path on supervisors, protects against stale/reused PIDs.
    p = subprocess.run(['ps', '-ww', '-p', str(pid), '-o', 'lstart=', '-o', 'command='], capture_output=True, text=True, timeout=5)
    return p.stdout.strip() if p.returncode == 0 else ''


def birth_identity(pid):
    # Kernel creation identity, not mutable argv or second-resolution ps time.
    # macOS Node rewrites argv AND the environment exposed by ps when retitled.
    if sys.platform == 'darwin':
        buf = ctypes.create_string_buffer(136)  # public proc_bsdinfo ABI
        lib = ctypes.CDLL('/usr/lib/libproc.dylib', use_errno=True)
        if lib.proc_pidinfo(int(pid), 3, 0, buf, len(buf)) != len(buf): return ''
        status, = struct.unpack_from('=I', buf.raw, 4)
        if status == 5: return ''  # SZOMB
        uid, = struct.unpack_from('=I', buf.raw, 20)
        group, = struct.unpack_from('=I', buf.raw, 100)
        sec, usec = struct.unpack_from('=QQ', buf.raw, 120)
        return f'darwin:{uid}:{group}:{sec}:{usec}'
    if sys.platform.startswith('linux'):
        try:
            proc = Path('/proc') / str(pid)
            stat = (proc/'stat').read_text().rsplit(')', 1)[1].split()
            if stat[0] == 'Z': return ''
            return ':'.join(['linux', (Path('/proc/sys/kernel/random/boot_id')).read_text().strip(), str(proc.stat().st_uid), stat[2], stat[19]])
        except (FileNotFoundError, ProcessLookupError): return ''
    raise RuntimeError('verified lifecycle process ownership supports macOS/Linux only')


def process_matches(receipt):
    if receipt.get('birth'):
        return birth_identity(receipt['pid']) == receipt['birth']
    return bool(receipt.get('identity')) and identity(receipt['pid']) == receipt['identity']


def stop_process(receipt, timeout=45):
    if not process_matches(receipt): return False
    os.kill(receipt['pid'], signal.SIGTERM)
    end = time.monotonic() + timeout
    while process_matches(receipt):
        # A child of the caller can remain as a zombie until wait().
        state = subprocess.run(['ps', '-p', str(receipt['pid']), '-o', 'stat='], capture_output=True, text=True, timeout=5).stdout
        if state.startswith('Z'): break
        if time.monotonic() >= end: raise RuntimeError('owned process did not exit; retained receipt, no speculative SIGKILL')
        time.sleep(.1)
    return True


def service(receipt_path, argv):
    """Supervisor publishes identity before starting a child. Parent death cannot
    lose a running child between Popen and receipt publication. Per-service lock
    excludes duplicate supervisors; child is joined on TERM, never disowned.
    """
    path = Path(receipt_path)
    with open(str(path) + '.lock', 'a+') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        stopping = False
        def term(_sig, _frame):
            nonlocal stopping
            stopping = True
        signal.signal(signal.SIGTERM, term); signal.signal(signal.SIGINT, term)
        own = {'pid': os.getpid(), 'birth': birth_identity(os.getpid()), 'identity': identity(os.getpid())}
        atomic(path, own)
        # Until this receipt exists, an orphaned launcher must keep lifecycle
        # serialization alive. Close only our inherited descriptor, never unlock
        # the parent's shared open-file description.
        inherited = os.environ.pop('WEBCHAT_LIFECYCLE_FD', None)
        if inherited is not None: os.close(int(inherited))
        if stopping: return
        child = subprocess.Popen(argv, start_new_session=True)
        own['child'] = {'pid': child.pid, 'birth': birth_identity(child.pid)}
        atomic(path, own)
        sent = False
        while child.poll() is None:
            if stopping and not sent:
                child.terminate(); sent = True
            time.sleep(.1)
        atomic(path, {**own, 'exited': True, 'exitCode': child.returncode})
        if child.returncode and not stopping: raise RuntimeError('service exited; inspect its log')


class Lifecycle:
    def __init__(self, home, driver):
        self.home, self.d = Path(home), driver
        self.journal = self.home / 'transaction.json'

    def rollback(self):
        if not self.journal.exists(): return
        tx = json.loads(self.journal.read_text())
        for name in STOP_ORDER:
            if name in tx['planned'] and name not in tx['before']: self.d.stop(name)
        self.journal.unlink()  # Only our completed transaction, never user data.

    def execute(self, mode):
        with lifecycle_lock(self.home) as lock:
            if hasattr(self.d, 'lock'): self.d.lock = lock
            self.rollback()
            if mode == 'down':
                for name in STOP_ORDER: self.d.stop(name)
                print('Stopped verified owned services. All data and models retained.')
                return
            self.d.preflight(mode)
            before = self.d.snapshot()
            tx = {'before': sorted(before), 'planned': []}
            atomic(self.journal, tx)
            def start(name):
                if name in before: return
                tx['planned'].append(name); atomic(self.journal, tx)
                self.d.start(name)
            try:
                if self.d.needs_ollama(): start('ollama')
                self.d.models(pull=mode == 'setup')
                if mode == 'setup':
                    self.d.build()
                    self.rollback()
                    print('Setup complete; no newly owned services left running. Run mise run up.')
                    return
                for name in DOCKER_SERVICES: start(name)
                self.d.migrate()
                for name in ('worker', 'api', 'web'): start(name)
                self.d.health()
                self.journal.unlink()
                self.d.announce()
            except BaseException:
                self.rollback()
                raise


def http(url, secret=None):
    headers = {'X-Webchat-Secret': secret} if secret else {}
    # Never inherit an HTTP proxy for local credentials or readiness.
    with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(urllib.request.Request(url, headers=headers), timeout=5) as response:
        return response.read()


def wait_for(check, label, timeout=120):
    end = time.monotonic() + timeout
    last = None
    while time.monotonic() < end:
        try:
            if check(): return
        except (OSError, ValueError, RuntimeError) as e: last = e
        time.sleep(.5)
    raise RuntimeError(f'{label} not ready within {timeout}s; inspect logs ({type(last).__name__})')


def stage_frontend(target):
    # Next writes next-env.d.ts and tsconfig during build. A private source
    # snapshot isolates those writes as well as output/cache from any live app.
    shutil.copytree(ROOT / 'web', target, ignore=shutil.ignore_patterns('node_modules', '.next', '.webchat-builds', 'test-results', 'playwright-report', '*.tsbuildinfo'))


class Driver:
    services = SERVICES
    def __init__(self, home):
        self.home = home
        self.lock = None
        self.logs = home / 'logs'; self.logs.mkdir(exist_ok=True)
        config = home / 'config.json'
        if config.exists():
            self.cfg = json.loads(config.read_text())
            if self.cfg['checkout'] != str(ROOT) or self.cfg['home'] != str(home):
                raise RuntimeError('runtime home belongs to another checkout/path; do not copy ownership metadata')
        else:
            self.cfg = self.new_config(home)
            atomic(config, self.cfg)
        self.env = {**os.environ, **self.cfg['env']}
        self.compose_file = home / 'compose-ownership.json'
        atomic(self.compose_file, {'services': {s: {'labels': {'webchat.lifecycle.owner': self.cfg['owner']}} for s in DOCKER_SERVICES}})
        self.compose = ['docker', 'compose', '--project-name', self.cfg['project'], '--env-file', '/dev/null', '-f', str(ROOT / 'compose.yaml'), '-f', str(self.compose_file)]

    def new_config(self, home):
        def port(key, default):
            value = os.environ.get(key, str(default))
            if not value.isdigit() or not 1024 <= int(value) <= 65535: raise RuntimeError(f'invalid {key}')
            return value
        ports = {k: port(k, v) for k, v in {'WEBCHAT_POSTGRES_PORT':15432, 'WEBCHAT_TEMPORAL_PORT':17233, 'WEBCHAT_TEMPORAL_UI_PORT':18080, 'WEBCHAT_API_PORT':18081, 'WEBCHAT_WEB_PORT':13000, 'WEBCHAT_OLLAMA_PORT':11434}.items()}
        if len(set(ports.values())) != len(ports): raise RuntimeError('ports must be distinct')
        owner = secrets.token_hex(16)
        env = {**ports, 'WEBCHAT_POSTGRES_PASSWORD':secrets.token_hex(32), 'WEBCHAT_DB_PASSWORD':secrets.token_hex(32), 'WEBCHAT_TEMPORAL_DB_PASSWORD':secrets.token_hex(32), 'WEBCHAT_PROXY_SECRET':secrets.token_hex(32)}
        env.update(WEBCHAT_DATABASE_URL=f"postgres://webchat:{env['WEBCHAT_DB_PASSWORD']}@127.0.0.1:{ports['WEBCHAT_POSTGRES_PORT']}/webchat?sslmode=disable", WEBCHAT_API_LISTEN='127.0.0.1:'+ports['WEBCHAT_API_PORT'], WEBCHAT_API_URL='http://127.0.0.1:'+ports['WEBCHAT_API_PORT'], WEBCHAT_BROWSER_ORIGIN='http://127.0.0.1:'+ports['WEBCHAT_WEB_PORT'], WEBCHAT_TEMPORAL_ADDRESS='127.0.0.1:'+ports['WEBCHAT_TEMPORAL_PORT'], WEBCHAT_OLLAMA_URL='http://127.0.0.1:'+ports['WEBCHAT_OLLAMA_PORT'], OLLAMA_HOST='127.0.0.1:'+ports['WEBCHAT_OLLAMA_PORT'], WEBCHAT_ATTACHMENT_ROOT=str(home / 'attachments'))
        return {'checkout':str(ROOT), 'home':str(home), 'owner':owner, 'project':'webchat-'+owner, 'env':env}

    def run(self, argv, timeout=120, capture=False):
        # The running command keeps the lifecycle lock if its controller dies.
        kwargs = {'cwd':ROOT, 'env':self.env, 'timeout':timeout, 'check':True, 'text':True,
                  'pass_fds':(self.lock.fileno(),) if self.lock else ()}
        if capture: kwargs['stdout'] = subprocess.PIPE; kwargs['stderr'] = subprocess.PIPE
        result = subprocess.run(argv, **kwargs)
        return result.stdout if capture else ''

    def container(self, name):
        ids = self.run(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project='+self.cfg['project'], '--filter', 'label=com.docker.compose.service='+name], capture=True).split()
        if not ids: return None
        if len(ids) != 1: raise RuntimeError(f'ambiguous {name} containers; inspect exact project, no automatic cleanup')
        info = json.loads(self.run(['docker', 'inspect', ids[0]], capture=True))[0]
        if info['Config']['Labels'].get('webchat.lifecycle.owner') != self.cfg['owner']:
            raise RuntimeError(f'unowned {name} container; refusing to start/stop/reconfigure')
        return info

    def receipt(self, name):
        path = self.home / (name+'.json')
        return json.loads(path.read_text()) if path.exists() else {}

    def active(self, name):
        if name in DOCKER_SERVICES:
            c = self.container(name)
            # Completed init jobs are already satisfied until explicit down.
            return bool(c and (c['State']['Running'] or (name in ('temporal-schema','temporal-namespace') and c['State']['ExitCode'] == 0)))
        r = self.receipt(name)
        return bool(not r.get('exited') and process_matches(r))

    def snapshot(self): return {s for s in SERVICES if self.active(s)}

    def preflight(self, mode):
        try: self.run(['docker', 'info'], capture=True); self.run(['docker', 'compose', 'version'], capture=True)
        except (OSError, subprocess.SubprocessError): raise RuntimeError('Docker daemon/Compose unavailable: install/start Docker, then retry')
        if mode == 'setup' and any(self.active(s) for s in ('worker', 'api', 'web')):
            raise RuntimeError('owned application is running: run mise run down before setup; data is retained')
        if mode == 'up' and not (self.home / 'build.json').exists(): raise RuntimeError('build missing: run mise run setup')
        for name, key in [('postgres','WEBCHAT_POSTGRES_PORT'), ('temporal','WEBCHAT_TEMPORAL_PORT'), ('temporal-ui','WEBCHAT_TEMPORAL_UI_PORT'), ('api','WEBCHAT_API_PORT'), ('web','WEBCHAT_WEB_PORT')]:
            if mode == 'setup' or self.active(name): continue
            self.free_port(self.env[key])
        if mode == 'up':
            # Probe helper execution, not merely a local mutable tag's presence.
            self.run(['docker','image','inspect','webchat-attachment-parser:1'], capture=True)
            built = json.loads((self.home / 'build.json').read_text())
            image = self.run(['docker','image','inspect','--format','{{.Id}}','webchat-attachment-parser:1'], capture=True).strip()
            if image != built['parserImage']: raise RuntimeError('parser image changed: run setup/requalify, never silently reuse a different helper')
            self.run(['docker','run','--rm','--network','none','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges','--user','65534:65534','--memory','512m','--pids-limit','32','--entrypoint','node','webchat-attachment-parser:1','--version'], capture=True)
        root = Path(self.env['WEBCHAT_ATTACHMENT_ROOT']); root.mkdir(parents=True, exist_ok=True, mode=0o700)
        # Fail early if original publication's hard-link/atomic-rename contract is unavailable.
        probe = root / ('.lifecycle-probe-'+secrets.token_hex(16)); link = probe.with_suffix('.link')
        try:
            with open(probe, 'xb') as f: f.write(b'probe'); f.flush(); os.fsync(f.fileno())
            os.link(probe, link)
        finally:
            probe.unlink(missing_ok=True); link.unlink(missing_ok=True)

    def free_port(self, port):
        with socket.socket() as s:
            # Match server restart semantics: TIME_WAIT is not a live listener.
            # No SO_REUSEPORT: an actual existing listener still fails closed.
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            try: s.bind(('127.0.0.1', int(port)))
            except OSError: raise RuntimeError(f'port {port} is occupied by an unowned service; choose a separate WEBCHAT_HOME/ports, never kill by port')

    def needs_ollama(self):
        try:
            version = json.loads(http(self.env['WEBCHAT_OLLAMA_URL']+'/api/version'))
        except Exception:
            self.free_port(self.env['WEBCHAT_OLLAMA_PORT'])
            return True
        if version.get('version') != '0.40.15': raise RuntimeError('shared Ollama version mismatch; use a separate endpoint, do not stop/reconfigure shared Ollama')
        print('Reusing compatible Ollama endpoint; ownership remains unchanged.')
        return False

    def models(self, pull=False):
        if pull:
            print('Ensuring both exact models (large multi-GB downloads); existing tags/configuration are not replaced.', flush=True)
        data = json.loads(http(self.env['WEBCHAT_OLLAMA_URL']+'/api/tags'))
        present = {m['name']: m for m in data['models']}
        for name, digest in MODELS.items():
            if name not in present:
                if not pull: raise RuntimeError(f'model {name} missing; run mise run setup (no fallback)')
                self.run(['ollama','pull',name], timeout=3600)
                present = {m['name']:m for m in json.loads(http(self.env['WEBCHAT_OLLAMA_URL']+'/api/tags'))['models']}
            m = present[name]
            if m.get('digest') != digest or m.get('details',{}).get('format') != 'safetensors' or m.get('details',{}).get('runner') != 'mlx' or m.get('remote_host') or m.get('remote_model'):
                raise RuntimeError(f'{name} does not match narrow image qualification (digest/safetensors/MLX). Use the qualified runtime or explicitly requalify; no replacement/fallback performed. See docs/web-chat-image-profile.md')
        if json.loads(http(self.env['WEBCHAT_OLLAMA_URL']+'/api/version')).get('version') != '0.40.15': raise RuntimeError('Ollama runtime identity changed')

    def start(self, name):
        if name in DOCKER_SERVICES:
            self.container(name)  # Reject unexpected ownership even on stopped objects.
            self.run(self.compose+['up','-d','--no-deps','--no-recreate',name], timeout=180)
            def ready():
                c = self.container(name)
                if not c: return False
                state = c['State']
                if name in ('temporal-schema','temporal-namespace'):
                    if not state['Running'] and state['ExitCode'] != 0: raise RuntimeError(name+' failed; inspect Docker logs')
                    return not state['Running'] and state['ExitCode'] == 0
                return state.get('Health',{}).get('Status') == 'healthy'
            wait_for(ready, name, 180)
            return
        r = self.receipt(name)
        if r and not r.get('exited'):
            if process_matches(r): return
            if process_matches(r.get('child',{})):
                raise RuntimeError(f'{name} supervisor lost but verified child remains; run down to join it before up')
        env = self.env.copy()
        if name == 'ollama': argv = ['ollama','serve']
        else:
            build = json.loads((self.home / 'build.json').read_text())
            argv = [str(Path(build['bin']) / ('web-'+name))]
            if name == 'web': argv = ['node', str(Path(build['web'])/'node_modules/next/dist/bin/next'), 'start', build['web'], '--hostname','127.0.0.1','--port',self.env['WEBCHAT_WEB_PORT']]
        receipt = self.home / (name+'.json')
        # Keep old receipts as evidence rather than overwriting a stale live claim.
        if receipt.exists(): receipt.rename(self.home / (name+'.previous-'+secrets.token_hex(8)+'.json'))
        if self.lock is None: raise RuntimeError('service launch requires lifecycle lock')
        env['WEBCHAT_LIFECYCLE_FD'] = str(self.lock.fileno())
        with open(self.logs/(name+'.log'), 'ab') as log:
            p = subprocess.Popen([sys.executable,str(Path(__file__).resolve()),'__service',str(receipt),*argv], cwd=ROOT, env=env, stdin=subprocess.DEVNULL, stdout=log, stderr=log, start_new_session=True, pass_fds=(self.lock.fileno(),))
        wait_for(lambda: self.active(name) and bool(self.receipt(name).get('child')), name+' supervisor', 15)
        if name == 'ollama': wait_for(lambda: json.loads(http(self.env['WEBCHAT_OLLAMA_URL']+'/api/version')).get('version') == '0.40.15', 'Ollama', 60)
        # Popen objects need not own/reap these deliberately detached services.
        p.returncode = 0

    def stop(self, name):
        if name in DOCKER_SERVICES:
            c = self.container(name)
            if c and c['State']['Running']:
                self.run(['docker','stop','--timeout','45',c['Id']], timeout=60)
                if self.container(name)['State']['Running']: raise RuntimeError('container shutdown unconfirmed: '+name)
            return
        r = self.receipt(name)
        if not r: return
        if process_matches(r): stop_process(r)
        # Supervisor SIGKILL can leave its recorded child; verify exact identity.
        child = r.get('child',{})
        if process_matches(child): stop_process(child)
        if process_matches(child): raise RuntimeError(name+' child exit unconfirmed')
        print(name+': local process exit verified (not provider/GPU reclamation)')

    def build(self):
        self.run(['go','mod','download'], timeout=300)
        self.run(['docker','build','-t','webchat-attachment-parser:1','infra/attachment-parser'], timeout=600)
        build_id = secrets.token_hex(16)
        bindir = self.home/'builds'/build_id; bindir.mkdir(parents=True)
        for app in ('api','worker'):
            self.run(['go','build','-o',str(bindir/('web-'+app)),'./cmd/web-'+app], timeout=300)
        web = bindir / 'web'
        stage_frontend(web)
        # Dependencies belong to this build too: a later setup/checkout install
        # cannot mutate a live application's module links. pnpm's content-addressed
        # store may be shared, but not node_modules or generated output.
        self.run(['pnpm','--dir',str(web),'install','--frozen-lockfile'], timeout=300)
        self.run(['pnpm','--dir',str(web),'exec','next','build','--webpack'], timeout=600)
        if not (web/'.next/BUILD_ID').is_file(): raise RuntimeError('fresh Next output missing BUILD_ID')
        image = self.run(['docker','image','inspect','--format','{{.Id}}','webchat-attachment-parser:1'], capture=True).strip()
        atomic(self.home/'build.json', {'bin':str(bindir), 'web':str(web), 'parserImage':image})

    def migrate(self):
        build = json.loads((self.home/'build.json').read_text())
        self.run([str(Path(build['bin'])/'web-api'),'-migrate'], timeout=120)

    def health(self):
        wait_for(lambda: bool(http(self.env['WEBCHAT_API_URL']+'/readyz', self.env['WEBCHAT_PROXY_SECRET'])), 'API/three worker queues/migrations', 120)
        wait_for(lambda: bool(http(self.env['WEBCHAT_BROWSER_ORIGIN']+'/')), 'browser', 60)
        body = http(self.env['WEBCHAT_BROWSER_ORIGIN']+'/').decode()
        assets = set(re.findall(r'/_next/static/[^"\s<>]+?\.css', body))
        if not assets: raise RuntimeError('served frontend has no verifiable current CSS')
        web = Path(json.loads((self.home/'build.json').read_text())['web'])
        for asset in assets:
            path = web/'.next'/asset.removeprefix('/_next/')
            if http(self.env['WEBCHAT_BROWSER_ORIGIN']+asset) != path.read_bytes(): raise RuntimeError('served frontend assets differ from fresh build; refusing readiness')
        if not all(self.active(s) for s in ('worker','api','web')): raise RuntimeError('required owned service exited')
        print('Verified served CSS equals fresh build output; all three queues polling.')

    def announce(self):
        print('Browser: '+self.env['WEBCHAT_BROWSER_ORIGIN'])
        print('Temporal UI: http://127.0.0.1:'+self.env['WEBCHAT_TEMPORAL_UI_PORT'])
        print('Logs: '+str(self.logs)+'; Docker logs: docker compose -p '+self.cfg['project']+' logs')
        print('Persistent attachments: '+self.env['WEBCHAT_ATTACHMENT_ROOT'])
        print('Postgres volume: '+self.cfg['project']+'_postgres-data')
        print('Qualification limitations remain: larger images and narrow Qwen parent reliability are NOT certified.')


def main():
    if len(sys.argv)>1 and sys.argv[1]=='__service':
        service(sys.argv[2],sys.argv[3:]); return
    if len(sys.argv)!=2 or sys.argv[1] not in ('setup','up','down'): raise RuntimeError('usage: lifecycle.py setup|up|down')
    raw = Path(os.environ.get('WEBCHAT_HOME', str(ROOT/'.webchat')))
    if not raw.is_absolute(): raise RuntimeError('WEBCHAT_HOME must be absolute')
    home = raw.resolve(); home.mkdir(parents=True,exist_ok=True,mode=0o700)
    # Initialization also serialized: never race credential/project generation.
    with lifecycle_lock(home): driver = Driver(home)
    Lifecycle(home,driver).execute(sys.argv[1])


if __name__=='__main__':
    try: main()
    except (Exception, KeyboardInterrupt) as e:
        # Exceptions intentionally exclude command env/secret-bearing arguments.
        print('webchat lifecycle: '+(str(e) if isinstance(e, RuntimeError) else type(e).__name__+'; inspect logs/configuration'),file=sys.stderr)
        sys.exit(1)
