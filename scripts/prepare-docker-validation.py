#!/usr/bin/env python3
"""Create local-only secrets for compose.validation.yml without replacing existing values."""
from pathlib import Path
import secrets
import subprocess

ROOT = Path(__file__).resolve().parents[1]
PRIVATE = ROOT / '.dev/private/docker-validation'
NAMES = ('database-url', 'master-key', 'admin-token', 'postgres-password')


def main():
    if PRIVATE.exists():
        if not all((PRIVATE / name).is_file() and (PRIVATE / name).stat().st_size for name in NAMES):
            raise SystemExit('Validation secrets are incomplete. Restore the original files; refusing to replace database credentials or the encryption key.')
        print('Existing validation secrets retained.')
        return
    # Refuse fresh credentials against the default project's existing database.
    found = subprocess.run(['docker', 'volume', 'ls', '--filter',
                            'label=com.docker.compose.project=upstream-gateway-validation',
                            '--format', '{{.Name}}'], check=True, capture_output=True, text=True)
    if found.stdout.strip():
        raise SystemExit('Existing validation volume has no secrets. Restore the original secret directory first.')
    PRIVATE.mkdir(parents=True, mode=0o700)
    PRIVATE.chmod(0o700)
    password = secrets.token_hex(32)
    values = {
        'postgres-password': password,
        'database-url': f'postgres://gateway_validation:{password}@database:5432/gateway_validation?sslmode=disable',
        'master-key': secrets.token_hex(32),
        'admin-token': secrets.token_hex(32),
    }
    for name, value in values.items():
        path = PRIVATE / name
        with path.open('x') as output:
            output.write(value + '\n')
        # Compose mounts individual files read-only. The non-root gateway must
        # be able to read them; their host parent directory remains owner-only.
        path.chmod(0o644)
    print(f'Validation secrets created in {PRIVATE}')
    print('Administrator token file: ' + str(PRIVATE / 'admin-token'))
    print('No existing console or production configuration was changed.')


if __name__ == '__main__':
    main()
