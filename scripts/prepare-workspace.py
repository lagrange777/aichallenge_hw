#!/usr/bin/env python3
"""Create an isolated public-source snapshot. Never copies .env, .git or user data."""
import argparse
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
BLOCKED = {'data', 'artifacts', 'tools', 'documents', 'node_modules', 'vendor', 'bin', 'profiles', 'memory'}
EXTENSIONS = {'.go', '.md', '.yaml', '.yml', '.html', '.js', '.css'}


def public(name):
    p = Path(name)
    return not any(x.startswith('.') or x in BLOCKED for x in p.parts) and (p.suffix in EXTENSIONS or name in {'go.mod', 'Dockerfile'})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', default=str(ROOT / '.workspaces/project'))
    args = parser.parse_args()
    destination = Path(args.output).resolve()
    if destination.exists():
        raise SystemExit(f'Refusing to overwrite existing workspace: {destination}. Use a new --output path.')
    names = subprocess.check_output(['git', 'ls-files', '-z'], cwd=ROOT).decode().split('\0')
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT).decode().strip()
    files = []
    for name in names:
        if not name or not public(name):
            continue
        source = ROOT / name
        if any(part.is_symlink() for part in [source, *source.parents]) or not source.is_file():
            continue
        files.append((name, source.read_bytes()))
    destination.mkdir(parents=True)
    for name, content in files:
        target = destination / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content)
    # Local baseline makes all assistant edits inspectable with git diff.
    for args in [['init', '-q', '-b', 'workspace'], ['add', '.'],
                 ['-c', 'user.name=Workspace snapshot', '-c', 'user.email=workspace@localhost',
                  '-c', 'core.hooksPath=/dev/null', 'commit', '-qm', f'Public workspace snapshot from {revision}']]:
        subprocess.run(['git', *args], cwd=destination, check=True, stdout=subprocess.DEVNULL)
    print(json.dumps({'workspace': str(destination), 'base': revision, 'files': len(files)}, ensure_ascii=False))


if __name__ == '__main__':
    main()
