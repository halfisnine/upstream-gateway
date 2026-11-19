#!/usr/bin/env python3
"""Real browser acceptance against disposable project-owned PostgreSQL and nodes."""
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
POSTGRES = os.environ.get("GATEWAY_POSTGRES_IMAGE", "mirror.ccs.tencentyun.com/library/postgres@sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994")

def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]

def main():
    subprocess.run([str(ROOT / ".dev/run"), "go", "build", "-o", "build/error-rules-check/gateway", "./cmd/gateway"], cwd=ROOT, check=True)
    private = ROOT / ".dev/private"
    private.mkdir(parents=True, exist_ok=True)
    name = "upstream-gateway-rules-test-" + secrets.token_hex(5)
    processes = []
    streams = []
    with tempfile.TemporaryDirectory(prefix="rules-acceptance-", dir=private) as temporary:
        directory = Path(temporary)
        try:
            password = secrets.token_hex(32)
            (directory / "postgres.env").write_text("POSTGRES_DB=gateway_test\nPOSTGRES_USER=gateway_test\nPOSTGRES_PASSWORD=" + password + "\n")
            subprocess.run(["docker", "run", "-d", "--name", name, "--cpus=1", "--memory=256m", "--memory-swap=256m", "--env-file", str(directory / "postgres.env"), "--tmpfs", "/var/lib/postgresql/data:rw", "-p", "127.0.0.1::5432", POSTGRES], check=True, stdout=subprocess.DEVNULL)
            database_port = subprocess.check_output(["docker", "port", name, "5432/tcp"], text=True).strip().split(":")[-1]
            for _ in range(30):
                ready = subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "gateway_test", "-d", "gateway_test"], stdout=subprocess.DEVNULL).returncode == 0
                if ready:
                    break
                time.sleep(.5)
            if not ready:
                raise RuntimeError("test database did not become ready")
            (directory / "database-url").write_text(f"postgres://gateway_test:{password}@127.0.0.1:{database_port}/gateway_test?sslmode=disable")
            (directory / "master-key").write_text(secrets.token_hex(32))
            (directory / "admin-token").write_text(secrets.token_hex(32))
            env = os.environ.copy()
            env.update(GATEWAY_DATABASE_URL_FILE=str(directory / "database-url"), GATEWAY_MASTER_KEY_FILE=str(directory / "master-key"), GATEWAY_ADMIN_TOKEN_FILE=str(directory / "admin-token"))
            binary = str(ROOT / "build/error-rules-check/gateway")
            subprocess.run([binary, "--migrate"], env=env, check=True)
            ports = [port() for _ in range(4)]
            for i in range(2):
                env.update(GATEWAY_NODE_ID=f"rules-test-{i}", GATEWAY_ADMIN_LISTEN=f"127.0.0.1:{ports[i*2]}", GATEWAY_LISTEN=f"127.0.0.1:{ports[i*2+1]}")
                stream = open(directory / f"node-{i}.log", "w")
                streams.append(stream)
                processes.append(subprocess.Popen([binary], env=env, stdout=stream, stderr=stream))
            time.sleep(.5)
            env.update(GATEWAY_RULES_CHECK_ADMIN=f"http://127.0.0.1:{ports[0]}", GATEWAY_RULES_CHECK_DATA=f"http://127.0.0.1:{ports[1]}", GATEWAY_RULES_CHECK_SECOND_ADMIN=f"http://127.0.0.1:{ports[2]}", GATEWAY_RULES_CHECK_SECOND_DATA=f"http://127.0.0.1:{ports[3]}", GATEWAY_RULES_CHECK_TOKEN_FILE=str(directory / "admin-token"))
            env.setdefault("PLAYWRIGHT_MODULE", str(ROOT / ".cache/browser/node_modules/playwright"))
            env.setdefault("PLAYWRIGHT_BROWSERS_PATH", str(ROOT / ".cache/browser-binaries"))
            env.setdefault("FONTCONFIG_FILE", str(ROOT / ".cache/fonts.conf"))
            subprocess.run(["node", "scripts/browser-error-rules.cjs"], env=env, cwd=ROOT, check=True, timeout=180)
        finally:
            for process in processes:
                process.terminate()
            for process in processes:
                try:
                    process.wait(timeout=40)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            for stream in streams:
                stream.close()
            subprocess.run(["docker", "rm", "-f", "-v", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

if __name__ == "__main__":
    main()
