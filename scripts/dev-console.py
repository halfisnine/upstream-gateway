#!/usr/bin/env python3
"""Start/stop this repository's loopback-only development console (Linux + Docker)."""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
PRIVATE = ROOT / '.dev/private/console'
DATA = ROOT / '.cache/console-postgres'
BUILD = ROOT / 'build/dev'
DB = 'upstream-gateway-console-db'
IMAGE = 'mirror.ccs.tencentyun.com/library/postgres@sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994'


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs)


def inspect_db():
    result = subprocess.run(['docker', 'inspect', DB], capture_output=True, text=True)
    if result.returncode:
        return None
    info = json.loads(result.stdout)[0]
    if info['Config']['Labels'].get('upstream-gateway.workspace') != str(ROOT):
        raise SystemExit('A container with this name belongs to another workspace; refusing to modify it.')
    return info


def process(node):
    path = PRIVATE / (node + '.pid')
    if not path.exists():
        return None
    pid = int(path.read_text())
    try:
        command = Path(f'/proc/{pid}/cmdline').read_bytes().split(b'\0')[0].decode()
    except (FileNotFoundError, ProcessLookupError):
        return None
    if not command:
        return None
    if command != str(BUILD / 'gateway'):
        raise SystemExit('Saved process identity does not match this workspace; refusing to signal it.')
    return pid


def write_secret(name, text):
    path = PRIVATE / name
    path.write_text(text + '\n')
    path.chmod(0o600)
    return path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['start', 'stop', 'status'])
    parser.add_argument('--node', default='dev-a')
    parser.add_argument('--admin-port', type=int, default=19090)
    parser.add_argument('--data-port', type=int, default=18080)
    args = parser.parse_args()
    import re
    if not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,63}', args.node):
        parser.error('invalid node identifier')
    if not all(1024 <= p <= 65535 for p in [args.admin_port, args.data_port]):
        parser.error('ports must be between 1024 and 65535')
    PRIVATE.mkdir(parents=True, exist_ok=True)
    PRIVATE.chmod(0o700)
    BUILD.mkdir(parents=True, exist_ok=True)
    pid = process(args.node)
    if args.action == 'status':
        print(f'{args.node}: ' + (f'running (PID {pid})' if pid else 'stopped'))
        return
    if args.action == 'stop':
        if pid:
            os.kill(pid, signal.SIGTERM)
            for _ in range(350):
                if not process(args.node):
                    break
                time.sleep(0.1)
            else:
                raise SystemExit('Node is still draining; no force-stop was attempted.')
            print('Node stopped. Database and saved configuration are retained.')
        else:
            print('Node is already stopped. Database and saved configuration are retained.')
        return
    if pid:
        print(f'{args.node} is already running (PID {pid}).')
        return
    run([str(ROOT / '.dev/run'), 'go', 'build', '-trimpath', '-buildvcs=false', '-o', 'build/dev/gateway', './cmd/gateway'])
    env_path = PRIVATE / 'postgres.env'
    info = inspect_db()
    if not env_path.exists():
        if info or DATA.exists():
            raise SystemExit('Existing database has no local credentials; restore credentials instead of replacing data.')
        password = secrets.token_hex(32)
        write_secret('postgres.env', 'POSTGRES_DB=gateway_console\nPOSTGRES_USER=gateway_console\nPOSTGRES_PASSWORD=' + password)
        write_secret('master-key', secrets.token_hex(32))
        write_secret('admin-token', secrets.token_hex(32))
    settings = dict(line.split('=', 1) for line in env_path.read_text().splitlines())
    if not (PRIVATE / 'master-key').exists() or not (PRIVATE / 'admin-token').exists():
        raise SystemExit('Missing encryption key or administrator token; restore the original files.')
    if not info:
        DATA.mkdir(parents=True, exist_ok=True)
        run(['docker', 'run', '-d', '--pull=never', '--name', DB,
             '--label', 'upstream-gateway.workspace=' + str(ROOT),
             '--cpus=1', '--memory=256m', '--memory-swap=256m',
             '--env-file', str(env_path), '--mount', f'type=bind,source={DATA},target=/var/lib/postgresql/data',
             os.environ.get('GATEWAY_POSTGRES_IMAGE', IMAGE)], stdout=subprocess.DEVNULL)
    elif not info['State']['Running']:
        run(['docker', 'start', DB], stdout=subprocess.DEVNULL)
    for _ in range(30):
        result = subprocess.run(['docker', 'exec', DB, 'pg_isready', '-U', 'gateway_console', '-d', 'gateway_console'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if result.returncode == 0:
            break
        time.sleep(1)
    else:
        raise SystemExit('Development database did not become ready.')
    ip = inspect_db()['NetworkSettings']['Networks']['bridge']['IPAddress']
    write_secret('database-url', f"postgres://gateway_console:{settings['POSTGRES_PASSWORD']}@{ip}:5432/gateway_console?sslmode=disable")
    env = os.environ.copy()
    env.update(GATEWAY_DATABASE_URL_FILE=str(PRIVATE / 'database-url'),
               GATEWAY_MASTER_KEY_FILE=str(PRIVATE / 'master-key'),
               GATEWAY_ADMIN_TOKEN_FILE=str(PRIVATE / 'admin-token'),
               GATEWAY_NODE_ID=args.node, GATEWAY_LISTEN=f'127.0.0.1:{args.data_port}',
               GATEWAY_ADMIN_LISTEN=f'127.0.0.1:{args.admin_port}')
    run([str(BUILD / 'gateway'), '--migrate'], env=env)
    with (BUILD / (args.node + '.log')).open('ab') as log:
        child = subprocess.Popen([str(BUILD / 'gateway')], env=env, cwd=ROOT, stdout=log, stderr=log, start_new_session=True)
    (PRIVATE / (args.node + '.pid')).write_text(str(child.pid))
    time.sleep(0.5)
    if child.poll() is not None:
        raise SystemExit('Node could not start; inspect its project-local log (check port conflicts).')
    print(f'Console: http://127.0.0.1:{args.admin_port}')
    print(f'Model listener: http://127.0.0.1:{args.data_port}')
    print(f'Administrator token file: {PRIVATE / "admin-token"}')
    print('No production services or channels were modified.')


if __name__ == '__main__':
    main()
